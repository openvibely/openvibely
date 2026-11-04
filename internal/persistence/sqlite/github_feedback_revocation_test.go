package sqlite

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestQueuedGitHubFeedbackRechecksReviewer(t *testing.T) {
	for _, identified := range []bool{false, true} {
		for _, boundary := range []string{"promotion", "conversion", "prepare", "commit", "consumed"} {
			name := boundary
			if identified {
				name += "/identified"
			} else {
				name += "/legacy"
			}
			t.Run(name, func(t *testing.T) {
				db := testutil.NewTestDB(t)
				ctx := t.Context()
				admission := ctx
				if identified {
					admission = models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
				}
				actor := &models.GitHubAuthorizedActor{GitHubLogin: "Reviewer", Permission: "triage", AddedBy: "test"}
				auth := NewGitHubAuthRepo(db)
				require.NoError(t, auth.UpsertAuthorizedActor(ctx, actor))
				task := createThreadInputTask(t, ctx, db, "default")
				agent := createThreadInputLLMConfig(t, ctx, db)
				pr := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/example/repo/pull/42", PRState: "open"}
				require.NoError(t, NewTaskPullRequestRepo(db).Upsert(ctx, pr))
				feedback := &models.GitHubPRFeedbackForwarded{TaskPullRequestID: pr.ID, TaskID: task.ID, RepoFullName: "example/repo", PRNumber: 42, FeedbackKind: "issue_comment", GitHubID: "review", AuthorLogin: "reviewer", Body: "revise", CreatedAt: time.Now()}
				input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, AgentConfigID: agent.ID, Content: "revise"}
				recorded, err := NewGitHubPRFeedbackRepo(db).RecordForwardedAndQueue(admission, feedback, input)
				require.NoError(t, err)
				require.True(t, recorded)
				inputs := NewThreadInputRepo(db)
				executions := NewExecutionRepo(db)
				execution := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning}
				if boundary != "promotion" {
					require.NoError(t, executions.Create(admission, execution))
					_, err = db.Exec(`UPDATE thread_inputs SET run_execution_id=? WHERE id=?`, execution.ID, input.ID)
					require.NoError(t, err)
					if boundary == "prepare" || boundary == "commit" || boundary == "consumed" {
						_, err = inputs.ConvertQueuedToSteering(admission, input.ID, execution.ID, execution.ID)
						require.NoError(t, err)
					}
					if boundary == "commit" || boundary == "consumed" {
						prepared, err := inputs.PreparePendingSteering(ctx, execution.ID, execution.ID)
						require.NoError(t, err)
						require.Len(t, prepared, 1)
					}
				} else {
					require.NoError(t, NewTaskRepo(db, nil).UpdateStatus(ctx, task.ID, models.StatusCompleted))
				}
				if boundary == "consumed" {
					_, err = db.Exec(`CREATE TRIGGER reject_reviewer_snapshot BEFORE INSERT ON github_reviewer_execution_provenance BEGIN SELECT RAISE(ABORT,'reviewer snapshot unavailable'); END`)
					require.NoError(t, err)
					require.ErrorContains(t, inputs.CommitLocalSteering(ctx, persistence.LocalSteeringCommit{ExecutionID: execution.ID, InputIDs: []string{input.ID}, ResponsesHistory: []any{"revise"}}), "reviewer snapshot unavailable")
					saved, err := inputs.GetByID(ctx, input.ID)
					require.NoError(t, err)
					require.Equal(t, models.ThreadInputPending, saved.InputStatus)
					replay, err := executions.ReplayMessagesByExecutionIDs(ctx, []string{execution.ID})
					require.NoError(t, err)
					require.Empty(t, replay[execution.ID])
					_, err = db.Exec(`DROP TRIGGER reject_reviewer_snapshot`)
					require.NoError(t, err)
					require.NoError(t, inputs.CommitLocalSteering(ctx, persistence.LocalSteeringCommit{ExecutionID: execution.ID, InputIDs: []string{input.ID}, ResponsesHistory: []any{"revise"}}))
					_, err = db.Exec(`DELETE FROM task_pull_requests WHERE id=?`, pr.ID)
					require.NoError(t, err)
					_, err = db.Exec(`DELETE FROM thread_inputs WHERE id=?`, input.ID)
					require.NoError(t, err)
					require.NoError(t, NewRunIdentityRepo(db).CheckExecutionGoal(ctx, execution.ID, task.ID))
				}
				require.NoError(t, auth.DeleteAuthorizedActor(ctx, actor.ID))
				if boundary == "consumed" {
					require.ErrorIs(t, NewRunIdentityRepo(db).CheckExecutionGoal(ctx, execution.ID, task.ID), persistence.ErrProjectDenied)
					require.ErrorIs(t, executions.MarkRunning(ctx, execution.ID), persistence.ErrProjectDenied)
					return
				}
				// Feedback retention must not determine whether queued instructions are authorized.
				_, err = db.Exec(`DELETE FROM task_pull_requests WHERE id=?`, pr.ID)
				require.NoError(t, err)

				switch boundary {
				case "promotion":
					require.ErrorIs(t, inputs.ClaimQueuedForTaskExecution(ctx, input.ID, execution), persistence.ErrProjectDenied)
					var count int
					require.NoError(t, db.QueryRow(`SELECT count(*) FROM executions WHERE task_id=?`, task.ID).Scan(&count))
					require.Zero(t, count)
				case "conversion":
					_, err = inputs.ConvertQueuedToSteering(admission, input.ID, execution.ID, execution.ID)
					require.ErrorIs(t, err, persistence.ErrProjectDenied)
				case "prepare":
					prepared, err := inputs.PreparePendingSteering(ctx, execution.ID, execution.ID)
					require.NoError(t, err)
					require.Empty(t, prepared)
				case "commit":
					require.ErrorIs(t, inputs.CommitLocalSteering(ctx, persistence.LocalSteeringCommit{ExecutionID: execution.ID, InputIDs: []string{input.ID}, ResponsesHistory: []any{"revise"}}), persistence.ErrProjectDenied)
				}
				saved, err := inputs.GetByID(ctx, input.ID)
				require.NoError(t, err)
				if boundary == "prepare" {
					require.Equal(t, models.ThreadInputStatus("cancelled"), saved.InputStatus)
				} else {
					require.Equal(t, models.ThreadInputPending, saved.InputStatus)
				}
				if boundary != "promotion" {
					replay, err := executions.ReplayMessagesByExecutionIDs(ctx, []string{execution.ID})
					require.NoError(t, err)
					require.Empty(t, replay[execution.ID])
				}
			})
		}
	}
}

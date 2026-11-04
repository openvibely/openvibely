package sqlite

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestProviderSteeringPreservesRevokedReviewerEvidence(t *testing.T) {
	for _, state := range []string{ProviderSteeringAcceptedPending, ProviderSteeringAcceptedAmbiguous, ProviderSteeringAcceptedConfirmed, "legacy_callback"} {
		t.Run(state, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := t.Context()
			owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
			task := createThreadInputTask(t, ctx, db, "default")
			execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
			executions := NewExecutionRepo(db)
			require.NoError(t, executions.Create(owner, execution))
			reviewer := &models.GitHubAuthorizedActor{GitHubLogin: "reviewer", Permission: "triage", AddedBy: "test"}
			auth := NewGitHubAuthRepo(db)
			require.NoError(t, auth.UpsertAuthorizedActor(ctx, reviewer))
			pr := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/example/repo/pull/42", PRState: "open"}
			require.NoError(t, NewTaskPullRequestRepo(db).Upsert(ctx, pr))
			input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, RunExecutionID: execution.ID, Content: "revise"}
			feedback := &models.GitHubPRFeedbackForwarded{TaskPullRequestID: pr.ID, TaskID: task.ID, RepoFullName: "example/repo", PRNumber: 42, FeedbackKind: "review", GitHubID: "review", AuthorLogin: "reviewer", Body: "revise", CreatedAt: time.Now()}
			recorded, err := NewGitHubPRFeedbackRepo(db).RecordForwardedAndQueue(owner, feedback, input)
			require.NoError(t, err)
			require.True(t, recorded)
			inputs := NewThreadInputRepo(db)
			_, err = inputs.ConvertQueuedToSteering(owner, input.ID, execution.ID, execution.ID)
			require.NoError(t, err)
			prepared, err := inputs.PreparePendingTextSteering(ctx, execution.ID, execution.ID)
			require.NoError(t, err)
			require.Len(t, prepared, 1)
			// Revocation after the provider call must not prevent recording its outcome.
			require.NoError(t, inputs.CheckSteeringDelivery(ctx, execution.ID, []string{input.ID}))
			require.ErrorIs(t, inputs.CheckSteeringDelivery(ctx, execution.ID, []string{"missing-input"}), persistence.ErrProjectDenied)
			require.NoError(t, auth.DeleteAuthorizedActor(ctx, reviewer.ID))
			require.ErrorIs(t, inputs.CheckSteeringDelivery(ctx, execution.ID, []string{input.ID}), persistence.ErrProjectDenied)
			_, err = db.Exec(`CREATE TRIGGER reject_provider_reviewer BEFORE INSERT ON github_reviewer_execution_provenance BEGIN SELECT RAISE(ABORT,'snapshot unavailable'); END`)
			require.NoError(t, err)
			record := func() error {
				if state == "legacy_callback" {
					return inputs.MarkApplied(ctx, input.ID, execution.ID, execution.ID)
				}
				return inputs.RecordProviderSteering(ctx, []string{input.ID}, "steer", "previous", "response", state)
			}
			require.ErrorContains(t, record(), "snapshot unavailable")
			var count int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM thread_input_provider_steering WHERE thread_input_id=?`, input.ID).Scan(&count))
			require.Zero(t, count)
			saved, err := inputs.GetByID(ctx, input.ID)
			require.NoError(t, err)
			require.Equal(t, models.ThreadInputPending, saved.InputStatus)
			_, err = db.Exec(`DROP TRIGGER reject_provider_reviewer`)
			require.NoError(t, err)
			require.NoError(t, record())
			foreign := createRuntimeScheduledTask(t, ctx, NewTaskRepo(db, nil), "default", "Other task")
			other := &models.Execution{TaskID: foreign.ID, Status: models.ExecRunning}
			require.NoError(t, executions.Create(owner, other))
			require.ErrorIs(t, inputs.CheckSteeringDelivery(ctx, other.ID, []string{input.ID}), persistence.ErrProjectDenied)
			require.ErrorIs(t, inputs.CheckSteeringDelivery(ctx, execution.ID, nil), persistence.ErrProjectDenied)
			require.ErrorIs(t, inputs.MarkApplied(ctx, input.ID, other.ID, other.ID), persistence.ErrProjectDenied)
			require.NoError(t, NewRunIdentityRepo(db).CheckExecutionGoal(ctx, other.ID, foreign.ID))

			saved, err = inputs.GetByID(ctx, input.ID)
			require.NoError(t, err)
			if state == ProviderSteeringAcceptedConfirmed || state == "legacy_callback" {
				require.Equal(t, models.ThreadInputApplied, saved.InputStatus)
			} else {
				require.Equal(t, models.ThreadInputPending, saved.InputStatus)
			}
			require.ErrorIs(t, NewRunIdentityRepo(db).CheckExecutionGoal(ctx, execution.ID, task.ID), persistence.ErrProjectDenied)
			// Neither queue cleanup nor ambiguous-delivery cleanup can erase the evidence.
			require.NoError(t, inputs.ClearProviderSteering(ctx, []string{"steer"}))
			_, err = db.Exec(`DELETE FROM task_pull_requests WHERE id=?`, pr.ID)
			require.NoError(t, err)
			_, err = db.Exec(`DELETE FROM thread_inputs WHERE id=?`, input.ID)
			require.NoError(t, err)
			require.ErrorIs(t, NewRunIdentityRepo(db).CheckExecutionGoal(ctx, execution.ID, task.ID), persistence.ErrProjectDenied)
			require.ErrorIs(t, executions.MarkRunning(ctx, execution.ID), persistence.ErrProjectDenied)
		})
	}
}

package sqlite

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestGitHubFeedbackQueuedSenderSurvivesSourceDeletion(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, NewGitHubAuthRepo(db).UpsertAuthorizedActor(t.Context(), &models.GitHubAuthorizedActor{GitHubLogin: "reviewer", Permission: "triage", AddedBy: "test"}))
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "feedback-sender"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "workspace", models.BootstrapUserID))
	identity := models.SlackIngressIdentity{ProjectID: "default", TeamID: "workspace", ExternalUserID: entry.SlackUserID, UserID: models.BootstrapUserID}
	sender := models.WithSlackIngressIdentity(owner, identity)
	sourceTask := createThreadInputTask(t, owner, db, "default")
	target := createRuntimeScheduledTask(t, owner, NewTaskRepo(db, nil), "default", "Feedback target")
	agent := createThreadInputLLMConfig(t, owner, db)
	require.NoError(t, NewTaskRepo(db, nil).UpdateStatus(owner, target.ID, models.StatusCompleted))
	source := models.Execution{TaskID: sourceTask.ID, Status: models.ExecRunning, PromptSent: "forward"}
	require.NoError(t, NewExecutionRepo(db).Create(sender, &source))
	pr := &models.TaskPullRequest{TaskID: target.ID, PRNumber: 42, PRURL: "https://github.com/example/repo/pull/42", PRState: "open"}
	require.NoError(t, NewTaskPullRequestRepo(db).Upsert(owner, pr))
	bindTestGitHubReviewer(t, db, "default", "reviewer", models.BootstrapUserID)
	feedback := models.GitHubPRFeedbackForwarded{TaskPullRequestID: pr.ID, TaskID: target.ID, RepoFullName: "example/repo", PRNumber: 42, FeedbackKind: "issue_comment", GitHubID: "provenance", AuthorLogin: "reviewer", AuthorUserID: 123, ProviderHost: "github.com", Body: "revise", CreatedAt: time.Now()}
	input := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: target.ID, AgentConfigID: agent.ID, Content: "revise"}
	admission := models.WithGitHubFeedbackExecution(owner, models.GitHubFeedbackExecution{TaskID: sourceTask.ID, ExecutionID: source.ID, RequireIdentity: true})
	forged := identity
	forged.ExternalUserID = "different-sender"
	recorded, err := NewGitHubPRFeedbackRepo(db).RecordForwardedAndQueue(models.WithSlackIngressIdentity(admission, forged), &feedback, &input)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	require.False(t, recorded)
	_, err = db.Exec(`CREATE TRIGGER reject_feedback_sender_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='slack.input_admitted' BEGIN SELECT RAISE(ABORT,'sender audit unavailable'); END`)
	require.NoError(t, err)
	recorded, err = NewGitHubPRFeedbackRepo(db).RecordForwardedAndQueue(admission, &feedback, &input)
	require.ErrorContains(t, err, "sender audit unavailable")
	require.False(t, recorded)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM slack_input_provenance`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM github_pr_feedback_forwarded`).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`DROP TRIGGER reject_feedback_sender_audit`)
	require.NoError(t, err)
	feedback.ID, input.ID = "", ""
	recorded, err = NewGitHubPRFeedbackRepo(db).RecordForwardedAndQueue(admission, &feedback, &input)
	require.NoError(t, err)
	require.True(t, recorded)
	var savedSender string
	require.NoError(t, db.QueryRow(`SELECT external_user_id FROM slack_input_provenance WHERE input_id=?`, input.ID).Scan(&savedSender))
	require.Equal(t, entry.SlackUserID, savedSender)
	// Source execution cleanup must not erase the independent queue snapshot.
	_, err = db.Exec(`DELETE FROM executions WHERE id=?`, source.ID)
	require.NoError(t, err)
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	inputs := NewThreadInputRepo(db)
	execution := models.Execution{TaskID: target.ID, AgentConfigID: agent.ID, IsFollowup: true}
	require.Error(t, inputs.ClaimQueuedForTaskExecution(ctx, input.ID, &execution))
	queued, err := inputs.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputPending, queued.InputStatus)
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "workspace", models.BootstrapUserID))
	require.NoError(t, inputs.ClaimQueuedForTaskExecution(ctx, input.ID, &execution))
	require.NoError(t, NewRunIdentityRepo(db).CheckExecutionGoal(ctx, execution.ID, target.ID))
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	require.Error(t, NewRunIdentityRepo(db).CheckExecutionGoal(ctx, execution.ID, target.ID))
	require.Error(t, NewExecutionRepo(db).MarkRunning(ctx, execution.ID))
}

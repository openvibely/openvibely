package sqlite

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestGitHubFeedbackAuditFailureRollsBackQueue(t *testing.T) {
	db := testutil.NewTestDB(t)
	require.NoError(t, NewGitHubAuthRepo(db).UpsertAuthorizedActor(t.Context(), &models.GitHubAuthorizedActor{GitHubLogin: "alice", Permission: "triage", AddedBy: "test"}))
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	task := createRuntimeScheduledTask(t, ctx, NewTaskRepo(db, nil), "default", "Feedback audit")
	pr := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/example/repo/pull/42", PRState: "open"}
	require.NoError(t, NewTaskPullRequestRepo(db).Upsert(ctx, pr))
	repo := NewGitHubPRFeedbackRepo(db)
	bindTestGitHubReviewer(t, db, "default", "alice", models.BootstrapUserID)
	feedback := models.GitHubPRFeedbackForwarded{TaskPullRequestID: pr.ID, TaskID: task.ID, RepoFullName: "example/repo", PRNumber: 42, FeedbackKind: "issue_comment", GitHubID: "audit-test", AuthorLogin: "alice", AuthorUserID: 123, ProviderHost: "github.com", Body: "feedback", CreatedAt: time.Now()}
	input := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, Content: "feedback"}
	execution := models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "forward feedback"}
	require.NoError(t, NewExecutionRepo(db).Create(ctx, &execution))
	legacy := models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "legacy"}
	require.NoError(t, NewExecutionRepo(db).Create(t.Context(), &legacy))
	for _, source := range []models.GitHubFeedbackExecution{
		{TaskID: "other-task", ExecutionID: execution.ID, RequireIdentity: true},
		{TaskID: task.ID, ExecutionID: "missing", RequireIdentity: true},
		{TaskID: task.ID, ExecutionID: legacy.ID, RequireIdentity: true},
	} {
		recorded, err := repo.RecordForwardedAndQueue(models.WithGitHubFeedbackExecution(ctx, source), &feedback, &input)
		require.ErrorIs(t, err, persistence.ErrProjectDenied)
		require.False(t, recorded)
	}
	ctx = models.WithGitHubFeedbackExecution(ctx, models.GitHubFeedbackExecution{TaskID: task.ID, ExecutionID: execution.ID, RequireIdentity: true})
	_, err := db.Exec(`CREATE TRIGGER reject_feedback_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='github.feedback_queued' BEGIN SELECT RAISE(ABORT,'feedback audit unavailable'); END`)
	require.NoError(t, err)
	recorded, err := repo.RecordForwardedAndQueue(ctx, &feedback, &input)
	require.ErrorContains(t, err, "feedback audit unavailable")
	require.False(t, recorded)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM github_pr_feedback_forwarded WHERE github_id='audit-test'`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM github_reviewer_input_identities`).Scan(&count))
	require.Zero(t, count)
	pending, err := NewThreadInputRepo(db).ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, pending)
	_, err = db.Exec(`DROP TRIGGER reject_feedback_audit`)
	require.NoError(t, err)
	// Failed writes may have assigned IDs to the in-memory objects. A fresh
	// retry represents a newly fetched provider record.
	feedback.ID = ""
	feedback.QueuedThreadInputID = ""
	input.ID = ""
	recorded, err = repo.RecordForwardedAndQueue(ctx, &feedback, &input)
	require.NoError(t, err)
	require.True(t, recorded)
	recorded, err = repo.RecordForwardedAndQueue(ctx, &feedback, &input)
	require.NoError(t, err)
	require.False(t, recorded)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE action='github.feedback_queued'`).Scan(&count))
	require.Equal(t, 1, count)
	legacyContext := models.WithGitHubFeedbackExecution(ctx, models.GitHubFeedbackExecution{TaskID: task.ID, ExecutionID: legacy.ID})
	recorded, err = repo.RecordForwardedAndQueue(legacyContext, &feedback, &input)
	require.NoError(t, err, "auth-off legacy snapshot absence remains compatible")
	require.False(t, recorded)

	_, err = db.Exec(`DELETE FROM github_authorized_actors WHERE github_login='alice'`)
	require.NoError(t, err)
	recorded, err = repo.RecordForwardedAndQueue(ctx, &feedback, &input)
	require.ErrorIs(t, err, persistence.ErrProjectDenied, "duplicate handoff cannot reuse removed reviewer authority")
	require.False(t, recorded)

}

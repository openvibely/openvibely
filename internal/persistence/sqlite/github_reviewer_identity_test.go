package sqlite

import (
	"database/sql"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func bindTestGitHubReviewer(t *testing.T, db *sql.DB, project, login, user string) models.GitHubReviewerIdentity {
	t.Helper()
	repo := NewGitHubAuthRepo(db)
	owner := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	actors, err := repo.ListAuthorizedActors(owner)
	require.NoError(t, err)
	for _, a := range actors {
		if a.GitHubLogin == login {
			b := models.GitHubReviewerIdentity{ProjectID: project, ActorID: a.ID, Host: "github.com", ExternalUserID: 123, ApplicationUserID: user}
			require.NoError(t, repo.BindReviewerIdentity(owner, b))

			return b
		}
	}
	t.Fatal("missing reviewer")
	return models.GitHubReviewerIdentity{}
}

func TestGitHubReviewerIdentityAttestation(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	repo := NewGitHubAuthRepo(db)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('reviewer','reviewer'); INSERT INTO org_memberships VALUES('default','reviewer','member')`)
	require.NoError(t, err)
	actor := &models.GitHubAuthorizedActor{GitHubLogin: "reviewer"}
	require.NoError(t, repo.UpsertAuthorizedActor(owner, actor))
	b := models.GitHubReviewerIdentity{ProjectID: "default", ActorID: actor.ID, Host: "github.com", ExternalUserID: 123, ApplicationUserID: "reviewer"}
	require.ErrorIs(t, repo.BindReviewerIdentity(ctx, b), persistence.ErrProjectDenied)
	require.ErrorIs(t, repo.BindReviewerIdentity(models.WithPrincipal(ctx, models.Principal{UserID: "reviewer", Role: "owner"}), b), persistence.ErrOrgDenied)
	for _, host := range []string{"", "github.com/path", "user@github.com", "github.com?x=y", "https://github.com"} {
		bad := b
		bad.Host = host
		require.ErrorIs(t, repo.BindReviewerIdentity(owner, bad), persistence.ErrProjectDenied)
	}
	require.NoError(t, repo.BindReviewerIdentity(owner, b))
	var details string
	require.NoError(t, db.QueryRow(`SELECT details FROM project_audit_events WHERE action='github.reviewer_bound' AND target_id=?`, actor.ID).Scan(&details))
	require.JSONEq(t, `{"host":"github.com","external_user_id":123,"application_user_id":"reviewer"}`, details)
	other := &models.GitHubAuthorizedActor{GitHubLogin: "another-reviewer"}
	require.NoError(t, repo.UpsertAuthorizedActor(owner, other))
	duplicate := b
	duplicate.ActorID = other.ID
	require.Error(t, repo.BindReviewerIdentity(owner, duplicate), "same external identity cannot be assigned to two trust entries")
	invalid := b
	invalid.ExternalUserID = 0
	require.ErrorIs(t, repo.BindReviewerIdentity(owner, invalid), persistence.ErrProjectDenied)
	for _, query := range []struct {
		project, host, login string
		id                   int64
	}{{"default", "other.example", "reviewer", 123}, {"default", "github.com", "other", 123}, {"foreign", "github.com", "reviewer", 123}, {"default", "github.com", "reviewer", 124}} {
		_, err := repo.ResolveReviewerIdentity(ctx, query.project, query.host, query.id, query.login)
		require.ErrorIs(t, err, persistence.ErrProjectDenied)
	}
	resolved, err := repo.ResolveReviewerIdentity(ctx, "default", "GitHub.COM", 123, "@Reviewer")
	require.NoError(t, err)
	require.Equal(t, "reviewer", resolved)
	_, err = db.Exec(`CREATE TRIGGER reject_binding_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='github.reviewer_bound' BEGIN SELECT RAISE(ABORT,'audit failed'); END`)
	require.NoError(t, err)
	b.ApplicationUserID = models.BootstrapUserID
	require.ErrorContains(t, repo.BindReviewerIdentity(owner, b), "audit failed")
	resolved, err = repo.ResolveReviewerIdentity(ctx, "default", "github.com", 123, "reviewer")
	require.NoError(t, err)
	require.Equal(t, "reviewer", resolved)
	_, err = db.Exec(`UPDATE users SET disabled=1 WHERE id='reviewer'`)
	require.NoError(t, err)
	_, err = repo.ResolveReviewerIdentity(ctx, "default", "github.com", 123, "reviewer")
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
}

func TestGitHubMappedReviewerSurvivesCleanupAndRejectsRebinding(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('reviewer','reviewer'); INSERT INTO org_memberships VALUES('default','reviewer','member')`)
	require.NoError(t, err)
	auth := NewGitHubAuthRepo(db)
	a := &models.GitHubAuthorizedActor{GitHubLogin: "reviewer"}
	require.NoError(t, auth.UpsertAuthorizedActor(owner, a))
	task := createThreadInputTask(t, owner, db, "default")
	agent := createThreadInputLLMConfig(t, owner, db)
	source := &models.Execution{TaskID: task.ID, Status: models.ExecCompleted}
	require.NoError(t, NewExecutionRepo(db).Create(owner, source))
	admission := models.WithGitHubFeedbackExecution(owner, models.GitHubFeedbackExecution{TaskID: task.ID, ExecutionID: source.ID, RequireIdentity: true})
	pr := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/example/repo/pull/42"}
	require.NoError(t, NewTaskPullRequestRepo(db).Upsert(ctx, pr))
	f := &models.GitHubPRFeedbackForwarded{TaskPullRequestID: pr.ID, TaskID: task.ID, RepoFullName: "example/repo", PRNumber: 42, FeedbackKind: "review", GitHubID: "review", AuthorLogin: "reviewer", AuthorUserID: 123, ProviderHost: "github.com", CreatedAt: time.Now()}
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, Content: "revise", AgentConfigID: agent.ID}
	repo := NewGitHubPRFeedbackRepo(db)
	_, err = repo.RecordForwardedAndQueue(admission, f, input)
	require.ErrorIs(t, err, persistence.ErrProjectDenied, "unbound reviewer cannot borrow owner permissions")
	b := bindTestGitHubReviewer(t, db, "default", "reviewer", "reviewer")
	_, err = db.Exec(`UPDATE tasks SET auto_merge=1 WHERE id=?`, task.ID)
	require.NoError(t, err)
	_, err = repo.RecordForwardedAndQueue(admission, f, input)
	require.ErrorIs(t, err, persistence.ErrProjectDenied, "an editor reviewer cannot instruct a maintainer-only task through the owner forwarder")
	_, err = db.Exec(`UPDATE tasks SET auto_merge=0 WHERE id=?`, task.ID)
	require.NoError(t, err)

	f.AuthorUserID = 124
	_, err = repo.RecordForwardedAndQueue(admission, f, input)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	f.AuthorUserID = 123
	recorded, err := repo.RecordForwardedAndQueue(admission, f, input)
	require.NoError(t, err)
	require.True(t, recorded)
	require.NoError(t, NewTaskRepo(db, nil).UpdateStatus(owner, task.ID, models.StatusCompleted))
	b.ApplicationUserID = models.BootstrapUserID
	require.NoError(t, auth.BindReviewerIdentity(owner, b))
	execution := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, IsFollowup: true}
	inputs := NewThreadInputRepo(db)
	require.ErrorIs(t, inputs.ClaimQueuedForTaskExecution(ctx, input.ID, execution), persistence.ErrProjectDenied)
	b.ApplicationUserID = "reviewer"
	require.NoError(t, auth.BindReviewerIdentity(owner, b))
	_, err = db.Exec(`CREATE TRIGGER reject_identity_snapshot BEFORE INSERT ON github_reviewer_execution_identities BEGIN SELECT RAISE(ABORT,'identity snapshot failed'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, inputs.ClaimQueuedForTaskExecution(ctx, input.ID, execution), "identity snapshot failed")
	pending, err := inputs.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputPending, pending.InputStatus)
	_, err = db.Exec(`DROP TRIGGER reject_identity_snapshot`)
	require.NoError(t, err)
	execution.ID = ""
	require.NoError(t, inputs.ClaimQueuedForTaskExecution(ctx, input.ID, execution))
	_, err = db.Exec(`DELETE FROM task_pull_requests WHERE id=?`, pr.ID)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM thread_inputs WHERE id=?`, input.ID)
	require.NoError(t, err)
	require.NoError(t, NewRunIdentityRepo(db).CheckExecutionGoal(ctx, execution.ID, task.ID))
	_, err = db.Exec(`DELETE FROM org_memberships WHERE user_id='reviewer'`)
	require.NoError(t, err)
	require.ErrorIs(t, NewRunIdentityRepo(db).CheckExecutionGoal(owner, execution.ID, task.ID), persistence.ErrProjectDenied)
	require.ErrorIs(t, NewExecutionRepo(db).MarkRunning(owner, execution.ID), persistence.ErrProjectDenied)
}

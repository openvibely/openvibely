package service

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/stretchr/testify/require"
)

func TestGitHubFeedbackHandoffScopeAndCurrentRole(t *testing.T) {
	f := newGitHubPRFeedbackForwarderFixture(t, []string{"alice"}, nil)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	pr, err := f.env.TaskPullRequests.GetByTaskID(ctx, f.task.ID)
	require.NoError(t, err)
	record := models.GitHubPRFeedbackForwarded{TaskPullRequestID: pr.ID, TaskID: f.task.ID, RepoFullName: "openvibely/openvibely", PRNumber: 42, FeedbackKind: "issue_comment", GitHubID: "scope-test", AuthorLogin: "alice", Body: "feedback", CreatedAt: time.Now()}
	input := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: f.project.ID, TaskID: f.task.ID, Content: "feedback"}
	for _, change := range []func(*models.GitHubPRFeedbackForwarded, *models.ThreadInput){
		func(_ *models.GitHubPRFeedbackForwarded, i *models.ThreadInput) { i.ProjectID = "default" },
		func(r *models.GitHubPRFeedbackForwarded, _ *models.ThreadInput) { r.TaskID = "other-task" },
		func(r *models.GitHubPRFeedbackForwarded, _ *models.ThreadInput) { r.PRNumber = 43 },
	} {
		r, i := record, input
		change(&r, &i)
		ok, err := f.env.GitHubPRFeedback.RecordForwardedAndQueue(owner, &r, &i)
		require.ErrorIs(t, err, persistence.ErrProjectDenied)
		require.False(t, ok)
	}
	org := NewOrganizationService(f.env.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "feedback-caller", "long-password-value", "")
	require.NoError(t, err)
	caller := models.WithPrincipal(ctx, models.Principal{UserID: user.UserID, Role: "owner"})
	ok, err := f.env.GitHubPRFeedback.RecordForwardedAndQueue(caller, &record, &input)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	require.False(t, ok)
	require.NoError(t, f.env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, "maintainer"))
	ok, err = f.env.GitHubPRFeedback.RecordForwardedAndQueue(caller, &record, &input)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, f.env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, "viewer"))
	ok, err = f.env.GitHubPRFeedback.RecordForwardedAndQueue(caller, &record, &input)
	require.ErrorIs(t, err, persistence.ErrProjectDenied, "deduplication cannot bypass current access")
	require.False(t, ok)
	pending, err := f.env.ThreadInputs.ListPendingForTask(ctx, f.task.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
}

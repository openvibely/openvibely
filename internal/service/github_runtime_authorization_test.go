package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestGitHubRuntimeCurrentProjectAuthorization(t *testing.T) {
	f := newAutomationRuntimeStoreFixture(t, AutomationAdapterGitHubSDLC)
	ctx := t.Context()
	worker := NewWorkerService(nil, 1, f.stores.Projects)
	worker.SetRunAuthorization(f.stores.RunIdentities, f.stores.Organizations, f.stores.ProjectAccess, false)
	org := NewOrganizationService(f.stores.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "github-runtime-user", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, f.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, "viewer"))
	calls := 0
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			calls++
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
		},
		getIssueFn: func(context.Context, *GitHubRepoRef, int) (*GitHubIssue, error) {
			calls++
			return &GitHubIssue{Number: 1}, nil
		},
		commentIssueFn: func(context.Context, *GitHubRepoRef, int, string) error { calls++; return nil },
	}
	f.project.RepoURL = "https://github.com/example/runtime"
	require.NoError(t, f.stores.Projects.Update(ctx, &f.project))
	opts := githubIssueRuntimeOptions{WorkerSvc: worker, ProjectID: f.project.ID, ProjectRepo: f.stores.Projects, GitHub: provider}
	handlers := buildGitHubIssueRuntimeHandlers(opts)
	for name, handler := range handlers {
		_, err := handler(ctx, json.RawMessage(`{}`))
		require.ErrorIs(t, err, authz.ErrDenied, name)
	}
	require.Zero(t, calls)
	caller := models.WithPrincipal(ctx, models.Principal{UserID: user.UserID, Role: "owner"})
	read := json.RawMessage(`{"issue_number":1}`)
	write := json.RawMessage(`{"issue_number":1,"body":"review"}`)
	_, err = handlers["github_get_issue"](caller, read)
	require.NoError(t, err, "viewer can read")
	for _, role := range []string{"viewer", "editor", "maintainer"} {
		require.NoError(t, f.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, role))
		before := calls
		_, err = handlers["github_comment_on_issue"](caller, write)
		if role == "maintainer" {
			require.NoError(t, err)
			require.Greater(t, calls, before)
		} else {
			require.ErrorIs(t, err, authz.ErrDenied)
			require.Equal(t, before, calls)
		}
	}
	require.NoError(t, f.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, "editor"))
	before := calls
	_, err = handlers["github_close_issue"](caller, json.RawMessage(`{}`))
	require.ErrorIs(t, err, authz.ErrDenied, "runtime created before downgrade must recheck")
	require.Equal(t, before, calls)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	_, err = handlers["github_comment_on_issue"](owner, write)
	require.NoError(t, err)
	opts.ProjectID = "missing-project"
	_, err = buildGitHubIssueRuntimeHandlers(opts)["github_get_issue"](caller, read)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.NoError(t, f.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "", true))
	_, err = handlers["github_get_issue"](caller, read)
	require.ErrorIs(t, err, authz.ErrDenied)
	worker.runAuthOff = true
	_, err = handlers["github_comment_on_issue"](ctx, write)
	require.NoError(t, err, "auth-off resolves the default owner")
}

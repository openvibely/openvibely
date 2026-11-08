package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestGitHubDiscoveryRechecksAfterProviderRead(t *testing.T) {
	f := newAutomationRuntimeStoreFixture(t, AutomationAdapterGitHubSDLC)
	ctx := t.Context()
	worker := NewWorkerService(nil, 1, f.stores.Projects)
	worker.SetRunAuthorization(f.stores.RunIdentities, f.stores.Organizations, f.stores.ProjectAccess, false)
	org := NewOrganizationService(f.stores.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "discovery-reader", "long-password-value", "")
	require.NoError(t, err)
	caller := models.WithPrincipal(ctx, user.Principal)
	f.project.RepoURL = "https://github.com/example/runtime"
	require.NoError(t, f.stores.Projects.Update(ctx, &f.project))
	calls := 0
	revoke := false
	provider := &fakeGitHubIssueRuntimeProvider{
		listMyIssuesFn: func(context.Context, *GitHubRepoRef) (*GitHubAuthenticatedUser, []GitHubIssue, error) {
			calls++
			if revoke {
				require.NoError(t, f.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, "editor"))
			}
			return &GitHubAuthenticatedUser{Login: "account"}, []GitHubIssue{{Number: 1, Title: "assigned"}}, nil
		},
	}
	handlers := buildGitHubIssueRuntimeHandlers(githubIssueRuntimeOptions{WorkerSvc: worker, ProjectID: f.project.ID, ProjectRepo: f.stores.Projects, GitHub: provider, AutomationRepo: f.repo})
	for _, lateRevocation := range []bool{false, true} {
		require.NoError(t, f.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, "maintainer"))
		revoke = lateRevocation
		result, err := handlers["github_list_my_assigned_issues"](caller, json.RawMessage(`{}`))
		if lateRevocation {
			require.ErrorIs(t, err, authz.ErrDenied)
			require.Empty(t, result)
		} else {
			require.NoError(t, err)
			require.Contains(t, result, "assigned")
		}
	}
	require.Equal(t, 2, calls, "both requests must reach the provider read before the late check")
}

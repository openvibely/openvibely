package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestGitHubRuntimeMutationRechecksAfterRepositoryResolution(t *testing.T) {
	f := newAutomationRuntimeStoreFixture(t, AutomationAdapterGitHubSDLC)
	ctx := t.Context()
	worker := NewWorkerService(nil, 1, f.stores.Projects)
	worker.SetRunAuthorization(f.stores.RunIdentities, f.stores.Organizations, f.stores.ProjectAccess, false)
	org := NewOrganizationService(f.stores.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "provider-boundary-user", "long-password-value", "")
	require.NoError(t, err)
	caller := models.WithPrincipal(ctx, user.Principal)
	f.project.RepoURL = "https://github.com/example/runtime"
	require.NoError(t, f.stores.Projects.Update(ctx, &f.project))
	writes, resolutions := 0, 0
	revoke := false
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			resolutions++
			if revoke {
				require.NoError(t, f.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, "editor"))
			}
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: f.project.RepoURL}, nil
		},
		commentIssueFn: func(context.Context, *GitHubRepoRef, int, string) error { writes++; return nil },
		addLabelsFn:    func(context.Context, *GitHubRepoRef, int, []string) error { writes++; return nil },
		closeIssueFn:   func(context.Context, *GitHubRepoRef, int) error { writes++; return nil },
	}
	handlers := buildGitHubIssueRuntimeHandlers(githubIssueRuntimeOptions{WorkerSvc: worker, ProjectID: f.project.ID, ProjectRepo: f.stores.Projects, GitHub: provider})
	for _, name := range []string{"github_comment_on_issue", "github_add_issue_labels", "github_close_issue"} {
		t.Run(name, func(t *testing.T) {
			for _, lateRevocation := range []bool{false, true} {
				require.NoError(t, f.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, "maintainer"))
				revoke = lateRevocation
				beforeWrites, beforeReads := writes, resolutions
				_, err := handlers[name](caller, json.RawMessage(`{"issue_number":1,"body":"review","labels":["reviewed"]}`))
				require.Equal(t, beforeReads+1, resolutions, "must reach the provider read")
				if lateRevocation {
					require.ErrorIs(t, err, authz.ErrDenied)
					require.Equal(t, beforeWrites, writes)
				} else {
					require.NoError(t, err)
					require.Equal(t, beforeWrites+1, writes)
				}
			}
		})
	}
}

func TestGitHubRuntimeProviderRechecksSavedBindingAfterRead(t *testing.T) {
	f := newAutomationRuntimeStoreFixture(t, AutomationAdapterGitHubSDLC)
	owner := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	worker := NewWorkerService(nil, 1, f.stores.Projects)
	worker.SetRunAuthorization(f.stores.RunIdentities, f.stores.Organizations, f.stores.ProjectAccess, false)
	entry := &models.SlackAuthorizedUser{ProjectID: f.project.ID, SlackUserID: "provider-boundary-sender"}
	require.NoError(t, f.stores.SlackAuth.Create(owner, entry))
	require.NoError(t, f.stores.SlackAuth.BindApplicationUser(owner, f.project.ID, entry.ID, "workspace", models.BootstrapUserID))
	sender := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{ProjectID: f.project.ID, TeamID: "workspace", ExternalUserID: entry.SlackUserID, UserID: models.BootstrapUserID})
	execution := models.Execution{TaskID: f.task.ID, Status: models.ExecRunning, PromptSent: "provider test"}
	require.NoError(t, f.stores.Executions.Create(sender, &execution))
	f.project.RepoURL = "https://github.com/example/runtime"
	require.NoError(t, f.stores.Projects.Update(owner, &f.project))
	reads, writes := 0, 0
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			reads++
			require.NoError(t, f.stores.SlackAuth.DeleteForProject(owner, f.project.ID, entry.ID))
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: f.project.RepoURL}, nil
		},
		commentIssueFn: func(context.Context, *GitHubRepoRef, int, string) error { writes++; return nil },
	}
	opts := githubIssueRuntimeOptions{WorkerSvc: worker, ProjectID: f.project.ID, ProjectRepo: f.stores.Projects, TaskRepo: f.taskRepo, TaskID: f.task.ID, ExecutionID: execution.ID, GitHub: provider}
	_, err := buildGitHubIssueRuntimeHandlers(opts)["github_comment_on_issue"](owner, json.RawMessage(`{"issue_number":1,"body":"review"}`))
	require.ErrorIs(t, err, authz.ErrDenied)
	require.Equal(t, 1, reads)
	require.Zero(t, writes, "saved provenance must be checked after the read even without ingress context")
}

type githubBoundaryBodyProvider struct {
	GitHubIssueRuntimeProvider
	update func(context.Context, *GitHubRepoRef, int, string) error
}

func (p githubBoundaryBodyProvider) UpdatePullRequestBody(ctx context.Context, repo *GitHubRepoRef, number int, body string) error {
	return p.update(ctx, repo, number, body)
}

func TestGitHubRuntimeProviderOptionalMutations(t *testing.T) {
	f := newAutomationRuntimeStoreFixture(t, AutomationAdapterGitHubSDLC)
	ctx := t.Context()
	worker := NewWorkerService(nil, 1, f.stores.Projects)
	worker.SetRunAuthorization(f.stores.RunIdentities, f.stores.Organizations, f.stores.ProjectAccess, false)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	calls := 0
	fake := &fakeGitHubIssueRuntimeProvider{replaceBranchHeadFn: func(context.Context, *GitHubRepoRef, GitHubReplaceBranchHeadRequest) (string, error) {
		calls++
		return "head", nil
	}}
	body := githubBoundaryBodyProvider{GitHubIssueRuntimeProvider: fake, update: func(context.Context, *GitHubRepoRef, int, string) error { calls++; return nil }}
	both := struct {
		GitHubIssueRuntimeProvider
		taskPullRequestBodyUpdater
		githubRuntimeBranchReplacement
	}{fake, body, fake}
	for _, tc := range []struct {
		name         string
		provider     GitHubIssueRuntimeProvider
		body, branch bool
	}{
		{"neither", struct{ GitHubIssueRuntimeProvider }{fake}, false, false},
		{"body", body, true, false},
		{"branch", fake, false, true},
		{"both", both, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guarded := guardGitHubRuntimeProvider(githubIssueRuntimeOptions{GitHub: tc.provider, WorkerSvc: worker, ProjectID: f.project.ID})
			updater, hasBody := guarded.(taskPullRequestBodyUpdater)
			replacer, hasBranch := guarded.(taskPullRequestBranchReplacer)
			require.Equal(t, tc.body, hasBody)
			require.Equal(t, tc.branch, hasBranch)
			before := calls
			if hasBody {
				require.ErrorIs(t, updater.UpdatePullRequestBody(ctx, nil, 1, "body"), authz.ErrDenied)
				require.Equal(t, before, calls)
				require.NoError(t, updater.UpdatePullRequestBody(owner, nil, 1, "body"))
				before++
			}
			if hasBranch {
				_, err := replacer.ReplaceBranchHead(ctx, nil, GitHubReplaceBranchHeadRequest{})
				require.ErrorIs(t, err, authz.ErrDenied)
				require.Equal(t, before, calls)
				_, err = replacer.ReplaceBranchHead(owner, nil, GitHubReplaceBranchHeadRequest{})
				require.NoError(t, err)
				before++
			}
			require.Equal(t, before, calls)
		})
	}
}

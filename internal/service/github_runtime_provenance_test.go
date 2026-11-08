package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestGitHubRuntimeSavedExecutionBindingRevocation(t *testing.T) {
	f := newAutomationRuntimeStoreFixture(t, AutomationAdapterGitHubSDLC)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	worker := NewWorkerService(nil, 1, f.stores.Projects)
	worker.SetRunAuthorization(f.stores.RunIdentities, f.stores.Organizations, f.stores.ProjectAccess, false)
	entry := &models.SlackAuthorizedUser{ProjectID: f.project.ID, SlackUserID: "github-executor"}
	require.NoError(t, f.stores.SlackAuth.Create(owner, entry))
	require.NoError(t, f.stores.SlackAuth.BindApplicationUser(owner, f.project.ID, entry.ID, "workspace", models.BootstrapUserID))
	sender := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{ProjectID: f.project.ID, TeamID: "workspace", ExternalUserID: entry.SlackUserID, UserID: models.BootstrapUserID})
	execution := models.Execution{TaskID: f.task.ID, Status: models.ExecRunning, PromptSent: "GitHub action"}
	require.NoError(t, f.stores.Executions.Create(sender, &execution))
	f.project.RepoURL = "https://github.com/example/runtime"
	require.NoError(t, f.stores.Projects.Update(owner, &f.project))
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
	}
	opts := githubIssueRuntimeOptions{WorkerSvc: worker, ProjectID: f.project.ID, TaskID: f.task.ID, ExecutionID: execution.ID, TaskRepo: f.taskRepo, ProjectRepo: f.stores.Projects, GitHub: provider}
	handlers := buildGitHubIssueRuntimeHandlers(opts)
	input := json.RawMessage(`{"issue_number":1}`)
	_, err := handlers["github_get_issue"](owner, input)
	require.NoError(t, err, "saved provenance must work without original request metadata")
	before := calls
	_, err = handlers["github_get_issue"](withAutomationExecution(owner, f.task.ID, "substituted"), input)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.Equal(t, before, calls)
	foreign := opts
	foreign.ProjectID = "default"
	_, err = buildGitHubIssueRuntimeHandlers(foreign)["github_get_issue"](owner, input)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.Equal(t, before, calls)
	org := NewOrganizationService(f.stores.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "admin")
	require.NoError(t, err)
	other, err := org.Accept(ctx, invite, "substitute-admin", "long-password-value", "")
	require.NoError(t, err)
	_, err = handlers["github_get_issue"](models.WithPrincipal(ctx, other.Principal), input)
	require.ErrorIs(t, err, authz.ErrDenied, "another administrator cannot substitute for saved executor")
	require.Equal(t, before, calls)
	legacy := models.Execution{TaskID: f.task.ID, Status: models.ExecRunning, PromptSent: "Legacy execution"}
	require.NoError(t, f.stores.Executions.Create(ctx, &legacy))
	legacyOptions := opts
	legacyOptions.ExecutionID = legacy.ID
	legacyHandler := buildGitHubIssueRuntimeHandlers(legacyOptions)["github_get_issue"]
	_, err = legacyHandler(owner, input)
	require.ErrorIs(t, err, authz.ErrDenied, "authenticated legacy runs need an execution identity")
	require.Equal(t, before, calls)
	worker.runAuthOff = true
	_, err = legacyHandler(ctx, input)
	require.NoError(t, err, "auth-off legacy recovery remains supported")
	worker.runAuthOff = false
	before = calls
	require.NoError(t, f.stores.SlackAuth.DeleteForProject(owner, f.project.ID, entry.ID))
	_, err = handlers["github_get_issue"](owner, input)
	require.ErrorIs(t, err, authz.ErrDenied, "binding removal must stop the next tool before watcher cancellation")
	require.Equal(t, before, calls)
}

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/stretchr/testify/require"
)

func TestAutomationGitHubLabelsRecheckAuthorization(t *testing.T) {
	for _, boundary := range []string{"before-admission", "during-provisioning", "during-reuse-read"} {
		t.Run(boundary, func(t *testing.T) {
			f := newAutomationRuntimeStoreFixture(t, AutomationAdapterGitHubSDLC)
			ctx := t.Context()
			f.project.RepoURL = "https://github.com/example/runtime.git"
			require.NoError(t, f.stores.Projects.Update(ctx, &f.project))
			org := NewOrganizationService(f.stores.Organizations, "secret")
			invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
			require.NoError(t, err)
			user, err := org.Accept(ctx, invite, "label-author", "long-password-value", "")
			require.NoError(t, err)
			require.NoError(t, f.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, "maintainer"))
			revoke := func() {
				require.NoError(t, f.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, ""))
			}
			invocation, _, err := f.repo.ClaimScheduledOccurrence(ctx, f.schedule, time.Now(), f.schedule.ComputeNextRun(time.Now()))
			require.NoError(t, err)
			node := automationNodeByKey(t, f.definition, "bug_finder")
			execution := models.Execution{TaskID: f.task.ID, Status: models.ExecRunning, PromptSent: "labels"}
			require.NoError(t, f.stores.Executions.Create(ctx, &execution))
			caller := models.WithPrincipal(ctx, models.Principal{UserID: user.UserID, Role: "owner"})
			caller = WithAutomationContext(caller, models.AutomationContext{ProjectID: f.project.ID, Bindings: []models.AutomationBinding{{AutomationID: f.definition.Automation.ID, VersionID: f.definition.Version.ID, InvocationID: invocation.ID, NodeID: node.ID}}})
			caller = withAutomationExecution(caller, f.task.ID, execution.ID)
			ensureCalls, createCalls, addCalls := 0, 0, 0
			provider := &fakeGitHubIssueRuntimeProvider{
				resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
					return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
				},
				ensureIssueLabelsFn: func(context.Context, *GitHubRepoRef, []string) error {
					ensureCalls++
					if boundary == "during-provisioning" {
						revoke()
					}
					return nil
				},
				createIssueFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
					createCalls++
					return &GitHubIssue{Number: 42, Title: req.Title, Labels: req.Labels}, nil
				},
				getIssueFn: func(context.Context, *GitHubRepoRef, int) (*GitHubIssue, error) {
					revoke()
					return &GitHubIssue{Number: 42}, nil
				},
				addLabelsFn: func(context.Context, *GitHubRepoRef, int, []string) error { addCalls++; return nil },
			}
			handler := buildGitHubIssueRuntimeHandlers(githubIssueRuntimeOptions{ProjectID: f.project.ID, ProjectRepo: f.stores.Projects, TaskRepo: f.taskRepo, AutomationRepo: f.repo, GitHub: provider})["github_create_issue"]
			input := json.RawMessage(`{"title":"Permission boundary finding","body":"Summary"}`)
			if boundary == "before-admission" {
				revoke()
			}
			if boundary == "during-reuse-read" {
				_, err = handler(caller, input)
				require.NoError(t, err)
			}
			_, err = handler(caller, input)
			require.Error(t, err)
			if boundary != "during-reuse-read" {
				require.ErrorIs(t, err, persistence.ErrProjectDenied)
			}
			require.Zero(t, addCalls, "revocation during issue read must block label repair")
			if boundary == "before-admission" {
				require.Zero(t, ensureCalls, "unauthorized caller must not provision repository labels")
			}
			if boundary == "during-reuse-read" {
				require.Equal(t, 1, createCalls)
				require.Equal(t, 2, ensureCalls)
			} else {
				require.Zero(t, createCalls)
				if boundary == "during-provisioning" {
					require.Equal(t, 1, ensureCalls)
				}
			}
		})
	}
}

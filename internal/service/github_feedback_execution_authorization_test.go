package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestGitHubFeedbackRuntimeRechecksExecutionAfterFetch(t *testing.T) {
	for _, mode := range []string{"allowed", "fetch", "transaction", "reviewer removed", "reviewer rebound"} {
		revoke := mode != "allowed"
		name := mode
		t.Run(name, func(t *testing.T) {
			f := newAutomationRuntimeStoreFixture(t, AutomationAdapterGitHubSDLC)
			owner := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
			worker := NewWorkerService(nil, 1, f.stores.Projects)
			worker.SetRunAuthorization(f.stores.RunIdentities, f.stores.Organizations, f.stores.ProjectAccess, false)
			entry := &models.SlackAuthorizedUser{ProjectID: f.project.ID, SlackUserID: "feedback-executor"}
			require.NoError(t, f.stores.SlackAuth.Create(owner, entry))
			require.NoError(t, f.stores.SlackAuth.BindApplicationUser(owner, f.project.ID, entry.ID, "workspace", models.BootstrapUserID))
			sender := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{ProjectID: f.project.ID, TeamID: "workspace", ExternalUserID: entry.SlackUserID, UserID: models.BootstrapUserID})
			execution := models.Execution{TaskID: f.task.ID, Status: models.ExecRunning, PromptSent: "forward feedback"}
			require.NoError(t, f.stores.Executions.Create(sender, &execution))
			f.project.RepoURL = "https://github.com/openvibely/openvibely"
			require.NoError(t, f.stores.Projects.Update(owner, &f.project))
			require.NoError(t, f.stores.GitHubAuth.UpsertAuthorizedActor(owner, &models.GitHubAuthorizedActor{GitHubLogin: "reviewer", Permission: "triage", AddedBy: "test"}))
			actors, err := f.stores.GitHubAuth.ListAuthorizedActors(owner)
			require.NoError(t, err)
			for _, actor := range actors {
				if actor.GitHubLogin == "reviewer" {
					require.NoError(t, f.stores.GitHubAuth.BindReviewerIdentity(owner, models.GitHubReviewerIdentity{ProjectID: f.project.ID, ActorID: actor.ID, Host: "github.com", ExternalUserID: 123, ApplicationUserID: models.BootstrapUserID}))
				}
			}
			require.NoError(t, f.stores.TaskPullRequests.Upsert(owner, &models.TaskPullRequest{TaskID: f.task.ID, PRNumber: 42, PRURL: f.project.RepoURL + "/pull/42", PRState: "open"}))
			fetches, wakeups := 0, 0
			provider := &fakeGitHubIssueRuntimeProvider{listPRFeedbackFn: func(context.Context, *GitHubRepoRef, int) ([]GitHubPullRequestFeedback, error) {
				fetches++
				if mode == "fetch" {
					require.NoError(t, f.stores.SlackAuth.DeleteForProject(owner, f.project.ID, entry.ID))
				}
				return []GitHubPullRequestFeedback{{ID: "feedback", Kind: "issue_comment", AuthorLogin: "reviewer", AuthorUserID: 123, Body: "Please revise"}}, nil
			}}
			opts := githubIssueRuntimeOptions{WorkerSvc: worker, ProjectID: f.project.ID, TaskID: f.task.ID, ExecutionID: execution.ID, TaskRepo: f.taskRepo, ProjectRepo: f.stores.Projects, TaskPullRequestRepo: f.stores.TaskPullRequests, GitHubPRFeedbackRepo: f.stores.GitHubPRFeedback, GitHubAuthRepo: f.stores.GitHubAuth, GitHub: provider, AfterPRFeedbackForwarded: func(string) { wakeups++ }}
			if mode == "transaction" || mode == "reviewer removed" || mode == "reviewer rebound" {
				opts.GitHubPRFeedbackRepo = feedbackAdmissionHook{GitHubPRFeedbackStore: f.stores.GitHubPRFeedback, before: func(ctx context.Context) {
					source, found := models.GitHubFeedbackExecutionFromContext(ctx)
					require.True(t, found)
					require.Equal(t, execution.ID, source.ExecutionID)
					require.True(t, source.RequireIdentity)
					if mode == "reviewer removed" || mode == "reviewer rebound" {
						actors, err := f.stores.GitHubAuth.ListAuthorizedActors(owner)
						require.NoError(t, err)
						for _, actor := range actors {
							if actor.GitHubLogin == "reviewer" {
								if mode == "reviewer removed" {
									require.NoError(t, f.stores.GitHubAuth.DeleteAuthorizedActor(owner, actor.ID))
								} else {
									require.NoError(t, f.stores.GitHubAuth.BindReviewerIdentity(owner, models.GitHubReviewerIdentity{ProjectID: f.project.ID, ActorID: actor.ID, Host: "github.com", ExternalUserID: 456, ApplicationUserID: models.BootstrapUserID}))
								}
							}
						}
					} else {
						require.NoError(t, f.stores.SlackAuth.DeleteForProject(owner, f.project.ID, entry.ID))
					}
				}}
			}
			_, err = buildGitHubIssueRuntimeHandlers(opts)["github_forward_pr_feedback_to_tasks"](owner, json.RawMessage(`{}`))
			if revoke {
				require.ErrorIs(t, err, authz.ErrDenied)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, fetches)
			pending, err := f.stores.ThreadInputs.ListPendingForTask(owner, f.task.ID)
			require.NoError(t, err)
			recorded, err := f.stores.GitHubPRFeedback.AlreadyForwarded(owner, "openvibely/openvibely", 42, "issue_comment", "feedback")
			require.NoError(t, err)
			if revoke {
				require.Empty(t, pending)
				require.False(t, recorded)
				require.Zero(t, wakeups)
			} else {
				require.Len(t, pending, 1)
				require.True(t, recorded)
				require.Equal(t, 1, wakeups)
			}
		})
	}
}

// Simulate revocation after all service checks but before the repository starts
// its transaction, retaining the real persistence implementation.
type feedbackAdmissionHook struct {
	persistence.GitHubPRFeedbackStore
	before func(context.Context)
}

func (h feedbackAdmissionHook) RecordForwardedAndQueue(ctx context.Context, feedback *models.GitHubPRFeedbackForwarded, input *models.ThreadInput) (bool, error) {
	h.before(ctx)
	return h.GitHubPRFeedbackStore.RecordForwardedAndQueue(ctx, feedback, input)
}

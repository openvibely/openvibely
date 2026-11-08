package service

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"github.com/stretchr/testify/require"
)

type upstreamTrackingProvider struct {
	fakeAutomationPullRequestProvider
	requestedRepo string
}

func (p *upstreamTrackingProvider) GetPullRequest(ctx context.Context, repo *GitHubRepoRef, number int) (*GitHubPullRequest, error) {
	p.requestedRepo = repo.FullName
	return p.fakeAutomationPullRequestProvider.GetPullRequest(ctx, repo, number)
}

func TestAutomationReusedUpstreamPRTracksActualRepository(t *testing.T) {
	for _, useSavedURL := range []bool{false, true} {
		name := "returned URL"
		if useSavedURL {
			name = "saved URL fallback"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newAutomationRuntimeStoreFixture(t, AutomationAdapterGitHubSDLC)
			f.project.RepoURL = "https://github.com/example/runtime"
			require.NoError(t, f.stores.Projects.Update(ctx, &f.project))
			task := &models.Task{ProjectID: f.project.ID, Title: "Existing upstream PR", Category: models.CategoryCompleted, Status: models.StatusCompleted}
			require.NoError(t, f.taskRepo.Create(ctx, task))
			implementation := automationNodeByKey(t, f.definition, "implementation")
			binding := models.AutomationBinding{AutomationID: f.definition.Automation.ID, VersionID: f.definition.Version.ID, NodeID: implementation.ID}
			automationContext := models.AutomationContext{ProjectID: f.project.ID, Bindings: []models.AutomationBinding{binding}}
			_, _, err := f.repo.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
				Context: automationContext, Binding: binding, WorkItemKey: "github:example/runtime:issue:42", WorkItemKind: "github_issue", WorkItemTitle: "Issue 42", WorkItemStatus: models.AutomationWorkItemActive,
				ActivityKey: "implementation:" + task.ID, ActivityType: "implementation_task", ActivityStatus: models.AutomationActivityCompleted,
				Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: task.ID}},
				EventKey:  "implementation:" + task.ID, ToNodeID: implementation.ID, Transition: models.AutomationTransitionEntered,
			})
			require.NoError(t, err)
			record := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 7, PRURL: "https://github.com/upstream/original/pull/7", PRState: "open", HeadRepoURL: f.project.RepoURL, BaseBranch: "main"}
			require.NoError(t, f.stores.TaskPullRequests.Upsert(ctx, record))
			fork, err := ParseGitHubRepoURL(f.project.RepoURL)
			require.NoError(t, err)
			pr := &GitHubPullRequest{Number: 7, URL: record.PRURL, State: "open"}
			if useSavedURL {
				pr.URL = ""
			}
			opts := githubIssueRuntimeOptions{ProjectID: f.project.ID, AutomationRepo: f.repo}
			result := &OpenTaskPullRequestResult{PullRequest: pr, Record: record, ReusedExistingRecord: true}
			require.NoError(t, recordGitHubPullRequestOpened(ctx, opts, &fork, task, GitHubIssueActionRequest{IssueNumber: 42}, result))
			count := func(kind, id string) int {
				t.Helper()
				n, err := f.stores.Fixtures.CountAutomationResourceLinks(ctx, kind, id)
				require.NoError(t, err)
				return n
			}
			before := count("pull_request", "github:upstream/original:pull:7")
			require.Positive(t, before)
			require.Zero(t, count("pull_request", "github:example/runtime:pull:7"))
			require.Positive(t, count("github_issue", "github:example/runtime:issue:42"), "source issue must remain in the fork")
			require.Zero(t, count("github_issue", "github:upstream/original:issue:42"))
			require.NoError(t, recordGitHubPullRequestOpened(ctx, opts, &fork, task, GitHubIssueActionRequest{IssueNumber: 42}, result))
			require.Equal(t, before, count("pull_request", "github:upstream/original:pull:7"), "repeat publication should reuse tracking")
			waiting, err := f.stores.Fixtures.CountMatchingWorkItems(ctx, storefixture.WorkItemMatch{AutomationID: f.definition.Automation.ID, Status: "waiting"})
			require.NoError(t, err)
			require.Equal(t, 1, waiting, "workflow must wait for the upstream PR before refresh")
			now := time.Now().UTC().Truncate(time.Second)
			require.NoError(t, f.stores.Fixtures.SetTaskPullRequestUpdatedAt(ctx, task.ID, now.Add(-time.Hour)))
			provider := &upstreamTrackingProvider{fakeAutomationPullRequestProvider: fakeAutomationPullRequestProvider{pull: GitHubPullRequest{Number: 7, URL: record.PRURL, State: "closed", Merged: true}}}
			_, err = NewAutomationExternalStateService(f.repo, f.stores.TaskPullRequests, f.stores.Projects, provider).Refresh(ctx, f.project.ID, f.definition.Automation.ID, now)
			require.NoError(t, err)
			require.Equal(t, "upstream/original", provider.requestedRepo)
			completed, err := f.stores.Fixtures.CountMatchingWorkItems(ctx, storefixture.WorkItemMatch{AutomationID: f.definition.Automation.ID, Status: "completed"})
			require.NoError(t, err)
			require.Equal(t, 1, completed, "upstream merge must advance the waiting workflow")
		})
	}
}

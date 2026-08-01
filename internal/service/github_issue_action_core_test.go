package service

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

type fakeGitHubIssueActionProvider struct {
	getIssueNumber int
	commentNumber  int
	commentBody    string
	labelNumber    int
	labels         []string
}

func (f *fakeGitHubIssueActionProvider) GetIssue(_ context.Context, _ *GitHubRepoRef, issueNumber int) (*GitHubIssue, error) {
	f.getIssueNumber = issueNumber
	return &GitHubIssue{Number: issueNumber, Title: "shared"}, nil
}
func (f *fakeGitHubIssueActionProvider) ListAuthenticatedAssignedIssues(_ context.Context, _ *GitHubRepoRef) (*GitHubAuthenticatedUser, []GitHubIssue, error) {
	return &GitHubAuthenticatedUser{Login: "Me"}, []GitHubIssue{{Number: 1}}, nil
}
func (f *fakeGitHubIssueActionProvider) ListAssignedIssues(_ context.Context, _ *GitHubRepoRef, _ string) ([]GitHubIssue, error) {
	return []GitHubIssue{{Number: 2}}, nil
}
func (f *fakeGitHubIssueActionProvider) ListAssignedIssuesWithPullRequests(_ context.Context, _ *GitHubRepoRef, _ string) ([]GitHubIssueWithPullRequest, error) {
	return []GitHubIssueWithPullRequest{{Issue: GitHubIssue{Number: 3}}}, nil
}
func (f *fakeGitHubIssueActionProvider) CommentOnIssue(_ context.Context, _ *GitHubRepoRef, issueNumber int, body string) error {
	f.commentNumber, f.commentBody = issueNumber, body
	return nil
}
func (f *fakeGitHubIssueActionProvider) AddLabelsToIssue(_ context.Context, _ *GitHubRepoRef, issueNumber int, labels []string) error {
	f.labelNumber, f.labels = issueNumber, labels
	return nil
}

type fakeGitHubIssueAuthorizationStore struct{}

func (fakeGitHubIssueAuthorizationStore) ListAuthorizedInboxAssignees(context.Context) ([]models.GitHubAuthorizedActor, error) {
	return []models.GitHubAuthorizedActor{{GitHubLogin: " Alice "}}, nil
}
func (fakeGitHubIssueAuthorizationStore) GetEnabledProjectInbox(context.Context, string) (*models.GitHubProjectInbox, error) {
	return &models.GitHubProjectInbox{GitHubLogin: "legacy", Enabled: true}, nil
}
func (fakeGitHubIssueAuthorizationStore) IsActorAuthorized(context.Context, string) (bool, error) {
	return true, nil
}

func TestGitHubIssueActionCoreCommonActionsAndAssignedIssuePostprocessing(t *testing.T) {
	provider := &fakeGitHubIssueActionProvider{}
	var resolved []string
	core := NewGitHubIssueActionCore(provider, fakeGitHubIssueAuthorizationStore{}, "project-1",
		func(input json.RawMessage, dst any) error { return json.Unmarshal(input, dst) },
		func(_ context.Context, repoURL string) (*GitHubRepoRef, error) {
			resolved = append(resolved, repoURL)
			return &GitHubRepoRef{FullName: "owner/repo"}, nil
		})
	ctx := context.Background()

	out, err := core.ExecuteGetIssue(ctx, json.RawMessage(`{"issue_number":7,"repo_url":"https://github.com/owner/repo"}`))
	if err != nil || !jsonContains(t, out, `{"ok":true,"issue":{"Number":7,"Title":"shared"}}`) {
		t.Fatalf("get issue output=%q err=%v", out, err)
	}
	if provider.getIssueNumber != 7 || len(resolved) != 1 || resolved[0] != "https://github.com/owner/repo" {
		t.Fatalf("get issue wiring number=%d resolved=%v", provider.getIssueNumber, resolved)
	}

	out, err = core.ExecuteGetProjectInbox(ctx, nil)
	if err != nil || !jsonContains(t, out, `{"configured":true,"assignees":["alice"]}`) {
		t.Fatalf("project inbox output=%q err=%v", out, err)
	}
	out, err = core.ExecuteIsActorAuthorized(ctx, json.RawMessage(`{"github_login":" ALICE "}`))
	if err != nil || !jsonContains(t, out, `{"github_login":"alice","authorized":true}`) {
		t.Fatalf("authorization output=%q err=%v", out, err)
	}

	postprocessCalls := 0
	postprocess := func(_ context.Context, _ *GitHubRepoRef, issues []GitHubIssue) ([]GitHubIssue, error) {
		postprocessCalls++
		return append(issues, GitHubIssue{Number: 9}), nil
	}
	out, err = core.ExecuteListMyAssignedIssues(ctx, json.RawMessage(`{}`), postprocess)
	if err != nil || !strings.Contains(out, `"account":{"login":"Me"`) || !strings.Contains(out, `"Number":1`) || !strings.Contains(out, `"Number":9`) {
		t.Fatalf("my assigned output=%q err=%v", out, err)
	}
	out, err = core.ExecuteListAssignedIssues(ctx, json.RawMessage(`{"assignee":" Dev-Bot "}`), postprocess)
	if err != nil || !strings.Contains(out, `"assignee":"dev-bot"`) || !strings.Contains(out, `"Number":2`) || !strings.Contains(out, `"Number":9`) {
		t.Fatalf("assigned output=%q err=%v", out, err)
	}
	if postprocessCalls != 2 {
		t.Fatalf("postprocess calls=%d, want 2", postprocessCalls)
	}

	out, err = core.ExecuteListAssignedIssuesWithPRs(ctx, json.RawMessage(`{"assignee":"Dev-Bot"}`))
	if err != nil || !jsonContains(t, out, `{"skipped_without_pr":"Assigned issues without an associated pull request are skipped."}`) {
		t.Fatalf("assigned with PRs output=%q err=%v", out, err)
	}
	out, err = core.ExecuteCommentOnIssue(ctx, json.RawMessage(`{"issue_number":8,"body":"hello"}`))
	if err != nil || out != `{"issue_number":8,"ok":true}` || provider.commentNumber != 8 || provider.commentBody != "hello" {
		t.Fatalf("comment output=%q request=%d/%q err=%v", out, provider.commentNumber, provider.commentBody, err)
	}
	out, err = core.ExecuteAddIssueLabels(ctx, json.RawMessage(`{"issue_number":8,"labels":["bug"]}`))
	if err != nil || out != `{"issue_number":8,"labels":["bug"],"ok":true}` || provider.labelNumber != 8 || len(provider.labels) != 1 || provider.labels[0] != "bug" {
		t.Fatalf("labels output=%q request=%d/%v err=%v", out, provider.labelNumber, provider.labels, err)
	}
}

func jsonContains(t *testing.T, actual, subset string) bool {
	t.Helper()
	var got, want map[string]any
	if err := json.Unmarshal([]byte(actual), &got); err != nil {
		t.Fatalf("decode actual JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(subset), &want); err != nil {
		t.Fatalf("decode subset JSON: %v", err)
	}
	return jsonValueContains(got, want)
}

func jsonValueContains(got, want any) bool {
	wantMap, ok := want.(map[string]any)
	if !ok {
		return reflect.DeepEqual(got, want)
	}
	gotMap, ok := got.(map[string]any)
	if !ok {
		return false
	}
	for key, wantValue := range wantMap {
		gotValue, ok := gotMap[key]
		if !ok || !jsonValueContains(gotValue, wantValue) {
			return false
		}
	}
	return true
}

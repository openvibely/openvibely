package service

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type fakeGitHubIssueActionProvider struct {
	getIssueNumber            int
	commentNumber             int
	commentBody               string
	labelNumber               int
	labels                    []string
	closeNumber               int
	createdIssues             []GitHubIssue
	myAssignedIssues          []GitHubIssue
	assignedIssues            []GitHubIssue
	assignedIssuesWithPRs     []GitHubIssueWithPullRequest
	myAssignedCalls           int
	assignedIssuesCalls       int
	assignedIssuesWithPRCalls int
}

func (f *fakeGitHubIssueActionProvider) GetIssue(_ context.Context, _ *GitHubRepoRef, issueNumber int) (*GitHubIssue, error) {
	f.getIssueNumber = issueNumber
	return &GitHubIssue{Number: issueNumber, Title: "shared"}, nil
}
func (f *fakeGitHubIssueActionProvider) ListAuthenticatedAssignedIssues(_ context.Context, _ *GitHubRepoRef) (*GitHubAuthenticatedUser, []GitHubIssue, error) {
	f.myAssignedCalls++
	if f.myAssignedIssues != nil {
		return &GitHubAuthenticatedUser{Login: "Me"}, f.myAssignedIssues, nil
	}
	return &GitHubAuthenticatedUser{Login: "Me"}, []GitHubIssue{{Number: 1}}, nil
}
func (f *fakeGitHubIssueActionProvider) ListAuthenticatedCreatedIssues(_ context.Context, _ *GitHubRepoRef) (*GitHubAuthenticatedUser, []GitHubIssue, error) {
	if f.createdIssues != nil {
		return &GitHubAuthenticatedUser{Login: "Me"}, f.createdIssues, nil
	}
	return &GitHubAuthenticatedUser{Login: "Me"}, []GitHubIssue{{Number: 9, URL: "https://github.com/owner/repo/issues/9", Title: "Existing issue", Body: "Detailed existing issue body", State: "open", UserLogin: "Me", Labels: []string{"bug"}}}, nil
}
func (f *fakeGitHubIssueActionProvider) ListAssignedIssues(_ context.Context, _ *GitHubRepoRef, _ string) ([]GitHubIssue, error) {
	f.assignedIssuesCalls++
	if f.assignedIssues != nil {
		return f.assignedIssues, nil
	}
	return []GitHubIssue{{Number: 2}}, nil
}
func (f *fakeGitHubIssueActionProvider) ListAssignedIssuesWithPullRequests(_ context.Context, _ *GitHubRepoRef, _ string) ([]GitHubIssueWithPullRequest, error) {
	f.assignedIssuesWithPRCalls++
	if f.assignedIssuesWithPRs != nil {
		return f.assignedIssuesWithPRs, nil
	}
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
func (f *fakeGitHubIssueActionProvider) CloseIssue(_ context.Context, _ *GitHubRepoRef, issueNumber int) error {
	f.closeNumber = issueNumber
	return nil
}

type fakeGitHubIssueAuthorizationStore struct {
	authorized map[string]bool
}

func (fakeGitHubIssueAuthorizationStore) ListAuthorizedInboxAssignees(context.Context) ([]models.GitHubAuthorizedActor, error) {
	return []models.GitHubAuthorizedActor{{GitHubLogin: " Alice "}}, nil
}
func (fakeGitHubIssueAuthorizationStore) GetEnabledProjectInbox(context.Context, string) (*models.GitHubProjectInbox, error) {
	return &models.GitHubProjectInbox{GitHubLogin: "legacy", Enabled: true}, nil
}
func (f fakeGitHubIssueAuthorizationStore) IsActorAuthorized(_ context.Context, login string) (bool, error) {
	if f.authorized == nil {
		return true, nil
	}
	return f.authorized[repository.NormalizeGitHubLogin(login)], nil
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

	out, err := core.ExecuteGetIssue(ctx, json.RawMessage(`{"issue_number":7,"repo_url":"get"}`))
	if err != nil || !jsonContains(t, out, `{"ok":true,"issue":{"Number":7,"Title":"shared"}}`) {
		t.Fatalf("get issue output=%q err=%v", out, err)
	}
	if provider.getIssueNumber != 7 {
		t.Fatalf("get issue number=%d, want 7", provider.getIssueNumber)
	}

	out, err = core.ExecuteGetProjectInbox(ctx, nil)
	if err != nil || !jsonContains(t, out, `{"configured":true,"assignees":["alice"]}`) {
		t.Fatalf("project inbox output=%q err=%v", out, err)
	}
	out, err = core.ExecuteIsActorAuthorized(ctx, json.RawMessage(`{"github_login":" ALICE "}`))
	if err != nil || !jsonContains(t, out, `{"github_login":"alice","authorized":true}`) {
		t.Fatalf("authorization output=%q err=%v", out, err)
	}

	out, err = core.ExecuteListMyAssignedIssues(ctx, json.RawMessage(`{"repo_url":"my-assigned"}`))
	if err != nil || !strings.Contains(out, `"account":{"login":"Me"`) || !strings.Contains(out, `"number":1`) || !strings.Contains(out, `"returned":1`) || !strings.Contains(out, `"total":1`) {
		t.Fatalf("my assigned output=%q err=%v", out, err)
	}
	out, err = core.ExecuteListAssignedIssues(ctx, json.RawMessage(`{"assignee":" Dev-Bot ","repo_url":"assigned"}`))
	if err != nil || !strings.Contains(out, `"assignee":"dev-bot"`) || !strings.Contains(out, `"number":2`) || !strings.Contains(out, `"returned":1`) || !strings.Contains(out, `"total":1`) {
		t.Fatalf("assigned output=%q err=%v", out, err)
	}
	out, err = core.ExecuteListExistingAutomationIssues(ctx, json.RawMessage(`{"repo_url":"created","limit":1}`))
	if err != nil || !strings.Contains(out, `"repository":"owner/repo"`) || !strings.Contains(out, `"title":"Existing issue"`) {
		t.Fatalf("existing Automation issues output=%q err=%v", out, err)
	}
	if strings.Contains(out, `"body_excerpt"`) || strings.Contains(out, "Detailed existing issue body") {
		t.Fatalf("existing Automation issues should omit compact body text: %q", out)
	}

	out, err = core.ExecuteListAssignedIssuesWithPRs(ctx, json.RawMessage(`{"assignee":"Dev-Bot","repo_url":"assigned-with-prs"}`))
	if err != nil || !jsonContains(t, out, `{"skipped_without_pr":"Assigned issues without an associated pull request are skipped."}`) {
		t.Fatalf("assigned with PRs output=%q err=%v", out, err)
	}
	out, err = core.ExecuteCommentOnIssue(ctx, json.RawMessage(`{"issue_number":8,"body":"hello","repo_url":"comment"}`))
	if err != nil || out != `{"issue_number":8,"ok":true}` || provider.commentNumber != 8 || provider.commentBody != "hello" {
		t.Fatalf("comment output=%q request=%d/%q err=%v", out, provider.commentNumber, provider.commentBody, err)
	}
	out, err = core.ExecuteAddIssueLabels(ctx, json.RawMessage(`{"issue_number":8,"labels":["bug"],"repo_url":"labels"}`))
	if err != nil || out != `{"issue_number":8,"labels":["bug"],"ok":true}` || provider.labelNumber != 8 || len(provider.labels) != 1 || provider.labels[0] != "bug" {
		t.Fatalf("labels output=%q request=%d/%v err=%v", out, provider.labelNumber, provider.labels, err)
	}
	out, err = core.ExecuteCloseIssue(ctx, json.RawMessage(`{"issue_number":8,"repo_url":"close"}`))
	if err != nil || out != `{"issue_number":8,"ok":true,"state":"closed"}` || provider.closeNumber != 8 {
		t.Fatalf("close output=%q request=%d err=%v", out, provider.closeNumber, err)
	}

	wantResolved := []string{"get", "my-assigned", "assigned", "created", "assigned-with-prs", "comment", "labels", "close"}
	if !reflect.DeepEqual(resolved, wantResolved) {
		t.Fatalf("resolved repositories=%v, want %v", resolved, wantResolved)
	}
}

func TestGitHubIssueActionCoreListExistingAutomationIssuesPaginatesCallerVisibleResults(t *testing.T) {
	provider := &fakeGitHubIssueActionProvider{createdIssues: []GitHubIssue{
		{Number: 1, Title: "Newest issue", State: "open", UserLogin: "Me"},
		{Number: 2, Title: "Middle issue", State: "closed", UserLogin: "Me"},
		{Number: 3, Title: "Oldest issue", State: "open", UserLogin: "Me"},
	}}
	core := NewGitHubIssueActionCore(provider, fakeGitHubIssueAuthorizationStore{}, "project-1",
		func(input json.RawMessage, dst any) error { return json.Unmarshal(input, dst) },
		func(context.Context, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{FullName: "owner/repo"}, nil
		})
	ctx := context.Background()

	first, err := core.ExecuteListExistingAutomationIssues(ctx, json.RawMessage(`{"limit":2}`))
	if err != nil {
		t.Fatalf("first page err=%v output=%q", err, first)
	}
	for _, want := range []string{`"returned":2`, `"total":3`, `"offset":0`, `"next_offset":2`, `"truncated":true`, `"title":"Newest issue"`, `"title":"Middle issue"`} {
		if !strings.Contains(first, want) {
			t.Fatalf("first page missing %s: %q", want, first)
		}
	}
	if strings.Contains(first, `"title":"Oldest issue"`) {
		t.Fatalf("first page included second-page issue: %q", first)
	}

	second, err := core.ExecuteListExistingAutomationIssues(ctx, json.RawMessage(`{"limit":2,"offset":2}`))
	if err != nil {
		t.Fatalf("second page err=%v output=%q", err, second)
	}
	for _, want := range []string{`"returned":1`, `"total":3`, `"offset":2`, `"next_offset":0`, `"truncated":false`, `"title":"Oldest issue"`} {
		if !strings.Contains(second, want) {
			t.Fatalf("second page missing %s: %q", want, second)
		}
	}

	empty, err := core.ExecuteListExistingAutomationIssues(ctx, json.RawMessage(`{"limit":2,"offset":4}`))
	if err != nil {
		t.Fatalf("empty page err=%v output=%q", err, empty)
	}
	for _, want := range []string{`"issues":[]`, `"returned":0`, `"total":3`, `"offset":4`, `"next_offset":0`, `"truncated":false`} {
		if !strings.Contains(empty, want) {
			t.Fatalf("empty page missing %s: %q", want, empty)
		}
	}

	if _, err := core.ExecuteListExistingAutomationIssues(ctx, json.RawMessage(`{"offset":-1}`)); err == nil || err.Error() != "limit must be 1-100 and offset must be non-negative" {
		t.Fatalf("negative offset error=%v, want validation error", err)
	}
}

func TestGitHubIssueActionCoreAssignedIssuesWithPRsPaginateCallerVisibleResults(t *testing.T) {
	provider := &fakeGitHubIssueActionProvider{assignedIssuesWithPRs: []GitHubIssueWithPullRequest{
		{Issue: GitHubIssue{Number: 1, Title: "First"}, PullRequest: GitHubPullRequest{Number: 101}},
		{Issue: GitHubIssue{Number: 2, Title: "Second"}, PullRequest: GitHubPullRequest{Number: 102}},
		{Issue: GitHubIssue{Number: 3, Title: "Third"}, PullRequest: GitHubPullRequest{Number: 103}},
	}}
	core := NewGitHubIssueActionCore(provider, fakeGitHubIssueAuthorizationStore{}, "project-1",
		func(input json.RawMessage, dst any) error { return json.Unmarshal(input, dst) },
		func(context.Context, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{FullName: "owner/repo"}, nil
		})

	type response struct {
		Items      []GitHubIssueWithPullRequest `json:"items"`
		Returned   int                          `json:"returned"`
		Total      int                          `json:"total"`
		Offset     int                          `json:"offset"`
		NextOffset int                          `json:"next_offset"`
		Truncated  bool                         `json:"truncated"`
	}
	page := func(input string, wantNumbers []int, wantOffset, wantNext int, wantTruncated bool) response {
		t.Helper()
		output, err := core.ExecuteListAssignedIssuesWithPRs(context.Background(), json.RawMessage(input))
		if err != nil {
			t.Fatalf("page input %s returned error: %v", input, err)
		}
		var got response
		if err := json.Unmarshal([]byte(output), &got); err != nil {
			t.Fatalf("decode page output %q: %v", output, err)
		}
		if got.Returned != len(wantNumbers) || got.Total != 3 || got.Offset != wantOffset || got.NextOffset != wantNext || got.Truncated != wantTruncated {
			t.Fatalf("page input %s metadata=%+v, want returned=%d total=3 offset=%d next_offset=%d truncated=%t", input, got, len(wantNumbers), wantOffset, wantNext, wantTruncated)
		}
		if len(got.Items) != len(wantNumbers) {
			t.Fatalf("page input %s returned %d items, want %d: %s", input, len(got.Items), len(wantNumbers), output)
		}
		for i, wantNumber := range wantNumbers {
			if got.Items[i].Issue.Number != wantNumber {
				t.Fatalf("page input %s item %d number=%d, want %d", input, i, got.Items[i].Issue.Number, wantNumber)
			}
		}
		return got
	}

	page(`{"assignee":"dev-bot","limit":1,"offset":0}`, []int{1}, 0, 1, true)
	page(`{"assignee":"dev-bot","limit":1,"offset":1}`, []int{2}, 1, 2, true)
	page(`{"assignee":"dev-bot","limit":1,"offset":2}`, []int{3}, 2, 0, false)
	page(`{"assignee":"dev-bot","limit":1,"offset":3}`, nil, 3, 0, false)
	page(`{"assignee":"dev-bot","limit":1,"offset":4}`, nil, 4, 0, false)

	callsBeforeInvalid := provider.assignedIssuesWithPRCalls
	for _, input := range []string{
		`{"assignee":"dev-bot","limit":0}`,
		`{"assignee":"dev-bot","Limit":0}`,
		`{"assignee":"dev-bot","limit":101}`,
		`{"assignee":"dev-bot","offset":-1}`,
		`{"assignee":"dev-bot","Offset":-1}`,
	} {
		if _, err := core.ExecuteListAssignedIssuesWithPRs(context.Background(), json.RawMessage(input)); err == nil || err.Error() != "limit must be 1-100 and offset must be non-negative" {
			t.Fatalf("invalid page input %s error=%v, want validation error", input, err)
		}
	}
	if provider.assignedIssuesWithPRCalls != callsBeforeInvalid {
		t.Fatalf("provider calls after invalid pages=%d, want %d", provider.assignedIssuesWithPRCalls, callsBeforeInvalid)
	}
}

func TestGitHubIssueActionCoreAssignedIssuesReturnCompactCompleteCandidateList(t *testing.T) {
	issueNumbers := []int{792, 791, 789, 783, 776, 768, 767, 766, 755}
	issues := make([]GitHubIssue, 0, len(issueNumbers))
	for _, number := range issueNumbers {
		issues = append(issues, GitHubIssue{
			Number:                        number,
			URL:                           fmt.Sprintf("https://github.com/openvibely/openvibely/issues/%d", number),
			Title:                         fmt.Sprintf("Issue %d", number),
			Body:                          fmt.Sprintf("body-%d-", number) + strings.Repeat("long body text ", 600),
			State:                         "open",
			UserLogin:                     "openvibely",
			Assignees:                     []string{"openvibely"},
			Labels:                        []string{"suggestion", "feature"},
			CompleteForTaskCreation:       true,
			TaskCreationCompletenessKnown: true,
		})
	}
	provider := &fakeGitHubIssueActionProvider{assignedIssues: issues}
	core := NewGitHubIssueActionCore(provider, fakeGitHubIssueAuthorizationStore{}, "project-1",
		func(input json.RawMessage, dst any) error { return json.Unmarshal(input, dst) },
		func(context.Context, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{FullName: "openvibely/openvibely"}, nil
		})

	out, err := core.ExecuteListAssignedIssues(context.Background(), json.RawMessage(`{"assignee":"openvibely"}`))
	if err != nil {
		t.Fatalf("assigned issues output err=%v", err)
	}
	for _, want := range []string{`"returned":9`, `"total":9`, `"truncated":false`, `"next_offset":0`} {
		if !strings.Contains(out, want) {
			t.Fatalf("compact assigned issue output missing %s: %s", want, out)
		}
	}
	for _, number := range issueNumbers {
		if !strings.Contains(out, fmt.Sprintf(`"number":%d`, number)) || !strings.Contains(out, fmt.Sprintf(`"Issue %d"`, number)) {
			t.Fatalf("compact assigned issue output omitted issue #%d: %s", number, out)
		}
	}
	if strings.Contains(out, "long body text") || strings.Contains(out, "body_excerpt") || strings.Contains(out, "body_truncated") {
		t.Fatalf("compact assigned issue output should not include body text or body fields: %d bytes", len(out))
	}
	if !strings.Contains(out, `"detail_required":true`) || !strings.Contains(out, `"complete_for_task_creation":false`) || !strings.Contains(out, `"task_creation_completeness_known":false`) {
		t.Fatalf("body-free assigned issue output should require targeted detail hydration: %s", out)
	}
	if len(out) > 8000 {
		t.Fatalf("compact assigned issue output is too large: %d bytes", len(out))
	}
}

func TestGitHubIssueActionCoreAssignedIssuesPaginateCompactCandidateList(t *testing.T) {
	provider := &fakeGitHubIssueActionProvider{assignedIssues: []GitHubIssue{
		{Number: 1, Title: "First", Body: "small body", TaskCreationCompletenessKnown: true, CompleteForTaskCreation: true},
		{Number: 2, Title: "Second", Body: "small body", TaskCreationCompletenessKnown: true, CompleteForTaskCreation: true},
		{Number: 3, Title: "Third", Body: "small body", TaskCreationCompletenessKnown: true, CompleteForTaskCreation: true},
	}}
	core := NewGitHubIssueActionCore(provider, fakeGitHubIssueAuthorizationStore{}, "project-1",
		func(input json.RawMessage, dst any) error { return json.Unmarshal(input, dst) },
		func(context.Context, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{FullName: "owner/repo"}, nil
		})

	first, err := core.ExecuteListAssignedIssues(context.Background(), json.RawMessage(`{"assignee":"openvibely","limit":2}`))
	if err != nil {
		t.Fatalf("first page err=%v", err)
	}
	for _, want := range []string{`"returned":2`, `"total":3`, `"offset":0`, `"next_offset":2`, `"truncated":true`, `"number":1`, `"number":2`} {
		if !strings.Contains(first, want) {
			t.Fatalf("first page missing %s: %s", want, first)
		}
	}
	if strings.Contains(first, `"number":3`) {
		t.Fatalf("first page included second-page issue: %s", first)
	}
	second, err := core.ExecuteListAssignedIssues(context.Background(), json.RawMessage(`{"assignee":"openvibely","limit":2,"offset":2}`))
	if err != nil {
		t.Fatalf("second page err=%v", err)
	}
	for _, want := range []string{`"returned":1`, `"total":3`, `"offset":2`, `"next_offset":0`, `"truncated":false`, `"number":3`} {
		if !strings.Contains(second, want) {
			t.Fatalf("second page missing %s: %s", want, second)
		}
	}
	if _, err := core.ExecuteListAssignedIssues(context.Background(), json.RawMessage(`{"assignee":"openvibely","limit":101}`)); err == nil || err.Error() != "limit must be 1-100 and offset must be non-negative" {
		t.Fatalf("invalid limit error=%v, want validation error", err)
	}
}

func TestGitHubIssueActionCoreAssignedIssuePaginationDistinguishesOmittedLimitFromExplicitZero(t *testing.T) {
	issues := make([]GitHubIssue, 101)
	issuesWithPRs := make([]GitHubIssueWithPullRequest, 101)
	for i := range issues {
		issue := GitHubIssue{Number: i + 1, Title: fmt.Sprintf("Issue %d", i+1)}
		issues[i] = issue
		issuesWithPRs[i] = GitHubIssueWithPullRequest{Issue: issue, PullRequest: GitHubPullRequest{Number: i + 1001}}
	}
	provider := &fakeGitHubIssueActionProvider{
		myAssignedIssues:      issues,
		assignedIssues:        issues,
		assignedIssuesWithPRs: issuesWithPRs,
	}
	core := NewGitHubIssueActionCore(provider, fakeGitHubIssueAuthorizationStore{}, "project-1",
		func(input json.RawMessage, dst any) error { return json.Unmarshal(input, dst) },
		func(context.Context, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{FullName: "owner/repo"}, nil
		})

	type paginationMetadata struct {
		Returned   int  `json:"returned"`
		Total      int  `json:"total"`
		Offset     int  `json:"offset"`
		NextOffset int  `json:"next_offset"`
		Truncated  bool `json:"truncated"`
	}
	type action struct {
		name      string
		omitted   string
		invalid   []string
		execute   func(context.Context, json.RawMessage) (string, error)
		callCount func() int
	}
	actions := []action{
		{
			name:      "my assigned issues",
			omitted:   `{}`,
			invalid:   []string{`{"limit":0}`, `{"Limit":0}`, `{"LIMIT":0}`, `{"limit":101}`, `{"offset":-1}`, `{"Offset":-1}`},
			execute:   core.ExecuteListMyAssignedIssues,
			callCount: func() int { return provider.myAssignedCalls },
		},
		{
			name:      "assigned issues",
			omitted:   `{"assignee":"openvibely"}`,
			invalid:   []string{`{"assignee":"openvibely","limit":0}`, `{"assignee":"openvibely","Limit":0}`, `{"assignee":"openvibely","LIMIT":0}`, `{"assignee":"openvibely","limit":101}`, `{"assignee":"openvibely","offset":-1}`, `{"assignee":"openvibely","Offset":-1}`},
			execute:   core.ExecuteListAssignedIssues,
			callCount: func() int { return provider.assignedIssuesCalls },
		},
		{
			name:      "assigned issues with pull requests",
			omitted:   `{"assignee":"openvibely"}`,
			invalid:   []string{`{"assignee":"openvibely","limit":0}`, `{"assignee":"openvibely","Limit":0}`, `{"assignee":"openvibely","LIMIT":0}`, `{"assignee":"openvibely","limit":101}`, `{"assignee":"openvibely","offset":-1}`, `{"assignee":"openvibely","Offset":-1}`},
			execute:   core.ExecuteListAssignedIssuesWithPRs,
			callCount: func() int { return provider.assignedIssuesWithPRCalls },
		},
	}

	for _, action := range actions {
		t.Run(action.name, func(t *testing.T) {
			callsBeforeOmitted := action.callCount()
			output, err := action.execute(context.Background(), json.RawMessage(action.omitted))
			if err != nil {
				t.Fatalf("omitted limit error=%v output=%q", err, output)
			}
			var metadata paginationMetadata
			if err := json.Unmarshal([]byte(output), &metadata); err != nil {
				t.Fatalf("decode omitted-limit output %q: %v", output, err)
			}
			if metadata.Returned != 100 || metadata.Total != 101 || metadata.Offset != 0 || metadata.NextOffset != 100 || !metadata.Truncated {
				t.Fatalf("omitted-limit metadata=%+v, want returned=100 total=101 offset=0 next_offset=100 truncated=true", metadata)
			}
			if calls := action.callCount(); calls != callsBeforeOmitted+1 {
				t.Fatalf("provider calls after omitted limit=%d, want %d", calls, callsBeforeOmitted+1)
			}

			callsBeforeInvalid := action.callCount()
			for _, input := range action.invalid {
				if output, err := action.execute(context.Background(), json.RawMessage(input)); err == nil || err.Error() != "limit must be 1-100 and offset must be non-negative" {
					t.Fatalf("invalid input %s output=%q error=%v, want validation error", input, output, err)
				}
				if calls := action.callCount(); calls != callsBeforeInvalid {
					t.Fatalf("provider calls after invalid input %s=%d, want %d", input, calls, callsBeforeInvalid)
				}
			}
		})
	}
}

func TestGitHubIssueActionCoreAssignedIssueValidationPrecedesRepoResolution(t *testing.T) {
	resolveCalls := 0
	core := NewGitHubIssueActionCore(&fakeGitHubIssueActionProvider{}, nil, "",
		func(input json.RawMessage, dst any) error { return json.Unmarshal(input, dst) },
		func(context.Context, string) (*GitHubRepoRef, error) {
			resolveCalls++
			return nil, nil
		})

	for _, execute := range []func(context.Context, json.RawMessage) (string, error){
		func(ctx context.Context, input json.RawMessage) (string, error) {
			return core.ExecuteListAssignedIssues(ctx, input)
		},
		core.ExecuteListAssignedIssuesWithPRs,
	} {
		if _, err := execute(context.Background(), json.RawMessage(`{"repo_url":"ignored"}`)); err == nil || err.Error() != "assignee is required" {
			t.Fatalf("error=%v, want assignee is required", err)
		}
	}
	if resolveCalls != 0 {
		t.Fatalf("resolve calls=%d, want 0", resolveCalls)
	}
}

func TestGitHubIssueActionCoreListAssignedIssuesRejectsUnauthorizedAssigneeBeforeRepoResolution(t *testing.T) {
	resolveCalls := 0
	provider := &fakeGitHubIssueActionProvider{}
	core := NewGitHubIssueActionCore(provider, fakeGitHubIssueAuthorizationStore{authorized: map[string]bool{"alice": true}}, "",
		func(input json.RawMessage, dst any) error { return json.Unmarshal(input, dst) },
		func(context.Context, string) (*GitHubRepoRef, error) {
			resolveCalls++
			return &GitHubRepoRef{FullName: "owner/repo"}, nil
		})

	for _, execute := range []func(context.Context, json.RawMessage) (string, error){
		func(ctx context.Context, input json.RawMessage) (string, error) {
			return core.ExecuteListAssignedIssues(ctx, input)
		},
		core.ExecuteListAssignedIssuesWithPRs,
	} {
		_, err := execute(context.Background(), json.RawMessage(`{"assignee":"mallory","repo_url":"ignored"}`))
		if err == nil || err.Error() != "GitHub assignee mallory is not authorized" {
			t.Fatalf("error=%v, want unauthorized assignee", err)
		}
	}
	if resolveCalls != 0 {
		t.Fatalf("resolve calls=%d, want 0", resolveCalls)
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

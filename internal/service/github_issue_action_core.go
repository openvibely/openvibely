package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// GitHubIssueActionRequest is the canonical request contract shared by
// interactive and task-runtime GitHub issue actions.
type GitHubIssueActionRequest struct {
	IssueNumber           int      `json:"issue_number"`
	IssueURL              string   `json:"issue_url"`
	RepoURL               string   `json:"repo_url"`
	Assignee              string   `json:"assignee"`
	GitHubLogin           string   `json:"github_login"`
	Body                  string   `json:"body"`
	Labels                []string `json:"labels"`
	TaskID                string   `json:"task_id"`
	Title                 string   `json:"title"`
	PRTitle               string   `json:"pr_title"`
	PRBody                string   `json:"pr_body"`
	Base                  string   `json:"base"`
	Draft                 bool     `json:"draft"`
	ExpectedHeadSHA       string   `json:"expected_head_sha"`
	ConfirmHistoryRewrite bool     `json:"confirm_history_rewrite"`
	Limit                 int      `json:"limit"`
	Offset                int      `json:"offset"`
}

type GitHubIssueActionProvider interface {
	GetIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int) (*GitHubIssue, error)
	ListAuthenticatedAssignedIssues(ctx context.Context, repo *GitHubRepoRef) (*GitHubAuthenticatedUser, []GitHubIssue, error)
	ListAuthenticatedCreatedIssues(ctx context.Context, repo *GitHubRepoRef) (*GitHubAuthenticatedUser, []GitHubIssue, error)
	ListAssignedIssues(ctx context.Context, repo *GitHubRepoRef, assignee string) ([]GitHubIssue, error)
	ListAssignedIssuesWithPullRequests(ctx context.Context, repo *GitHubRepoRef, assignee string) ([]GitHubIssueWithPullRequest, error)
	CommentOnIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int, bodyText string) error
	AddLabelsToIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int, labels []string) error
	CloseIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int) error
}

type GitHubIssueAuthorizationStore interface {
	ListAuthorizedInboxAssignees(ctx context.Context) ([]models.GitHubAuthorizedActor, error)
	GetEnabledProjectInbox(ctx context.Context, projectID string) (*models.GitHubProjectInbox, error)
	IsActorAuthorized(ctx context.Context, githubLogin string) (bool, error)
}

type GitHubIssueActionDecoder func(input json.RawMessage, dst any) error
type GitHubIssueRepoResolver func(ctx context.Context, repoURL string) (*GitHubRepoRef, error)

type GitHubIssueActionCore struct {
	provider  GitHubIssueActionProvider
	auth      GitHubIssueAuthorizationStore
	projectID string
	decode    GitHubIssueActionDecoder
	resolve   GitHubIssueRepoResolver
}

func NewGitHubIssueActionCore(provider GitHubIssueActionProvider, auth GitHubIssueAuthorizationStore, projectID string, decode GitHubIssueActionDecoder, resolve GitHubIssueRepoResolver) *GitHubIssueActionCore {
	return &GitHubIssueActionCore{provider: provider, auth: auth, projectID: projectID, decode: decode, resolve: resolve}
}

func (c *GitHubIssueActionCore) request(input json.RawMessage) (GitHubIssueActionRequest, error) {
	var req GitHubIssueActionRequest
	if err := c.decode(input, &req); err != nil {
		return req, err
	}
	return req, nil
}

func (c *GitHubIssueActionCore) requestAndRepo(ctx context.Context, input json.RawMessage, validate func(GitHubIssueActionRequest) error) (GitHubIssueActionRequest, *GitHubRepoRef, error) {
	req, err := c.request(input)
	if err != nil {
		return req, nil, err
	}
	if validate != nil {
		if err := validate(req); err != nil {
			return req, nil, err
		}
	}
	repo, err := c.resolve(ctx, req.RepoURL)
	if err != nil {
		return req, nil, err
	}
	return req, repo, nil
}

func (c *GitHubIssueActionCore) ExecuteGetIssue(ctx context.Context, input json.RawMessage) (string, error) {
	req, repo, err := c.requestAndRepo(ctx, input, nil)
	if err != nil {
		return "", err
	}
	issue, err := c.provider.GetIssue(ctx, repo, req.IssueNumber)
	if err != nil {
		return "", err
	}
	return githubIssueActionJSON(map[string]any{"ok": true, "issue": issue})
}

func (c *GitHubIssueActionCore) ExecuteGetProjectInbox(ctx context.Context, _ json.RawMessage) (string, error) {
	if c.auth == nil {
		return "", fmt.Errorf("github auth repository unavailable")
	}
	actors, err := c.auth.ListAuthorizedInboxAssignees(ctx)
	if err != nil {
		return "", err
	}
	assignees := make([]string, 0, len(actors))
	for _, actor := range actors {
		if login := repository.NormalizeGitHubLogin(actor.GitHubLogin); login != "" {
			assignees = append(assignees, login)
		}
	}
	legacyInbox, err := c.auth.GetEnabledProjectInbox(ctx, c.projectID)
	if err != nil {
		return "", err
	}
	return githubIssueActionJSON(map[string]any{"ok": true, "configured": len(assignees) > 0, "assignees": assignees, "authorized_users": actors, "legacy_inbox": legacyInbox})
}

func (c *GitHubIssueActionCore) ExecuteIsActorAuthorized(ctx context.Context, input json.RawMessage) (string, error) {
	if c.auth == nil {
		return "", fmt.Errorf("github auth repository unavailable")
	}
	req, err := c.request(input)
	if err != nil {
		return "", err
	}
	login := strings.TrimSpace(req.GitHubLogin)
	if login == "" {
		return "", fmt.Errorf("github_login is required")
	}
	authorized, err := c.auth.IsActorAuthorized(ctx, login)
	if err != nil {
		return "", err
	}
	return githubIssueActionJSON(map[string]any{"ok": true, "github_login": repository.NormalizeGitHubLogin(login), "authorized": authorized})
}

func (c *GitHubIssueActionCore) ExecuteListMyAssignedIssues(ctx context.Context, input json.RawMessage) (string, error) {
	req, repo, err := c.requestAndRepo(ctx, input, nil)
	if err != nil {
		return "", err
	}
	limit, offset, err := assignedIssueListPage(req)
	if err != nil {
		return "", err
	}
	user, issues, err := c.provider.ListAuthenticatedAssignedIssues(ctx, repo)
	if err != nil {
		return "", err
	}
	summaries, nextOffset := compactAssignedGitHubIssues(issues, limit, offset)
	return githubIssueActionJSON(map[string]any{"ok": true, "account": user, "issues": summaries, "returned": len(summaries), "total": len(issues), "offset": offset, "next_offset": nextOffset, "truncated": nextOffset > 0})
}

func (c *GitHubIssueActionCore) ExecuteListExistingAutomationIssues(ctx context.Context, input json.RawMessage) (string, error) {
	req, repo, err := c.requestAndRepo(ctx, input, nil)
	if err != nil {
		return "", err
	}
	limit := req.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || req.Offset < 0 {
		return "", fmt.Errorf("limit must be 1-100 and offset must be non-negative")
	}
	user, issues, err := c.provider.ListAuthenticatedCreatedIssues(ctx, repo)
	if err != nil {
		return "", err
	}
	summaries := compactExistingGitHubIssues(issues, limit, req.Offset)
	nextOffset := 0
	if req.Offset+len(summaries) < len(issues) {
		nextOffset = req.Offset + len(summaries)
	}
	return githubIssueActionJSON(map[string]any{
		"ok": true, "account": user, "repository": repo.FullName,
		"issues": summaries, "returned": len(summaries), "total": len(issues), "offset": req.Offset, "next_offset": nextOffset, "truncated": nextOffset > 0,
	})
}

func compactExistingGitHubIssues(issues []GitHubIssue, limit, offset int) []map[string]any {
	if offset >= len(issues) {
		return []map[string]any{}
	}
	end := offset + limit
	if end > len(issues) {
		end = len(issues)
	}
	summaries := make([]map[string]any, 0, end-offset)
	for _, issue := range issues[offset:end] {
		summaries = append(summaries, map[string]any{
			"number":     issue.Number,
			"url":        issue.URL,
			"title":      issue.Title,
			"state":      issue.State,
			"labels":     issue.Labels,
			"created_by": issue.UserLogin,
		})
	}
	return summaries
}

func (c *GitHubIssueActionCore) ExecuteListAssignedIssues(ctx context.Context, input json.RawMessage) (string, error) {
	req, err := c.request(input)
	if err != nil {
		return "", err
	}
	limit, offset, err := assignedIssueListPage(req)
	if err != nil {
		return "", err
	}
	assignee := strings.TrimSpace(req.Assignee)
	if assignee == "" {
		return "", fmt.Errorf("assignee is required")
	}
	if err := c.requireAuthorizedAssignee(ctx, assignee); err != nil {
		return "", err
	}
	repo, err := c.resolve(ctx, req.RepoURL)
	if err != nil {
		return "", err
	}
	issues, err := c.provider.ListAssignedIssues(ctx, repo, assignee)
	if err != nil {
		return "", err
	}
	summaries, nextOffset := compactAssignedGitHubIssues(issues, limit, offset)
	return githubIssueActionJSON(map[string]any{"ok": true, "assignee": repository.NormalizeGitHubLogin(assignee), "issues": summaries, "returned": len(summaries), "total": len(issues), "offset": offset, "next_offset": nextOffset, "truncated": nextOffset > 0})
}

func assignedIssueListPage(req GitHubIssueActionRequest) (int, int, error) {
	limit := req.Limit
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 100 || req.Offset < 0 {
		return 0, 0, fmt.Errorf("limit must be 1-100 and offset must be non-negative")
	}
	return limit, req.Offset, nil
}

func assignedIssueListPageForInput(input json.RawMessage, req GitHubIssueActionRequest) (int, int, error) {
	if req.Limit == 0 && githubIssueActionInputHasField(input, "limit") {
		return 0, 0, fmt.Errorf("limit must be 1-100 and offset must be non-negative")
	}
	return assignedIssueListPage(req)
}

func githubIssueActionInputHasField(input json.RawMessage, field string) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(input, &object); err != nil {
		return false
	}
	for key := range object {
		if strings.EqualFold(key, field) {
			return true
		}
	}
	return false
}

func compactAssignedGitHubIssues(issues []GitHubIssue, limit, offset int) ([]map[string]any, int) {
	if offset >= len(issues) {
		return []map[string]any{}, 0
	}
	end := offset + limit
	if end > len(issues) {
		end = len(issues)
	}
	summaries := make([]map[string]any, 0, end-offset)
	for _, issue := range issues[offset:end] {
		summaries = append(summaries, map[string]any{
			"number":                           issue.Number,
			"url":                              issue.URL,
			"title":                            issue.Title,
			"state":                            issue.State,
			"created_by":                       issue.UserLogin,
			"assignees":                        issue.Assignees,
			"labels":                           issue.Labels,
			"complete_for_task_creation":       false,
			"task_creation_completeness_known": false,
			"detail_required":                  true,
		})
	}
	nextOffset := 0
	if end < len(issues) {
		nextOffset = end
	}
	return summaries, nextOffset
}

func pageAssignedGitHubIssuesWithPRs(items []GitHubIssueWithPullRequest, limit, offset int) ([]GitHubIssueWithPullRequest, int) {
	if offset >= len(items) {
		return []GitHubIssueWithPullRequest{}, 0
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	page := items[offset:end]
	nextOffset := 0
	if end < len(items) {
		nextOffset = end
	}
	return page, nextOffset
}

func (c *GitHubIssueActionCore) ExecuteListAssignedIssuesWithPRs(ctx context.Context, input json.RawMessage) (string, error) {
	req, err := c.request(input)
	if err != nil {
		return "", err
	}
	limit, offset, err := assignedIssueListPageForInput(input, req)
	if err != nil {
		return "", err
	}
	assignee := strings.TrimSpace(req.Assignee)
	if assignee == "" {
		return "", fmt.Errorf("assignee is required")
	}
	if err := c.requireAuthorizedAssignee(ctx, assignee); err != nil {
		return "", err
	}
	repo, err := c.resolve(ctx, req.RepoURL)
	if err != nil {
		return "", err
	}
	items, err := c.provider.ListAssignedIssuesWithPullRequests(ctx, repo, assignee)
	if err != nil {
		return "", err
	}
	page, nextOffset := pageAssignedGitHubIssuesWithPRs(items, limit, offset)
	return githubIssueActionJSON(map[string]any{
		"ok": true, "items": page, "returned": len(page), "total": len(items), "offset": offset,
		"next_offset": nextOffset, "truncated": nextOffset > 0,
		"skipped_without_pr": "Assigned issues without an associated pull request are skipped.",
	})
}

func (c *GitHubIssueActionCore) requireAuthorizedAssignee(ctx context.Context, assignee string) error {
	if c.auth == nil {
		return fmt.Errorf("github auth repository unavailable")
	}
	authorized, err := c.auth.IsActorAuthorized(ctx, assignee)
	if err != nil {
		return err
	}
	if !authorized {
		return fmt.Errorf("GitHub assignee %s is not authorized", repository.NormalizeGitHubLogin(assignee))
	}
	return nil
}

func (c *GitHubIssueActionCore) ExecuteCommentOnIssue(ctx context.Context, input json.RawMessage) (string, error) {
	req, repo, err := c.requestAndRepo(ctx, input, nil)
	if err != nil {
		return "", err
	}
	if err := c.provider.CommentOnIssue(ctx, repo, req.IssueNumber, req.Body); err != nil {
		return "", err
	}
	return githubIssueActionJSON(map[string]any{"ok": true, "issue_number": req.IssueNumber})
}

func (c *GitHubIssueActionCore) ExecuteAddIssueLabels(ctx context.Context, input json.RawMessage) (string, error) {
	req, repo, err := c.requestAndRepo(ctx, input, nil)
	if err != nil {
		return "", err
	}
	if err := c.provider.AddLabelsToIssue(ctx, repo, req.IssueNumber, req.Labels); err != nil {
		return "", err
	}
	return githubIssueActionJSON(map[string]any{"ok": true, "issue_number": req.IssueNumber, "labels": req.Labels})
}

func (c *GitHubIssueActionCore) ExecuteCloseIssue(ctx context.Context, input json.RawMessage) (string, error) {
	req, repo, err := c.requestAndRepo(ctx, input, nil)
	if err != nil {
		return "", err
	}
	if err := c.provider.CloseIssue(ctx, repo, req.IssueNumber); err != nil {
		return "", err
	}
	return githubIssueActionJSON(map[string]any{"ok": true, "issue_number": req.IssueNumber, "state": "closed"})
}

func githubIssueActionJSON(payload map[string]any) (string, error) {
	b, err := json.Marshal(payload)
	return string(b), err
}

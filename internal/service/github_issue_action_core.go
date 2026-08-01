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
}

type GitHubIssueActionProvider interface {
	GetIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int) (*GitHubIssue, error)
	ListAuthenticatedAssignedIssues(ctx context.Context, repo *GitHubRepoRef) (*GitHubAuthenticatedUser, []GitHubIssue, error)
	ListAssignedIssues(ctx context.Context, repo *GitHubRepoRef, assignee string) ([]GitHubIssue, error)
	ListAssignedIssuesWithPullRequests(ctx context.Context, repo *GitHubRepoRef, assignee string) ([]GitHubIssueWithPullRequest, error)
	CommentOnIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int, bodyText string) error
	AddLabelsToIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int, labels []string) error
}

type GitHubIssueAuthorizationStore interface {
	ListAuthorizedInboxAssignees(ctx context.Context) ([]models.GitHubAuthorizedActor, error)
	GetEnabledProjectInbox(ctx context.Context, projectID string) (*models.GitHubProjectInbox, error)
	IsActorAuthorized(ctx context.Context, githubLogin string) (bool, error)
}

type GitHubIssueActionDecoder func(input json.RawMessage, dst any) error
type GitHubIssueRepoResolver func(ctx context.Context, repoURL string) (*GitHubRepoRef, error)
type GitHubAssignedIssuesPostprocessor func(ctx context.Context, repo *GitHubRepoRef, issues []GitHubIssue) ([]GitHubIssue, error)

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

func (c *GitHubIssueActionCore) ExecuteGetIssue(ctx context.Context, input json.RawMessage) (string, error) {
	req, err := c.request(input)
	if err != nil {
		return "", err
	}
	repo, err := c.resolve(ctx, req.RepoURL)
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

func (c *GitHubIssueActionCore) ExecuteListMyAssignedIssues(ctx context.Context, input json.RawMessage, postprocess GitHubAssignedIssuesPostprocessor) (string, error) {
	req, err := c.request(input)
	if err != nil {
		return "", err
	}
	repo, err := c.resolve(ctx, req.RepoURL)
	if err != nil {
		return "", err
	}
	user, issues, err := c.provider.ListAuthenticatedAssignedIssues(ctx, repo)
	if err != nil {
		return "", err
	}
	if postprocess != nil {
		issues, err = postprocess(ctx, repo, issues)
		if err != nil {
			return "", err
		}
	}
	return githubIssueActionJSON(map[string]any{"ok": true, "account": user, "issues": issues})
}

func (c *GitHubIssueActionCore) ExecuteListAssignedIssues(ctx context.Context, input json.RawMessage, postprocess GitHubAssignedIssuesPostprocessor) (string, error) {
	req, err := c.request(input)
	if err != nil {
		return "", err
	}
	assignee := strings.TrimSpace(req.Assignee)
	if assignee == "" {
		return "", fmt.Errorf("assignee is required")
	}
	repo, err := c.resolve(ctx, req.RepoURL)
	if err != nil {
		return "", err
	}
	issues, err := c.provider.ListAssignedIssues(ctx, repo, assignee)
	if err != nil {
		return "", err
	}
	if postprocess != nil {
		issues, err = postprocess(ctx, repo, issues)
		if err != nil {
			return "", err
		}
	}
	return githubIssueActionJSON(map[string]any{"ok": true, "assignee": repository.NormalizeGitHubLogin(assignee), "issues": issues})
}

func (c *GitHubIssueActionCore) ExecuteListAssignedIssuesWithPRs(ctx context.Context, input json.RawMessage) (string, error) {
	req, err := c.request(input)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(req.Assignee) == "" {
		return "", fmt.Errorf("assignee is required")
	}
	repo, err := c.resolve(ctx, req.RepoURL)
	if err != nil {
		return "", err
	}
	items, err := c.provider.ListAssignedIssuesWithPullRequests(ctx, repo, req.Assignee)
	if err != nil {
		return "", err
	}
	return githubIssueActionJSON(map[string]any{"ok": true, "items": items, "skipped_without_pr": "Assigned issues without an associated pull request are skipped."})
}

func (c *GitHubIssueActionCore) ExecuteCommentOnIssue(ctx context.Context, input json.RawMessage) (string, error) {
	req, err := c.request(input)
	if err != nil {
		return "", err
	}
	repo, err := c.resolve(ctx, req.RepoURL)
	if err != nil {
		return "", err
	}
	if err := c.provider.CommentOnIssue(ctx, repo, req.IssueNumber, req.Body); err != nil {
		return "", err
	}
	return githubIssueActionJSON(map[string]any{"ok": true, "issue_number": req.IssueNumber})
}

func (c *GitHubIssueActionCore) ExecuteAddIssueLabels(ctx context.Context, input json.RawMessage) (string, error) {
	req, err := c.request(input)
	if err != nil {
		return "", err
	}
	repo, err := c.resolve(ctx, req.RepoURL)
	if err != nil {
		return "", err
	}
	if err := c.provider.AddLabelsToIssue(ctx, repo, req.IssueNumber, req.Labels); err != nil {
		return "", err
	}
	return githubIssueActionJSON(map[string]any{"ok": true, "issue_number": req.IssueNumber, "labels": req.Labels})
}

func githubIssueActionJSON(payload map[string]any) (string, error) {
	b, err := json.Marshal(payload)
	return string(b), err
}

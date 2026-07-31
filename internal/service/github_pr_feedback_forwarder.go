package service

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type GitHubPRFeedbackProvider interface {
	GetAuthenticatedUser(ctx context.Context) (*GitHubAuthenticatedUser, error)
	ListPullRequestFeedback(ctx context.Context, repo *GitHubRepoRef, prNumber int) ([]GitHubPullRequestFeedback, error)
}

type GitHubPRFeedbackForwarder struct {
	github          GitHubPRFeedbackProvider
	prRepo          *repository.TaskPullRequestRepo
	feedbackRepo    *repository.GitHubPRFeedbackRepo
	authRepo        *repository.GitHubAuthRepo
	threadInputRepo *repository.ThreadInputRepo
}

type GitHubPRFeedbackForwardResult struct {
	OK                  bool                              `json:"ok"`
	ScannedPullRequests int                               `json:"scanned_pull_requests"`
	Forwarded           []GitHubPRFeedbackForwardedResult `json:"forwarded"`
	SkippedUnauthorized int                               `json:"skipped_unauthorized"`
	SkippedSelfOrBot    int                               `json:"skipped_self_or_bot"`
	SkippedDuplicate    int                               `json:"skipped_duplicate"`
	SkippedEmpty        int                               `json:"skipped_empty"`
}

type GitHubPRFeedbackForwardedResult struct {
	TaskID            string `json:"task_id"`
	PullRequestNumber int    `json:"pull_request_number"`
	FeedbackKind      string `json:"feedback_kind"`
	GitHubID          string `json:"github_id"`
	AuthorLogin       string `json:"author_login"`
	QueuedMessageID   string `json:"queued_message_id"`
	URL               string `json:"url"`
}

func NewGitHubPRFeedbackForwarder(github GitHubPRFeedbackProvider, prRepo *repository.TaskPullRequestRepo, feedbackRepo *repository.GitHubPRFeedbackRepo, authRepo *repository.GitHubAuthRepo, threadInputRepo *repository.ThreadInputRepo) *GitHubPRFeedbackForwarder {
	return &GitHubPRFeedbackForwarder{github: github, prRepo: prRepo, feedbackRepo: feedbackRepo, authRepo: authRepo, threadInputRepo: threadInputRepo}
}

func (f *GitHubPRFeedbackForwarder) ForwardAuthorizedFeedback(ctx context.Context, projectID string, repo *GitHubRepoRef) (*GitHubPRFeedbackForwardResult, error) {
	if f == nil || f.github == nil {
		return nil, fmt.Errorf("github feedback provider unavailable")
	}
	if f.prRepo == nil || f.feedbackRepo == nil || f.authRepo == nil || f.threadInputRepo == nil {
		return nil, fmt.Errorf("github pr feedback forwarding dependencies unavailable")
	}
	if repo == nil {
		return nil, fmt.Errorf("repository reference is required")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	authenticatedUser, err := f.github.GetAuthenticatedUser(ctx)
	if err != nil {
		return nil, err
	}
	selfLogin := ""
	if authenticatedUser != nil {
		selfLogin = repository.NormalizeGitHubLogin(authenticatedUser.Login)
	}
	prs, err := f.prRepo.ListOpenByProjectID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	result := &GitHubPRFeedbackForwardResult{OK: true, ScannedPullRequests: len(prs)}
	for _, pr := range prs {
		if pr.PRNumber <= 0 || strings.TrimSpace(pr.TaskID) == "" || strings.TrimSpace(pr.ID) == "" {
			continue
		}
		prRepo, err := parsePersistedGitHubPullRequestURL(pr.PRURL, pr.PRNumber)
		if err != nil {
			continue
		}
		items, err := f.github.ListPullRequestFeedback(ctx, prRepo, pr.PRNumber)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Kind) == "" {
				continue
			}
			if strings.TrimSpace(item.Body) == "" && strings.TrimSpace(item.State) == "" {
				result.SkippedEmpty++
				continue
			}
			author := repository.NormalizeGitHubLogin(item.AuthorLogin)
			if author == "" {
				result.SkippedUnauthorized++
				continue
			}
			if isGitHubPRFeedbackSelfOrBot(author, item.AuthorType, selfLogin) {
				result.SkippedSelfOrBot++
				continue
			}
			authorized, err := f.authRepo.IsActorAuthorized(ctx, author)
			if err != nil {
				return nil, err
			}
			if !authorized {
				result.SkippedUnauthorized++
				continue
			}
			already, err := f.feedbackRepo.AlreadyForwarded(ctx, prRepo.FullName, pr.PRNumber, item.Kind, item.ID)
			if err != nil {
				return nil, err
			}
			if already {
				result.SkippedDuplicate++
				continue
			}
			queued := &models.ThreadInput{
				Scope:       models.ThreadInputScopeTask,
				ProjectID:   projectID,
				TaskID:      pr.TaskID,
				Content:     formatGitHubPRFeedbackForTask(pr.PRNumber, item),
				Source:      models.TaskOriginSystemAgent,
				OriginAgent: "github-dev-inbox",
			}
			createdAt := item.CreatedAt
			if createdAt.IsZero() {
				createdAt = time.Now().UTC()
			}
			feedbackRecord := &models.GitHubPRFeedbackForwarded{
				TaskPullRequestID: pr.ID,
				TaskID:            pr.TaskID,
				RepoFullName:      prRepo.FullName,
				PRNumber:          pr.PRNumber,
				FeedbackKind:      item.Kind,
				GitHubID:          item.ID,
				GitHubNodeID:      item.NodeID,
				AuthorLogin:       author,
				HTMLURL:           item.URL,
				Body:              item.Body,
				CreatedAt:         createdAt,
			}
			recorded, err := f.feedbackRepo.RecordForwardedAndQueue(ctx, f.threadInputRepo, feedbackRecord, queued)
			if err != nil {
				return nil, err
			}
			if !recorded {
				result.SkippedDuplicate++
				continue
			}
			result.Forwarded = append(result.Forwarded, GitHubPRFeedbackForwardedResult{
				TaskID:            pr.TaskID,
				PullRequestNumber: pr.PRNumber,
				FeedbackKind:      item.Kind,
				GitHubID:          item.ID,
				AuthorLogin:       author,
				QueuedMessageID:   queued.ID,
				URL:               item.URL,
			})
		}
	}
	return result, nil
}

func parsePersistedGitHubPullRequestURL(raw string, expectedPRNumber int) (*GitHubRepoRef, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid pull request URL")
	}
	if !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawPath != "" {
		return nil, fmt.Errorf("unsupported pull request URL")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || !strings.EqualFold(parts[2], "pull") {
		return nil, fmt.Errorf("invalid pull request URL")
	}
	prNumber, err := strconv.Atoi(parts[3])
	if err != nil || prNumber <= 0 || prNumber != expectedPRNumber {
		return nil, fmt.Errorf("pull request URL number does not match persisted number")
	}
	owner := strings.ToLower(strings.TrimSpace(parts[0]))
	repo := strings.ToLower(strings.TrimSpace(parts[1]))
	for _, segment := range []string{owner, repo} {
		if segment == "" || segment == "." || segment == ".." {
			return nil, fmt.Errorf("invalid pull request repository")
		}
		for _, char := range segment {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' && char != '_' && char != '.' {
				return nil, fmt.Errorf("invalid pull request repository")
			}
		}
	}
	return &GitHubRepoRef{
		Owner:    owner,
		Name:     repo,
		FullName: owner + "/" + repo,
		CloneURL: fmt.Sprintf("https://github.com/%s/%s.git", owner, repo),
		HTMLURL:  fmt.Sprintf("https://github.com/%s/%s", owner, repo),
	}, nil
}

func isGitHubPRFeedbackSelfOrBot(authorLogin, authorType, selfLogin string) bool {
	authorLogin = repository.NormalizeGitHubLogin(authorLogin)
	selfLogin = repository.NormalizeGitHubLogin(selfLogin)
	if authorLogin != "" && selfLogin != "" && authorLogin == selfLogin {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(authorType), "Bot")
}

func formatGitHubPRFeedbackForTask(prNumber int, item GitHubPullRequestFeedback) string {
	kind := strings.ReplaceAll(strings.TrimSpace(item.Kind), "_", " ")
	if kind == "" {
		kind = "feedback"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "GitHub PR #%d received authorized %s feedback from @%s.\n", prNumber, kind, repository.NormalizeGitHubLogin(item.AuthorLogin))
	if strings.TrimSpace(item.State) != "" {
		fmt.Fprintf(&b, "Review state: %s\n", strings.TrimSpace(item.State))
	}
	if strings.TrimSpace(item.Path) != "" {
		fmt.Fprintf(&b, "File: %s", strings.TrimSpace(item.Path))
		if item.Line > 0 {
			fmt.Fprintf(&b, ":%d", item.Line)
		}
		b.WriteString("\n")
	}
	if strings.TrimSpace(item.URL) != "" {
		fmt.Fprintf(&b, "GitHub URL: %s\n", strings.TrimSpace(item.URL))
	}
	body := strings.TrimSpace(item.Body)
	if body != "" {
		b.WriteString("\nFeedback:\n")
		b.WriteString(body)
		b.WriteString("\n")
	}
	b.WriteString("\nPlease address this GitHub PR feedback in the task worktree, then update or reuse the PR when ready.")
	return strings.TrimSpace(b.String())
}

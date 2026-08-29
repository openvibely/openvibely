package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type GitHubPRFeedbackRepo struct {
	db *sql.DB
}

func NewGitHubPRFeedbackRepo(db *sql.DB) *GitHubPRFeedbackRepo {
	return &GitHubPRFeedbackRepo{db: db}
}

func (r *GitHubPRFeedbackRepo) AlreadyForwarded(ctx context.Context, repoFullName string, prNumber int, feedbackKind, githubID string) (bool, error) {
	repoFullName = strings.TrimSpace(repoFullName)
	feedbackKind = strings.TrimSpace(feedbackKind)
	githubID = strings.TrimSpace(githubID)
	if repoFullName == "" || prNumber <= 0 || feedbackKind == "" || githubID == "" {
		return false, nil
	}
	var count int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM github_pr_feedback_forwarded WHERE repo_full_name = ? COLLATE NOCASE AND pr_number = ? AND feedback_kind = ? AND github_id = ?`,
		repoFullName, prNumber, feedbackKind, githubID).Scan(&count); err != nil {
		return false, fmt.Errorf("checking github pr feedback forwarded: %w", err)
	}
	return count > 0, nil
}

func (r *GitHubPRFeedbackRepo) RecordForwardedAndQueue(ctx context.Context, threadInputRepo *ThreadInputRepo, feedback *models.GitHubPRFeedbackForwarded, input *models.ThreadInput) (bool, error) {
	if threadInputRepo == nil {
		return false, fmt.Errorf("thread input repository is required")
	}
	if input == nil {
		return false, fmt.Errorf("thread input is required")
	}
	var recorded bool
	err := withImmediateTx(ctx, threadInputRepo.db, func(exec SQLExecutor) error {
		ok, err := r.recordForwardedWithExecutor(ctx, exec, feedback)
		if err != nil || !ok {
			recorded = ok
			return err
		}
		if err := threadInputRepo.CreateQueuedWithExecutor(ctx, exec, input); err != nil {
			return err
		}
		feedback.QueuedThreadInputID = input.ID
		if _, err := exec.ExecContext(ctx, `UPDATE github_pr_feedback_forwarded SET queued_thread_input_id = ? WHERE id = ?`, input.ID, feedback.ID); err != nil {
			return fmt.Errorf("linking github pr feedback queued input: %w", err)
		}
		recorded = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return recorded, nil
}

func (r *GitHubPRFeedbackRepo) RecordForwarded(ctx context.Context, feedback *models.GitHubPRFeedbackForwarded) (recorded bool, err error) {
	err = withBoundSQLiteConn(ctx, r.db, func(conn *sql.Conn) error {
		recorded, err = r.recordForwardedWithExecutor(ctx, conn, feedback)
		return err
	})
	return recorded, err
}

func (r *GitHubPRFeedbackRepo) recordForwardedWithExecutor(ctx context.Context, exec SQLExecutor, feedback *models.GitHubPRFeedbackForwarded) (bool, error) {
	if feedback == nil {
		return false, fmt.Errorf("github pr feedback is nil")
	}
	feedback.TaskPullRequestID = strings.TrimSpace(feedback.TaskPullRequestID)
	feedback.TaskID = strings.TrimSpace(feedback.TaskID)
	feedback.RepoFullName = strings.ToLower(strings.TrimSpace(feedback.RepoFullName))
	feedback.FeedbackKind = strings.TrimSpace(feedback.FeedbackKind)
	feedback.GitHubID = strings.TrimSpace(feedback.GitHubID)
	feedback.AuthorLogin = NormalizeGitHubLogin(feedback.AuthorLogin)
	if feedback.TaskPullRequestID == "" || feedback.TaskID == "" || feedback.RepoFullName == "" || feedback.PRNumber <= 0 || feedback.FeedbackKind == "" || feedback.GitHubID == "" || feedback.AuthorLogin == "" {
		return false, fmt.Errorf("task pull request id, task id, repo, pr number, feedback kind, github id, and author login are required")
	}
	var queuedID any
	if strings.TrimSpace(feedback.QueuedThreadInputID) != "" {
		queuedID = strings.TrimSpace(feedback.QueuedThreadInputID)
	}
	var forwardedAt string
	err := exec.QueryRowContext(ctx,
		`INSERT INTO github_pr_feedback_forwarded (
			 task_pull_request_id, task_id, repo_full_name, pr_number, feedback_kind, github_id,
			 github_node_id, author_login, html_url, body, created_at, queued_thread_input_id
		 ) SELECT ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?, ?
		 WHERE NOT EXISTS (
			 SELECT 1 FROM github_pr_feedback_forwarded
			 WHERE repo_full_name = ? COLLATE NOCASE AND pr_number = ? AND feedback_kind = ? AND github_id = ?
		 )
		 ON CONFLICT(repo_full_name, pr_number, feedback_kind, github_id) DO NOTHING
		 RETURNING id, forwarded_at`,
		feedback.TaskPullRequestID, feedback.TaskID, feedback.RepoFullName, feedback.PRNumber, feedback.FeedbackKind, feedback.GitHubID,
		strings.TrimSpace(feedback.GitHubNodeID), feedback.AuthorLogin, strings.TrimSpace(feedback.HTMLURL), feedback.Body, feedback.CreatedAt.Format("2006-01-02T15:04:05Z07:00"), queuedID,
		feedback.RepoFullName, feedback.PRNumber, feedback.FeedbackKind, feedback.GitHubID).
		Scan(&feedback.ID, &forwardedAt)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("recording github pr feedback forwarded: %w", err)
	}
	if parsed, parseErr := parseGitHubPRFeedbackTime(forwardedAt); parseErr == nil {
		feedback.ForwardedAt = parsed
	}
	return true, nil
}

func parseGitHubPRFeedbackTime(raw string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(raw)); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported sqlite time %q", raw)
}

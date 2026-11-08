package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
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

func (r *GitHubPRFeedbackRepo) RecordForwardedAndQueue(ctx context.Context, feedback *models.GitHubPRFeedbackForwarded, input *models.ThreadInput) (bool, error) {
	if input == nil {
		return false, fmt.Errorf("thread input is required")
	}
	threadInputRepo := NewThreadInputRepo(r.db)
	var recorded bool
	err := withImmediateTx(ctx, r.db, func(exec sqlExecutor) error {
		if feedback == nil || input.Scope != models.ThreadInputScopeTask || input.TaskID != feedback.TaskID {
			return persistence.ErrProjectDenied
		}
		var projectID, taskID, prURL string
		var prNumber int
		if err := exec.QueryRowContext(ctx, `SELECT t.project_id,p.task_id,p.pr_number,p.pr_url FROM task_pull_requests p JOIN tasks t ON t.id=p.task_id WHERE p.id=?`, feedback.TaskPullRequestID).Scan(&projectID, &taskID, &prNumber, &prURL); err != nil {
			return err
		}
		if projectID != input.ProjectID || taskID != input.TaskID || prNumber != feedback.PRNumber {
			return persistence.ErrProjectDenied
		}
		actor, identified := models.PrincipalFromContext(ctx)
		if identified {
			if err := requireAutomationContinuationAuthor(ctx, exec, projectID, actor.UserID); err != nil {
				return err
			}
		}
		if source, captured := models.GitHubFeedbackExecutionFromContext(ctx); captured {
			if source.TaskID == "" || source.ExecutionID == "" {
				return persistence.ErrProjectDenied
			}
			var sourceProject, sourceTask, savedUser string
			if err := exec.QueryRowContext(ctx, `SELECT t.project_id,e.task_id,COALESCE(r.user_id,'') FROM executions e JOIN tasks t ON t.id=e.task_id LEFT JOIN execution_run_identities r ON r.execution_id=e.id WHERE e.id=?`, source.ExecutionID).Scan(&sourceProject, &sourceTask, &savedUser); err != nil {
				if err == sql.ErrNoRows {
					return persistence.ErrProjectDenied
				}
				return err
			}
			if sourceProject != projectID || sourceTask != source.TaskID || (savedUser != "" && (!identified || savedUser != actor.UserID)) || (source.RequireIdentity && savedUser == "") {
				return persistence.ErrProjectDenied
			}
			if err := checkExecutionGoal(ctx, exec, source.ExecutionID, source.TaskID); err != nil {
				return err
			}
			// Copy database-owned sender evidence into the queued input snapshot,
			// even when recovery no longer has the original channel context.
			var sender models.SlackIngressIdentity
			err := exec.QueryRowContext(ctx, `SELECT project_id,team_id,external_user_id,user_id FROM slack_execution_provenance WHERE execution_id=?`, source.ExecutionID).Scan(&sender.ProjectID, &sender.TeamID, &sender.ExternalUserID, &sender.UserID)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if err == nil {
				if supplied, ok := models.SlackIngressIdentityFromContext(ctx); ok && (supplied.ProjectID != sender.ProjectID || supplied.TeamID != sender.TeamID || supplied.ExternalUserID != sender.ExternalUserID || supplied.UserID != sender.UserID) {
					return persistence.ErrProjectDenied
				}
				ctx = models.WithSlackIngressIdentity(ctx, sender)
			}
		}
		var reviewerAllowed bool
		if err := exec.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM github_authorized_actors WHERE lower(github_login)=?)`, persistence.NormalizeGitHubLogin(feedback.AuthorLogin)).Scan(&reviewerAllowed); err != nil {
			return err
		}
		if !reviewerAllowed {
			return persistence.ErrProjectDenied
		}
		var reviewer *models.GitHubReviewerIdentity
		source, _ := models.GitHubFeedbackExecutionFromContext(ctx)
		if source.RequireIdentity {
			parsed, err := url.Parse(prURL)
			if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, feedback.ProviderHost) {
				return persistence.ErrProjectDenied
			}

			reviewer, err = resolveReviewerIdentity(ctx, exec, projectID, feedback.ProviderHost, feedback.AuthorUserID, feedback.AuthorLogin)
			if err != nil {
				return err
			}
			if _, err = authorizeExecutionActor(ctx, exec, taskID, reviewer.ApplicationUserID); err != nil {
				return err
			}
		}
		ok, err := r.recordForwardedWithExecutor(ctx, exec, feedback)
		if err != nil || !ok {
			recorded = ok
			return err
		}
		if err := threadInputRepo.createQueuedWithExecutor(ctx, exec, input); err != nil {
			return err
		}
		if _, err := exec.ExecContext(ctx, `INSERT INTO github_reviewer_input_provenance(input_id,reviewer_login) VALUES(?,?)`, input.ID, persistence.NormalizeGitHubLogin(feedback.AuthorLogin)); err != nil {
			return err
		}
		if reviewer != nil {
			if _, err := exec.ExecContext(ctx, `INSERT INTO github_reviewer_input_identities(input_id,project_id,actor_id,host,external_user_id,application_user_id) VALUES(?,?,?,?,?,?)`, input.ID, reviewer.ProjectID, reviewer.ActorID, reviewer.Host, reviewer.ExternalUserID, reviewer.ApplicationUserID); err != nil {
				return err
			}
		}
		feedback.QueuedThreadInputID = input.ID
		if _, err := exec.ExecContext(ctx, `UPDATE github_pr_feedback_forwarded SET queued_thread_input_id = ? WHERE id = ?`, input.ID, feedback.ID); err != nil {
			return fmt.Errorf("linking github pr feedback queued input: %w", err)
		}
		if identified {
			if err := projectAudit(ctx, exec, projectID, actor.UserID, "github.feedback_queued", input.ID, `{}`); err != nil {
				return err
			}
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

func (r *GitHubPRFeedbackRepo) recordForwardedWithExecutor(ctx context.Context, exec sqlExecutor, feedback *models.GitHubPRFeedbackForwarded) (bool, error) {
	if feedback == nil {
		return false, fmt.Errorf("github pr feedback is nil")
	}
	feedback.TaskPullRequestID = strings.TrimSpace(feedback.TaskPullRequestID)
	feedback.TaskID = strings.TrimSpace(feedback.TaskID)
	feedback.RepoFullName = strings.ToLower(strings.TrimSpace(feedback.RepoFullName))
	feedback.FeedbackKind = strings.TrimSpace(feedback.FeedbackKind)
	feedback.GitHubID = strings.TrimSpace(feedback.GitHubID)
	feedback.AuthorLogin = persistence.NormalizeGitHubLogin(feedback.AuthorLogin)
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

package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

type TaskPullRequestRepo struct {
	db *sql.DB
}

func NewTaskPullRequestRepo(db *sql.DB) *TaskPullRequestRepo {
	return &TaskPullRequestRepo{db: db}
}

func (r *TaskPullRequestRepo) GetByTaskID(ctx context.Context, taskID string) (*models.TaskPullRequest, error) {
	return r.getOne(ctx,
		`SELECT id, task_id, pr_number, pr_url, pr_state, published_head_sha, issue_number, issue_url, created_at, updated_at
			 FROM task_pull_requests WHERE task_id = ?`, taskID)
}

func (r *TaskPullRequestRepo) GetByIssueNumber(ctx context.Context, issueNumber int) (*models.TaskPullRequest, error) {
	if issueNumber <= 0 {
		return nil, nil
	}
	return r.getOne(ctx,
		`SELECT id, task_id, pr_number, pr_url, pr_state, published_head_sha, issue_number, issue_url, created_at, updated_at
			 FROM task_pull_requests WHERE issue_number = ? ORDER BY updated_at DESC, created_at DESC LIMIT 1`, issueNumber)
}

func (r *TaskPullRequestRepo) ListOpenByProjectID(ctx context.Context, projectID string) ([]models.TaskPullRequest, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT pr.id, pr.task_id, pr.pr_number, pr.pr_url, pr.pr_state, pr.published_head_sha, pr.issue_number, pr.issue_url, pr.created_at, pr.updated_at
			 FROM task_pull_requests pr
			 JOIN tasks t ON t.id = pr.task_id
			 WHERE t.project_id = ? AND lower(pr.pr_state) = 'open'
			 ORDER BY pr.updated_at DESC, pr.created_at DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing task pull requests by project: %w", err)
	}
	defer rows.Close()

	var prs []models.TaskPullRequest
	for rows.Next() {
		var pr models.TaskPullRequest
		if err := rows.Scan(&pr.ID, &pr.TaskID, &pr.PRNumber, &pr.PRURL, &pr.PRState, &pr.PublishedHeadSHA, &pr.IssueNumber, &pr.IssueURL, &pr.CreatedAt, &pr.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning task pull request: %w", err)
		}
		prs = append(prs, pr)
	}
	return prs, rows.Err()
}

func (r *TaskPullRequestRepo) getOne(ctx context.Context, query string, args ...any) (*models.TaskPullRequest, error) {
	var pr models.TaskPullRequest
	err := r.db.QueryRowContext(ctx, query, args...).
		Scan(&pr.ID, &pr.TaskID, &pr.PRNumber, &pr.PRURL, &pr.PRState, &pr.PublishedHeadSHA, &pr.IssueNumber, &pr.IssueURL, &pr.CreatedAt, &pr.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting task pull request: %w", err)
	}
	return &pr, nil
}

func (r *TaskPullRequestRepo) Upsert(ctx context.Context, pr *models.TaskPullRequest) error {
	if pr == nil {
		return fmt.Errorf("task pull request is nil")
	}
	return queryRowBoundSQLite(ctx, r.db,
		`INSERT INTO task_pull_requests (id, task_id, pr_number, pr_url, pr_state, published_head_sha, issue_number, issue_url)
				 VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT(task_id) DO UPDATE SET
					pr_number = excluded.pr_number,
					pr_url = excluded.pr_url,
					pr_state = excluded.pr_state,
					published_head_sha = excluded.published_head_sha,
					issue_number = excluded.issue_number,
					issue_url = excluded.issue_url,
					updated_at = datetime('now')
				 RETURNING id, created_at, updated_at`,
		pr.TaskID, pr.PRNumber, pr.PRURL, pr.PRState, pr.PublishedHeadSHA, pr.IssueNumber, pr.IssueURL).Scan(&pr.ID, &pr.CreatedAt, &pr.UpdatedAt)
}

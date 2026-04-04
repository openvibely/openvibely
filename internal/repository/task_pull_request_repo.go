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
	var pr models.TaskPullRequest
	err := r.db.QueryRowContext(ctx,
		`SELECT id, task_id, pr_number, pr_url, pr_state, created_at, updated_at
		 FROM task_pull_requests WHERE task_id = ?`, taskID).
		Scan(&pr.ID, &pr.TaskID, &pr.PRNumber, &pr.PRURL, &pr.PRState, &pr.CreatedAt, &pr.UpdatedAt)
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
	return r.db.QueryRowContext(ctx,
		`INSERT INTO task_pull_requests (id, task_id, pr_number, pr_url, pr_state)
		 VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?)
		 ON CONFLICT(task_id) DO UPDATE SET
			pr_number = excluded.pr_number,
			pr_url = excluded.pr_url,
			pr_state = excluded.pr_state,
			updated_at = datetime('now')
		 RETURNING id, created_at, updated_at`,
		pr.TaskID, pr.PRNumber, pr.PRURL, pr.PRState).
		Scan(&pr.ID, &pr.CreatedAt, &pr.UpdatedAt)
}

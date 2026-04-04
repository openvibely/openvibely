package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

// ReviewCommentRepo handles database operations for code review comments.
type ReviewCommentRepo struct {
	db *sql.DB
}

// NewReviewCommentRepo creates a new ReviewCommentRepo.
func NewReviewCommentRepo(db *sql.DB) *ReviewCommentRepo {
	return &ReviewCommentRepo{db: db}
}

// Create adds a new review comment.
func (r *ReviewCommentRepo) Create(ctx context.Context, c *models.ReviewComment) error {
	return r.db.QueryRowContext(ctx,
		`INSERT INTO review_comments (task_id, file_path, line_number, line_type, comment_text, reviewed_by)
		 VALUES (?, ?, ?, ?, ?, ?)
		 RETURNING id, created_at`,
		c.TaskID, c.FilePath, c.LineNumber, c.LineType, c.CommentText, c.ReviewedBy).
		Scan(&c.ID, &c.CreatedAt)
}

// ListByTask returns all review comments for a task, ordered by file and line number.
func (r *ReviewCommentRepo) ListByTask(ctx context.Context, taskID string) ([]models.ReviewComment, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, task_id, file_path, line_number, line_type, comment_text, reviewed_by, created_at
		 FROM review_comments
		 WHERE task_id = ?
		 ORDER BY file_path ASC, line_number ASC, created_at ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list review comments: %w", err)
	}
	defer rows.Close()

	var comments []models.ReviewComment
	for rows.Next() {
		var c models.ReviewComment
		if err := rows.Scan(&c.ID, &c.TaskID, &c.FilePath, &c.LineNumber, &c.LineType, &c.CommentText, &c.ReviewedBy, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan review comment: %w", err)
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

// CountByTask returns the number of review comments for a task.
func (r *ReviewCommentRepo) CountByTask(ctx context.Context, taskID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM review_comments WHERE task_id = ?`, taskID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count review comments: %w", err)
	}
	return count, nil
}

// UpdateText updates the comment text of a review comment by ID.
func (r *ReviewCommentRepo) UpdateText(ctx context.Context, id, commentText string) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE review_comments SET comment_text = ? WHERE id = ?`, commentText, id)
	if err != nil {
		return fmt.Errorf("update review comment: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("review comment not found")
	}
	return nil
}

// Delete removes a review comment by ID.
func (r *ReviewCommentRepo) Delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM review_comments WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete review comment: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("review comment not found")
	}
	return nil
}

// DeleteByTask removes all review comments for a task.
func (r *ReviewCommentRepo) DeleteByTask(ctx context.Context, taskID string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM review_comments WHERE task_id = ?`, taskID)
	if err != nil {
		return fmt.Errorf("delete review comments by task: %w", err)
	}
	return nil
}

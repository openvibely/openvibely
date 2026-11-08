package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
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
	saved := *c
	err := r.mutateTaskComments(ctx, c.TaskID, "review.comment.created", func(tx sqlExecutor) (string, error) {
		err := tx.QueryRowContext(ctx, `INSERT INTO review_comments (task_id,file_path,line_number,line_type,comment_text,reviewed_by) VALUES(?,?,?,?,?,?) RETURNING id,created_at`, c.TaskID, c.FilePath, c.LineNumber, c.LineType, c.CommentText, c.ReviewedBy).Scan(&saved.ID, &saved.CreatedAt)
		return saved.ID, err
	})
	if err == nil {
		*c = saved
	}
	return err
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
	result, err := execBoundSQLite(ctx, r.db,
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
	result, err := execBoundSQLite(ctx, r.db,
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
	return r.mutateTaskComments(ctx, taskID, "review.comments.cleared", func(tx sqlExecutor) (string, error) {
		_, err := tx.ExecContext(ctx, `DELETE FROM review_comments WHERE task_id=?`, taskID)
		return taskID, err
	})
}

// UpdateTextForProject updates only comments owned by the request project.
func (r *ReviewCommentRepo) UpdateTextForProject(ctx context.Context, projectID, id, text string) error {
	return r.mutateProjectComment(ctx, projectID, id, "review.comment.updated", func(tx sqlExecutor) error {
		result, err := tx.ExecContext(ctx, `UPDATE review_comments SET comment_text = ? WHERE id = ? AND task_id IN (SELECT id FROM tasks WHERE project_id = ?)`, text, id, projectID)
		return reviewCommentMutationResult(result, err)
	})
}

// DeleteForProject deletes only comments owned by the request project.
func (r *ReviewCommentRepo) DeleteForProject(ctx context.Context, projectID, id string) error {
	return r.mutateProjectComment(ctx, projectID, id, "review.comment.deleted", func(tx sqlExecutor) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM review_comments WHERE id = ? AND task_id IN (SELECT id FROM tasks WHERE project_id = ?)`, id, projectID)
		return reviewCommentMutationResult(result, err)
	})
}

func reviewCommentMutationResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return persistence.ErrNotFound
	}
	return nil
}

func (r *ReviewCommentRepo) mutateProjectComment(ctx context.Context, projectID, id, action string, mutate func(sqlExecutor) error) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		var actualProject string
		err := tx.QueryRowContext(ctx, `SELECT t.project_id FROM review_comments c JOIN tasks t ON t.id=c.task_id WHERE c.id=?`, id).Scan(&actualProject)
		if errors.Is(err, sql.ErrNoRows) {
			return persistence.ErrNotFound
		}
		if err != nil {
			return err
		}
		if actualProject != projectID {
			return persistence.ErrNotFound
		}
		actor, identified := models.PrincipalFromContext(ctx)
		if identified {
			role, err := projectRole(ctx, tx, actor.UserID, projectID)
			if err != nil {
				return err
			}
			if role != "editor" && role != "maintainer" && role != "owner" {
				return persistence.ErrProjectDenied
			}
		}
		if err := mutate(tx); err != nil {
			return err
		}
		if identified {
			return projectAudit(ctx, tx, projectID, actor.UserID, action, id, `{}`)
		}
		return nil
	})
}

func (r *ReviewCommentRepo) mutateTaskComments(ctx context.Context, taskID, action string, mutate func(sqlExecutor) (string, error)) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		actor, identified := models.PrincipalFromContext(ctx)
		var projectID string
		if identified {
			err := tx.QueryRowContext(ctx, `SELECT project_id FROM tasks WHERE id=?`, taskID).Scan(&projectID)
			if errors.Is(err, sql.ErrNoRows) {
				return persistence.ErrNotFound
			}
			if err != nil {
				return err
			}
			role, err := projectRole(ctx, tx, actor.UserID, projectID)
			if err != nil {
				return err
			}
			if role != "editor" && role != "maintainer" && role != "owner" {
				return persistence.ErrProjectDenied
			}
		}
		target, err := mutate(tx)
		if err != nil {
			return err
		}
		if identified {
			return projectAudit(ctx, tx, projectID, actor.UserID, action, target, `{}`)
		}
		return nil
	})
}

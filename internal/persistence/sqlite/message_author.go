package sqlite

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
)

// Keep attribution as a snapshot: removing a member does not rename old messages.
func resolveExecutionAuthor(ctx context.Context, db sqlExecutor, e *models.Execution) error {
	if e.AuthorUserID != "" {
		return nil
	}
	if !e.IsFollowup {
		if err := db.QueryRowContext(ctx, `SELECT author_user_id, author_name FROM tasks WHERE id=?`, e.TaskID).Scan(&e.AuthorUserID, &e.AuthorName); err != nil {
			return err
		}
		return nil
	}
	if p, ok := models.PrincipalFromContext(ctx); ok {
		e.AuthorUserID, e.AuthorName = p.UserID, p.Username
	}
	return nil
}
func saveInputAuthor(ctx context.Context, db sqlExecutor, input *models.ThreadInput) error {
	if p, ok := models.PrincipalFromContext(ctx); ok {
		input.AuthorUserID, input.AuthorName = p.UserID, p.Username
		_, err := db.ExecContext(ctx, `UPDATE thread_inputs SET author_user_id=?, author_name=? WHERE id=?`, input.AuthorUserID, input.AuthorName, input.ID)
		return err
	}
	return nil
}

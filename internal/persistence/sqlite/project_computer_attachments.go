package sqlite

import (
	"context"
	"database/sql"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
)

func (r *ProjectComputerRepo) ThreadAttachments(ctx context.Context, projectID string) ([]models.ChatAttachment, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT a.id,a.execution_id,a.file_name,a.file_path,a.media_type,a.file_size,a.created_at FROM chat_attachments a JOIN executions e ON e.id=a.execution_id JOIN tasks t ON t.id=e.task_id WHERE t.id=? AND t.project_id=? ORDER BY a.created_at DESC LIMIT 200`, r.taskID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.ChatAttachment{}
	for rows.Next() {
		var a models.ChatAttachment
		if err := rows.Scan(&a.ID, &a.ExecutionID, &a.FileName, &a.FilePath, &a.MediaType, &a.FileSize, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (r *ProjectComputerRepo) ThreadAttachment(ctx context.Context, projectID, attachmentID string) (*models.ChatAttachment, error) {
	var a models.ChatAttachment
	err := r.db.QueryRowContext(ctx, `SELECT a.id,a.execution_id,a.file_name,a.file_path,a.media_type,a.file_size,a.created_at FROM chat_attachments a JOIN executions e ON e.id=a.execution_id JOIN tasks t ON t.id=e.task_id WHERE t.id=? AND t.project_id=? AND a.id=?`, r.taskID, projectID, attachmentID).Scan(&a.ID, &a.ExecutionID, &a.FileName, &a.FilePath, &a.MediaType, &a.FileSize, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &a, err
}

// CreateThreadAttachment selects storage from the persisted conversation category.
// Execution ownership and insertion are checked in the same transaction.
func (r *ProjectComputerRepo) CreateThreadAttachment(ctx context.Context, projectID string, a *models.ChatAttachment) (bool, error) {
	var taskAttachment bool
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		var category string
		if err := tx.QueryRowContext(ctx, `SELECT t.category FROM executions e JOIN tasks t ON t.id=e.task_id WHERE e.id=? AND t.id=? AND t.project_id=? AND e.status='running'`, a.ExecutionID, r.taskID, projectID).Scan(&category); err != nil {
			return err
		}
		taskAttachment = category != string(models.CategoryChat)
		if taskAttachment {
			att := &models.Attachment{TaskID: r.taskID, ExecutionID: a.ExecutionID, FileName: a.FileName, FilePath: a.FilePath, MediaType: a.MediaType, FileSize: a.FileSize}
			if err := createAttachmentWithExecutor(ctx, tx, att); err != nil {
				return err
			}
			a.ID, a.CreatedAt = att.ID, att.CreatedAt
			return nil
		}
		_, err := createChatAttachmentWithExecutor(ctx, tx, a)
		return err
	})
	if err == nil {
		key := r.taskID
		if !taskAttachment {
			key = attachmentcontext.ChatProjectKey(projectID)
		}
		attachmentcontext.Invalidate(key)
	}
	return taskAttachment, err
}

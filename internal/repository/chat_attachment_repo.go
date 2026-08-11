package repository

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/openvibely/openvibely/internal/models"
)

type ChatAttachmentRepo struct {
	db *sql.DB
}

func NewChatAttachmentRepo(db *sql.DB) *ChatAttachmentRepo {
	return &ChatAttachmentRepo{db: db}
}

func (r *ChatAttachmentRepo) Create(ctx context.Context, att *models.ChatAttachment) error {
	return r.CreateWithExecutor(ctx, r.db, att)
}

// CreateWithExecutor persists a chat attachment using the caller's transaction.
func (r *ChatAttachmentRepo) CreateWithExecutor(ctx context.Context, exec SQLExecutor, att *models.ChatAttachment) error {
	query := `
			INSERT INTO chat_attachments (execution_id, file_name, file_path, media_type, file_size)
			VALUES (?, ?, ?, ?, ?)
			RETURNING id, created_at
		`
	err := exec.QueryRowContext(ctx, query, att.ExecutionID,
		att.FileName,
		att.FilePath,
		att.MediaType,
		att.FileSize,
	).Scan(&att.ID, &att.CreatedAt)
	if err != nil {
		return fmt.Errorf("creating chat attachment: %w", err)
	}
	return nil
}

func (r *ChatAttachmentRepo) GetByID(ctx context.Context, id string) (*models.ChatAttachment, error) {
	query := `
		SELECT id, execution_id, file_name, file_path, media_type, file_size, created_at
		FROM chat_attachments
		WHERE id = ?
	`
	var att models.ChatAttachment
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&att.ID,
		&att.ExecutionID,
		&att.FileName,
		&att.FilePath,
		&att.MediaType,
		&att.FileSize,
		&att.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting chat attachment: %w", err)
	}
	return &att, nil
}

func (r *ChatAttachmentRepo) ListByExecution(ctx context.Context, executionID string) ([]models.ChatAttachment, error) {
	query := `
		SELECT id, execution_id, file_name, file_path, media_type, file_size, created_at
		FROM chat_attachments
		WHERE execution_id = ?
		ORDER BY created_at ASC
	`
	rows, err := r.db.QueryContext(ctx, query, executionID)
	if err != nil {
		return nil, fmt.Errorf("listing chat attachments: %w", err)
	}
	defer rows.Close()

	var attachments []models.ChatAttachment
	for rows.Next() {
		var att models.ChatAttachment
		if err := rows.Scan(
			&att.ID,
			&att.ExecutionID,
			&att.FileName,
			&att.FilePath,
			&att.MediaType,
			&att.FileSize,
			&att.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning chat attachment: %w", err)
		}
		attachments = append(attachments, att)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating chat attachments: %w", err)
	}
	return attachments, nil
}

func (r *ChatAttachmentRepo) ListByExecutionIDs(ctx context.Context, execIDs []string) (map[string][]models.ChatAttachment, error) {
	if len(execIDs) == 0 {
		return map[string][]models.ChatAttachment{}, nil
	}
	placeholders := make([]byte, 0, len(execIDs)*2-1)
	args := make([]interface{}, len(execIDs))
	for i, id := range execIDs {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, execution_id, file_name, file_path, media_type, file_size, created_at
		 FROM chat_attachments WHERE execution_id IN (`+string(placeholders)+`) ORDER BY created_at ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("batch listing chat attachments: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]models.ChatAttachment, len(execIDs))
	for rows.Next() {
		var att models.ChatAttachment
		if err := rows.Scan(
			&att.ID,
			&att.ExecutionID,
			&att.FileName,
			&att.FilePath,
			&att.MediaType,
			&att.FileSize,
			&att.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning chat attachment: %w", err)
		}
		result[att.ExecutionID] = append(result[att.ExecutionID], att)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating chat attachments: %w", err)
	}
	return result, nil
}

func (r *ChatAttachmentRepo) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM chat_attachments WHERE id = ?`
	result, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("deleting chat attachment: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("chat attachment not found")
	}
	return nil
}

func (r *ChatAttachmentRepo) DeleteByExecution(ctx context.Context, executionID string) error {
	query := `DELETE FROM chat_attachments WHERE execution_id = ?`
	_, err := r.db.ExecContext(ctx, query, executionID)
	if err != nil {
		return fmt.Errorf("deleting chat attachments by execution: %w", err)
	}
	return nil
}

// GetAllFilePaths returns all file paths currently in the database
func (r *ChatAttachmentRepo) GetAllFilePaths(ctx context.Context) ([]string, error) {
	query := `SELECT file_path FROM chat_attachments ORDER BY file_path`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("querying file paths: %w", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("scanning file path: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating file paths: %w", err)
	}
	return paths, nil
}

// CleanupOrphanedFiles removes chat attachment files from disk that no longer have database records
func (r *ChatAttachmentRepo) CleanupOrphanedFiles(ctx context.Context, uploadsDir string) (int, error) {
	dbPaths, err := r.GetAllFilePaths(ctx)
	if err != nil {
		return 0, fmt.Errorf("getting file paths: %w", err)
	}

	return cleanupOrphanedAttachmentFiles(uploadsDir, dbPaths, orphanedAttachmentCleanupOptions{
		walkError: "walking chat uploads directory",
		scope: func(uploadsDir string) orphanedAttachmentCleanupScope {
			chatUploadsDir := filepath.Join(uploadsDir, "chat")
			return orphanedAttachmentCleanupScope{
				walkDir:  chatUploadsDir,
				pruneDir: chatUploadsDir,
			}
		},
	})
}

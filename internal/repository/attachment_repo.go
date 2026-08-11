package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/openvibely/openvibely/internal/models"
)

type AttachmentRepo struct {
	db *sql.DB
}

func NewAttachmentRepo(db *sql.DB) *AttachmentRepo {
	return &AttachmentRepo{db: db}
}

func (r *AttachmentRepo) Create(ctx context.Context, att *models.Attachment) error {
	query := `
		INSERT INTO task_attachments (task_id, file_name, file_path, media_type, file_size)
		VALUES (?, ?, ?, ?, ?)
		RETURNING id, created_at
	`
	err := r.db.QueryRowContext(ctx, query,
		att.TaskID,
		att.FileName,
		att.FilePath,
		att.MediaType,
		att.FileSize,
	).Scan(&att.ID, &att.CreatedAt)
	if err != nil {
		return fmt.Errorf("creating attachment: %w", err)
	}
	return nil
}

func (r *AttachmentRepo) GetByID(ctx context.Context, id string) (*models.Attachment, error) {
	query := `
		SELECT id, task_id, file_name, file_path, media_type, file_size, created_at
		FROM task_attachments
		WHERE id = ?
	`
	var att models.Attachment
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&att.ID,
		&att.TaskID,
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
		return nil, fmt.Errorf("getting attachment: %w", err)
	}
	return &att, nil
}

func (r *AttachmentRepo) ListByTask(ctx context.Context, taskID string) ([]models.Attachment, error) {
	query := `
		SELECT id, task_id, file_name, file_path, media_type, file_size, created_at
		FROM task_attachments
		WHERE task_id = ?
		ORDER BY created_at ASC
	`
	rows, err := r.db.QueryContext(ctx, query, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing attachments: %w", err)
	}
	defer rows.Close()

	var attachments []models.Attachment
	for rows.Next() {
		var att models.Attachment
		if err := rows.Scan(
			&att.ID,
			&att.TaskID,
			&att.FileName,
			&att.FilePath,
			&att.MediaType,
			&att.FileSize,
			&att.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning attachment: %w", err)
		}
		attachments = append(attachments, att)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating attachments: %w", err)
	}
	return attachments, nil
}

func (r *AttachmentRepo) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM task_attachments WHERE id = ?`
	result, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("deleting attachment: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("attachment not found")
	}
	return nil
}

func (r *AttachmentRepo) DeleteByTask(ctx context.Context, taskID string) error {
	query := `DELETE FROM task_attachments WHERE task_id = ?`
	_, err := r.db.ExecContext(ctx, query, taskID)
	if err != nil {
		return fmt.Errorf("deleting task attachments: %w", err)
	}
	return nil
}

// GetAllFilePaths returns all file paths currently in the database
func (r *AttachmentRepo) GetAllFilePaths(ctx context.Context) ([]string, error) {
	query := `SELECT file_path FROM task_attachments ORDER BY file_path`
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

// CleanupOrphanedFiles removes attachment files from disk that no longer have database records
func (r *AttachmentRepo) CleanupOrphanedFiles(ctx context.Context, uploadsDir string) (int, error) {
	dbPaths, err := r.GetAllFilePaths(ctx)
	if err != nil {
		return 0, fmt.Errorf("getting file paths: %w", err)
	}

	return cleanupOrphanedAttachmentFiles(uploadsDir, dbPaths, orphanedAttachmentCleanupOptions{
		walkError: "walking uploads directory",
		scope: func(uploadsDir string) orphanedAttachmentCleanupScope {
			chatUploadsDir := filepath.Join(uploadsDir, "chat")
			return orphanedAttachmentCleanupScope{
				walkDir:  uploadsDir,
				pruneDir: uploadsDir,
				skipWalkDir: func(path string) bool {
					// Chat attachments are cleaned separately by ChatAttachmentRepo.
					return filepath.Clean(path) == chatUploadsDir
				},
				skipPruneDir: func(name string) bool {
					return name == "chat"
				},
			}
		},
	})
}

type orphanedAttachmentCleanupScope struct {
	walkDir      string
	pruneDir     string
	skipWalkDir  func(path string) bool
	skipPruneDir func(name string) bool
}

type orphanedAttachmentCleanupOptions struct {
	walkError string
	scope     func(uploadsDir string) orphanedAttachmentCleanupScope
}

func cleanupOrphanedAttachmentFiles(uploadsDir string, dbPaths []string, opts orphanedAttachmentCleanupOptions) (int, error) {
	normalizedUploadsDir := normalizeAttachmentPath(uploadsDir)

	dbPathSet := make(map[string]bool)
	for _, path := range dbPaths {
		dbPathSet[normalizeAttachmentPath(path)] = true
	}

	scope := opts.scope(normalizedUploadsDir)
	if _, err := os.Stat(scope.walkDir); os.IsNotExist(err) {
		return 0, nil // Nothing to clean up
	}

	deletedCount := 0
	err := filepath.Walk(scope.walkDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			if scope.skipWalkDir != nil && scope.skipWalkDir(path) {
				return filepath.SkipDir
			}
			return nil
		}

		if !dbPathSet[normalizeAttachmentPath(path)] {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("removing orphaned file %s: %w", path, err)
			}
			deletedCount++
		}

		return nil
	})
	if err != nil {
		return deletedCount, fmt.Errorf("%s: %w", opts.walkError, err)
	}

	if err := cleanupEmptyAttachmentDirs(scope.pruneDir, scope.skipPruneDir); err != nil {
		return deletedCount, fmt.Errorf("cleaning up empty directories: %w", err)
	}

	return deletedCount, nil
}

func cleanupEmptyAttachmentDirs(rootDir string, skipDir func(name string) bool) error {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if skipDir != nil && skipDir(entry.Name()) {
			continue
		}

		dirPath := filepath.Join(rootDir, entry.Name())
		subEntries, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}

		if len(subEntries) == 0 {
			if err := os.Remove(dirPath); err != nil {
				return fmt.Errorf("removing empty directory %s: %w", dirPath, err)
			}
		}
	}

	return nil
}

func normalizeAttachmentPath(path string) string {
	if absPath, err := filepath.Abs(path); err == nil {
		return filepath.Clean(absPath)
	}
	return filepath.Clean(path)
}

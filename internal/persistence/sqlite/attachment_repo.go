package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
)

type AttachmentRepo struct {
	db *sql.DB
}

func NewAttachmentRepo(db *sql.DB) *AttachmentRepo {
	return &AttachmentRepo{db: db}
}

func (r *AttachmentRepo) Create(ctx context.Context, att *models.Attachment) error {
	if err := withBoundSQLiteConn(ctx, r.db, func(conn *sql.Conn) error {
		return createAttachmentWithExecutor(ctx, conn, att)
	}); err != nil {
		return err
	}
	attachmentcontext.Invalidate(att.TaskID)
	return nil
}

func createAttachmentWithExecutor(ctx context.Context, exec sqlExecutor, att *models.Attachment) error {
	query := `
		INSERT INTO task_attachments (task_id, execution_id, file_name, file_path, media_type, file_size)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id, created_at
	`
	err := exec.QueryRowContext(ctx, query,
		att.TaskID,
		nullableString(att.ExecutionID),
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

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

const attachmentColumns = `a.id, a.task_id, COALESCE(a.execution_id, ''), a.file_name, a.file_path, a.media_type, a.file_size, a.created_at`

type attachmentScanner interface {
	Scan(dest ...any) error
}

func scanAttachment(row attachmentScanner, att *models.Attachment) error {
	return row.Scan(
		&att.ID,
		&att.TaskID,
		&att.ExecutionID,
		&att.FileName,
		&att.FilePath,
		&att.MediaType,
		&att.FileSize,
		&att.CreatedAt,
	)
}

func (r *AttachmentRepo) GetByID(ctx context.Context, id string) (*models.Attachment, error) {
	query := `SELECT ` + attachmentColumns + ` FROM task_attachments a WHERE a.id = ?`
	return r.getByQuery(ctx, query, id)
}

func (r *AttachmentRepo) GetByIDForProject(ctx context.Context, id, projectID string) (*models.Attachment, error) {
	query := `
			SELECT ` + attachmentColumns + `
			FROM task_attachments a
			JOIN tasks t ON t.id = a.task_id
			WHERE a.id = ? AND t.project_id = ?
		`
	return r.getByQuery(ctx, query, id, projectID)
}

func (r *AttachmentRepo) getByQuery(ctx context.Context, query string, args ...any) (*models.Attachment, error) {
	var att models.Attachment
	err := scanAttachment(r.db.QueryRowContext(ctx, query, args...), &att)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting attachment: %w", err)
	}
	return &att, nil
}

func (r *AttachmentRepo) listByQuery(ctx context.Context, query string, args ...any) ([]models.Attachment, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing attachments: %w", err)
	}
	defer rows.Close()

	var attachments []models.Attachment
	for rows.Next() {
		var att models.Attachment
		if err := scanAttachment(rows, &att); err != nil {
			return nil, fmt.Errorf("scanning attachment: %w", err)
		}
		attachments = append(attachments, att)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating attachments: %w", err)
	}
	return attachments, nil
}

// ListByTask returns every attachment of the task, including files attached to
// individual task-thread messages.
func (r *AttachmentRepo) ListByTask(ctx context.Context, taskID string) ([]models.Attachment, error) {
	return r.listByQuery(ctx, `
		SELECT `+attachmentColumns+`
		FROM task_attachments a
		WHERE a.task_id = ?
		ORDER BY a.created_at ASC, a.rowid ASC
	`, taskID)
}

// ListByExecutionIDs groups task-thread message attachments by the execution
// (message) that attached them.
func (r *AttachmentRepo) ListByExecutionIDs(ctx context.Context, execIDs []string) (map[string][]models.Attachment, error) {
	result := make(map[string][]models.Attachment, len(execIDs))
	if len(execIDs) == 0 {
		return result, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(execIDs)), ",")
	args := make([]any, len(execIDs))
	for i, id := range execIDs {
		args[i] = id
	}
	attachments, err := r.listByQuery(ctx, `
		SELECT `+attachmentColumns+`
		FROM task_attachments a
		WHERE a.execution_id IN (`+placeholders+`)
		ORDER BY a.created_at ASC, a.rowid ASC
	`, args...)
	if err != nil {
		return nil, err
	}
	for _, att := range attachments {
		result[att.ExecutionID] = append(result[att.ExecutionID], att)
	}
	return result, nil
}

func (r *AttachmentRepo) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM task_attachments WHERE id = ? RETURNING task_id`
	return r.deleteByQuery(ctx, query, id)
}

func (r *AttachmentRepo) DeleteByIDForProject(ctx context.Context, id, projectID string) error {
	query := `
		DELETE FROM task_attachments
		WHERE id = ?
			AND task_id IN (SELECT id FROM tasks WHERE project_id = ?)
		RETURNING task_id
	`
	return r.deleteByQuery(ctx, query, id, projectID)
}

// Deletions invalidate the task's model attachment context only after the
// autocommitted statement succeeds.
func (r *AttachmentRepo) deleteByQuery(ctx context.Context, query string, args ...any) error {
	var taskID string
	err := queryRowBoundSQLite(ctx, r.db, query, args...).Scan(&taskID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("attachment not found")
	}
	if err != nil {
		return fmt.Errorf("deleting attachment: %w", err)
	}
	attachmentcontext.Invalidate(taskID)
	return nil
}

func (r *AttachmentRepo) DeleteByTask(ctx context.Context, taskID string) error {
	query := `DELETE FROM task_attachments WHERE task_id = ?`
	_, err := execBoundSQLite(ctx, r.db, query, taskID)
	if err != nil {
		return fmt.Errorf("deleting task attachments: %w", err)
	}
	attachmentcontext.Invalidate(taskID)
	return nil
}

// Task-thread message attachments may live under uploads/chat, so both cleanup
// scopes must treat paths from either table as referenced.
const selectAllTaskAttachmentFilePathsSQL = `SELECT file_path FROM task_attachments UNION ALL SELECT file_path FROM generated_files UNION ALL SELECT file_path FROM chat_attachments`

// GetAllFilePaths returns all file paths currently in the database
func (r *AttachmentRepo) GetAllFilePaths(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, selectAllTaskAttachmentFilePathsSQL)
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

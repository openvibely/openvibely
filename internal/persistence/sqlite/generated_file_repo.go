package sqlite

import (
	"context"
	"database/sql"
	"github.com/openvibely/openvibely/internal/models"
)

// File outputs share attachment storage ownership but have their own table so
// publishing never changes the next request's implicit attachment list.
func (r *AttachmentRepo) CreateGeneratedFile(ctx context.Context, f *models.GeneratedFile) error {
	return queryRowBoundSQLite(ctx, r.db, `INSERT INTO generated_files(project_id,execution_id,file_name,file_path,media_type,file_size,display)
 SELECT ?,e.id,?,?,?,?,? FROM executions e JOIN tasks t ON t.id=e.task_id WHERE e.id=? AND t.project_id=?
 RETURNING id,created_at`, f.ProjectID, f.FileName, f.FilePath, f.MediaType, f.FileSize, f.Display, f.ExecutionID, f.ProjectID).Scan(&f.ID, &f.CreatedAt)
}
func (r *AttachmentRepo) GeneratedFile(ctx context.Context, id, projectID string) (*models.GeneratedFile, error) {
	var f models.GeneratedFile
	err := r.db.QueryRowContext(ctx, `SELECT id,project_id,execution_id,file_name,file_path,media_type,file_size,display,created_at FROM generated_files WHERE id=? AND project_id=?`, id, projectID).Scan(&f.ID, &f.ProjectID, &f.ExecutionID, &f.FileName, &f.FilePath, &f.MediaType, &f.FileSize, &f.Display, &f.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &f, err
}
func (r *AttachmentRepo) ListGeneratedFiles(ctx context.Context, projectID, taskID string) ([]models.GeneratedFile, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT f.id,f.project_id,f.execution_id,f.file_name,f.file_path,f.media_type,f.file_size,f.display,f.created_at FROM generated_files f JOIN executions e ON e.id=f.execution_id WHERE f.project_id=? AND (?='' OR e.task_id=?) ORDER BY f.created_at DESC,f.id LIMIT 200`, projectID, taskID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.GeneratedFile{}
	for rows.Next() {
		var f models.GeneratedFile
		if err := rows.Scan(&f.ID, &f.ProjectID, &f.ExecutionID, &f.FileName, &f.FilePath, &f.MediaType, &f.FileSize, &f.Display, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (r *AttachmentRepo) ListUploadedFiles(ctx context.Context, projectID, taskID string) ([]models.FileListItem, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,file_name,file_size,kind FROM (
 SELECT a.id,a.file_name,a.file_size,a.created_at,'task' AS kind FROM task_attachments a JOIN tasks t ON t.id=a.task_id WHERE t.project_id=? AND (?='' OR t.id=?)
 UNION ALL
 SELECT a.id,a.file_name,a.file_size,a.created_at,'chat' AS kind FROM chat_attachments a JOIN executions e ON e.id=a.execution_id JOIN tasks t ON t.id=e.task_id WHERE t.project_id=? AND (?='' OR t.id=?)
 ) ORDER BY created_at DESC LIMIT 200`, projectID, taskID, taskID, projectID, taskID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.FileListItem{}
	for rows.Next() {
		var f models.FileListItem
		var id, kind string
		if err := rows.Scan(&id, &f.Name, &f.Size, &kind); err != nil {
			return nil, err
		}
		f.URL = "/attachments/" + id + "/download"
		if kind == "chat" {
			f.URL = "/chat/attachments/" + id + "/download"
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

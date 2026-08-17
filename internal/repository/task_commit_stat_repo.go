package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type TaskCommitStatRepo struct {
	db *sql.DB
}

func NewTaskCommitStatRepo(db *sql.DB) *TaskCommitStatRepo {
	return &TaskCommitStatRepo{db: db}
}

func (r *TaskCommitStatRepo) UpsertProducedCommitStat(ctx context.Context, stat *models.TaskCommitStat) error {
	if stat == nil {
		return fmt.Errorf("task commit stat is nil")
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO task_commit_stats (
			project_id, task_id, execution_id, commit_sha, short_sha, subject, author,
			produced_at, insertions, deletions, files_changed, changed_files_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id, commit_sha) DO UPDATE SET
			execution_id = excluded.execution_id,
			short_sha = excluded.short_sha,
			subject = excluded.subject,
			author = excluded.author,
			produced_at = excluded.produced_at,
			insertions = excluded.insertions,
			deletions = excluded.deletions,
			files_changed = excluded.files_changed,
			changed_files_json = excluded.changed_files_json,
			updated_at = datetime('now')`,
		stat.ProjectID, stat.TaskID, stat.ExecutionID, stat.CommitSHA, stat.ShortSHA, stat.Subject, stat.Author,
		stat.ProducedAt, stat.Insertions, stat.Deletions, stat.FilesChanged, stat.ChangedFilesJSON)
	if err != nil {
		return fmt.Errorf("upserting task commit stat: %w", err)
	}
	return nil
}

func (r *TaskCommitStatRepo) ListProducedCommitStats(ctx context.Context, projectID string, since time.Time) ([]models.TaskCommitStat, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, task_id, execution_id, commit_sha, short_sha, subject, author,
			produced_at, insertions, deletions, files_changed, changed_files_json, created_at, updated_at
		FROM task_commit_stats
		WHERE project_id = ? AND produced_at >= ?
		ORDER BY produced_at DESC, created_at DESC, id DESC`, projectID, since)
	if err != nil {
		return nil, fmt.Errorf("listing task commit stats: %w", err)
	}
	defer rows.Close()

	var stats []models.TaskCommitStat
	for rows.Next() {
		var stat models.TaskCommitStat
		var executionID sql.NullString
		if err := rows.Scan(
			&stat.ID, &stat.ProjectID, &stat.TaskID, &executionID, &stat.CommitSHA, &stat.ShortSHA, &stat.Subject, &stat.Author,
			&stat.ProducedAt, &stat.Insertions, &stat.Deletions, &stat.FilesChanged, &stat.ChangedFilesJSON, &stat.CreatedAt, &stat.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning task commit stat: %w", err)
		}
		if executionID.Valid {
			execID := executionID.String
			stat.ExecutionID = &execID
		}
		stats = append(stats, stat)
	}
	return stats, rows.Err()
}

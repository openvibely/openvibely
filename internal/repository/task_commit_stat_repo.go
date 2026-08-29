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

type ProducedCommitStatAggregate struct {
	TotalCommits    int
	TotalInsertions int
	TotalDeletions  int
}

func NewTaskCommitStatRepo(db *sql.DB) *TaskCommitStatRepo {
	return &TaskCommitStatRepo{db: db}
}

func (r *TaskCommitStatRepo) UpsertProducedCommitStat(ctx context.Context, stat *models.TaskCommitStat) error {
	if stat == nil {
		return fmt.Errorf("task commit stat is nil")
	}
	_, err := execBoundSQLite(ctx, r.db, `
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

func (r *TaskCommitStatRepo) FirstProducedCommitStatTime(ctx context.Context, projectID string) (time.Time, error) {
	var producedAt time.Time
	err := r.db.QueryRowContext(ctx, `
		SELECT produced_at
		FROM task_commit_stats
		WHERE project_id = ?
		ORDER BY produced_at ASC
		LIMIT 1`, projectID).Scan(&producedAt)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("getting first task commit stat time: %w", err)
	}
	return producedAt, nil
}

func (r *TaskCommitStatRepo) ProducedCommitStatAggregate(ctx context.Context, projectID string, since time.Time) (ProducedCommitStatAggregate, error) {
	var aggregate ProducedCommitStatAggregate
	err := r.db.QueryRowContext(ctx, `
			SELECT COUNT(*), COALESCE(SUM(insertions), 0), COALESCE(SUM(deletions), 0)
			FROM task_commit_stats
			WHERE project_id = ? AND produced_at >= ?`, projectID, since).Scan(
		&aggregate.TotalCommits, &aggregate.TotalInsertions, &aggregate.TotalDeletions)
	if err != nil {
		return ProducedCommitStatAggregate{}, fmt.Errorf("summarizing task commit stats: %w", err)
	}
	return aggregate, nil
}

func (r *TaskCommitStatRepo) ListProducedCommitStatCommits(ctx context.Context, projectID string, since time.Time, limit int) ([]models.GitCommit, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
			SELECT commit_sha, short_sha, subject, author, produced_at, insertions, deletions, files_changed
			FROM task_commit_stats
			WHERE project_id = ? AND produced_at >= ?
			ORDER BY produced_at DESC, created_at DESC, id DESC
			LIMIT ?`, projectID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("listing task commit stat commits: %w", err)
	}
	defer rows.Close()

	commits := make([]models.GitCommit, 0, limit)
	for rows.Next() {
		var commit models.GitCommit
		if err := rows.Scan(
			&commit.Hash, &commit.ShortHash, &commit.Subject, &commit.Author, &commit.Date,
			&commit.Insertions, &commit.Deletions, &commit.FilesChanged,
		); err != nil {
			return nil, fmt.Errorf("scanning task commit stat commit: %w", err)
		}
		commits = append(commits, commit)
	}
	return commits, rows.Err()
}

func (r *TaskCommitStatRepo) ForEachProducedCommitStatSubject(ctx context.Context, projectID string, since time.Time, fn func(subject string) error) error {
	rows, err := r.db.QueryContext(ctx, `
			SELECT subject
			FROM task_commit_stats
			WHERE project_id = ? AND produced_at >= ?
			ORDER BY produced_at DESC, created_at DESC, id DESC`, projectID, since)
	if err != nil {
		return fmt.Errorf("listing task commit stat subjects: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var subject string
		if err := rows.Scan(&subject); err != nil {
			return fmt.Errorf("scanning task commit stat subject: %w", err)
		}
		if err := fn(subject); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (r *TaskCommitStatRepo) ForEachProducedCommitStatChangedFilesJSON(ctx context.Context, projectID string, since time.Time, fn func(changedFilesJSON string) error) error {
	rows, err := r.db.QueryContext(ctx, `
			SELECT changed_files_json
			FROM task_commit_stats
			WHERE project_id = ? AND produced_at >= ?`, projectID, since)
	if err != nil {
		return fmt.Errorf("listing task commit stat changed files: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var changedFilesJSON string
		if err := rows.Scan(&changedFilesJSON); err != nil {
			return fmt.Errorf("scanning task commit stat changed files: %w", err)
		}
		if err := fn(changedFilesJSON); err != nil {
			return err
		}
	}
	return rows.Err()
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

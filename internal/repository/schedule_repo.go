package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type ScheduleRepo struct {
	db *sql.DB
}

func NewScheduleRepo(db *sql.DB) *ScheduleRepo {
	return &ScheduleRepo{db: db}
}

func (r *ScheduleRepo) ListByTask(ctx context.Context, taskID string) ([]models.Schedule, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run, last_run, created_at, updated_at
		 FROM schedules WHERE task_id = ? ORDER BY created_at DESC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing schedules: %w", err)
	}
	defer rows.Close()
	return r.scanRows(rows)
}

func (r *ScheduleRepo) ListDue(ctx context.Context, now time.Time) ([]models.Schedule, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run, last_run, created_at, updated_at
		 FROM schedules WHERE enabled = 1 AND next_run IS NOT NULL AND next_run <= ?
		 ORDER BY next_run ASC`, now)
	if err != nil {
		return nil, fmt.Errorf("listing due schedules: %w", err)
	}
	defer rows.Close()
	return r.scanRows(rows)
}

func (r *ScheduleRepo) ListByProject(ctx context.Context, projectID string) ([]models.Schedule, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT s.id, s.task_id, s.run_at, s.repeat_type, s.repeat_interval, s.enabled,
		 s.next_run, s.last_run, s.created_at, s.updated_at
		 FROM schedules s
		 JOIN tasks t ON t.id = s.task_id
		 WHERE t.project_id = ?
		 ORDER BY s.next_run ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing project schedules: %w", err)
	}
	defer rows.Close()
	return r.scanRows(rows)
}

func (r *ScheduleRepo) ListByTaskIDs(ctx context.Context, taskIDs []string) (map[string][]models.Schedule, error) {
	if len(taskIDs) == 0 {
		return map[string][]models.Schedule{}, nil
	}
	placeholders := make([]byte, 0, len(taskIDs)*2-1)
	args := make([]interface{}, len(taskIDs))
	for i, id := range taskIDs {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run, last_run, created_at, updated_at
		 FROM schedules WHERE task_id IN (`+string(placeholders)+`) ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("batch listing schedules: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]models.Schedule, len(taskIDs))
	for rows.Next() {
		var s models.Schedule
		if err := rows.Scan(&s.ID, &s.TaskID, &s.RunAt, &s.RepeatType, &s.RepeatInterval,
			&s.Enabled, &s.NextRun, &s.LastRun, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning schedule: %w", err)
		}
		result[s.TaskID] = append(result[s.TaskID], s)
	}
	return result, rows.Err()
}

func (r *ScheduleRepo) GetByID(ctx context.Context, id string) (*models.Schedule, error) {
	var s models.Schedule
	err := r.db.QueryRowContext(ctx,
		`SELECT id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run, last_run, created_at, updated_at
		 FROM schedules WHERE id = ?`, id).
		Scan(&s.ID, &s.TaskID, &s.RunAt, &s.RepeatType, &s.RepeatInterval, &s.Enabled,
			&s.NextRun, &s.LastRun, &s.CreatedAt, &s.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting schedule: %w", err)
	}
	return &s, nil
}

func (r *ScheduleRepo) Create(ctx context.Context, s *models.Schedule) error {
	// Compute initial next_run
	if s.NextRun == nil {
		t := s.RunAt
		s.NextRun = &t
	}
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO schedules (id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run)
		 VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?)
		 RETURNING id, created_at, updated_at`,
		s.TaskID, s.RunAt, s.RepeatType, s.RepeatInterval, s.Enabled, s.NextRun).
		Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return fmt.Errorf("creating schedule: %w", err)
	}
	return nil
}

func (r *ScheduleRepo) Update(ctx context.Context, s *models.Schedule) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE schedules SET run_at = ?, repeat_type = ?, repeat_interval = ?,
		 enabled = ?, next_run = ?, updated_at = datetime('now')
		 WHERE id = ?`,
		s.RunAt, s.RepeatType, s.RepeatInterval, s.Enabled, s.NextRun, s.ID)
	if err != nil {
		return fmt.Errorf("updating schedule: %w", err)
	}
	return nil
}

func (r *ScheduleRepo) MarkRan(ctx context.Context, id string, lastRun time.Time, nextRun *time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE schedules SET last_run = ?, next_run = ?, updated_at = datetime('now') WHERE id = ?`,
		lastRun, nextRun, id)
	if err != nil {
		return fmt.Errorf("marking schedule ran: %w", err)
	}
	return nil
}

func (r *ScheduleRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting schedule: %w", err)
	}
	return nil
}

func (r *ScheduleRepo) ToggleEnabled(ctx context.Context, id string, enabled bool) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE schedules SET enabled = ?, updated_at = datetime('now') WHERE id = ?`,
		enabled, id)
	if err != nil {
		return fmt.Errorf("toggling schedule: %w", err)
	}
	return nil
}

func (r *ScheduleRepo) scanRows(rows *sql.Rows) ([]models.Schedule, error) {
	var schedules []models.Schedule
	for rows.Next() {
		var s models.Schedule
		if err := rows.Scan(&s.ID, &s.TaskID, &s.RunAt, &s.RepeatType, &s.RepeatInterval,
			&s.Enabled, &s.NextRun, &s.LastRun, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning schedule: %w", err)
		}
		schedules = append(schedules, s)
	}
	return schedules, rows.Err()
}

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type ScheduleRepo struct {
	db *sql.DB
}

const (
	scheduleDiscoverySelectColumns = `s.id, s.task_id, s.run_at, s.repeat_type, s.repeat_interval, s.enabled, s.clear_context_on_start,
			 s.next_run, s.last_run, s.created_at, s.updated_at, t.title`
	scheduleDiscoveryOrderBy = `ORDER BY (s.next_run IS NULL), s.next_run ASC, s.created_at DESC, s.id ASC`
	// Matches the runtime list_schedules page cap. Larger, filtered, empty, or
	// single-page result sets keep the existing project/task-indexed join shape
	// because selective predicates can be cheaper than scanning the global order index.
	scheduleDiscoveryOrderedScanMaxLimit = 50
)

func NewScheduleRepo(db *sql.DB) *ScheduleRepo {
	return &ScheduleRepo{db: db}
}

func (r *ScheduleRepo) ListByTask(ctx context.Context, taskID string) ([]models.Schedule, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, task_id, run_at, repeat_type, repeat_interval, enabled, clear_context_on_start, next_run, last_run, created_at, updated_at
		 FROM schedules WHERE task_id = ? ORDER BY created_at DESC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing schedules: %w", err)
	}
	defer rows.Close()
	return r.scanRows(rows)
}

func (r *ScheduleRepo) ListDue(ctx context.Context, now time.Time) ([]models.Schedule, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, task_id, run_at, repeat_type, repeat_interval, enabled, clear_context_on_start, next_run, last_run, created_at, updated_at
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
		`SELECT s.id, s.task_id, s.run_at, s.repeat_type, s.repeat_interval, s.enabled, s.clear_context_on_start,
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
		`SELECT id, task_id, run_at, repeat_type, repeat_interval, enabled, clear_context_on_start, next_run, last_run, created_at, updated_at
		 FROM schedules WHERE task_id IN (`+string(placeholders)+`) ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("batch listing schedules: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]models.Schedule, len(taskIDs))
	for rows.Next() {
		var s models.Schedule
		if err := rows.Scan(&s.ID, &s.TaskID, &s.RunAt, &s.RepeatType, &s.RepeatInterval,
			&s.Enabled, &s.ClearContextOnStart, &s.NextRun, &s.LastRun, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning schedule: %w", err)
		}
		result[s.TaskID] = append(result[s.TaskID], s)
	}
	return result, rows.Err()
}

// ScheduleDiscoveryFilter bounds a read-only, current-project schedule discovery query.
type ScheduleDiscoveryFilter struct {
	// TaskID optionally restricts results to schedules bound to a single task.
	TaskID string
	// Title is an optional partial (case-insensitive substring) task title match.
	Title string
	// Enabled optionally restricts results to enabled (true) or disabled (false)
	// schedules. Nil returns both.
	Enabled *bool
	// Limit caps the number of returned rows. Callers should clamp before use.
	Limit int
	// Offset skips the first N rows for pagination.
	Offset int
}

// ScheduleDiscoveryRow is a schedule paired with its bound task's title for
// read-only discovery projections.
type ScheduleDiscoveryRow struct {
	Schedule  models.Schedule
	TaskTitle string
}

// ListSchedulesForDiscovery returns a bounded, deterministic page of schedules for
// a single project, plus the total number of matching rows for pagination. It never
// crosses project boundaries: the join on tasks.project_id enforces isolation.
//
// Ordering is deterministic: schedules with a pending next_run come first ordered by
// next_run ASC, then schedules without a next_run, all tie-broken by created_at DESC
// then id ASC.
func (r *ScheduleRepo) ListSchedulesForDiscovery(ctx context.Context, projectID string, filter ScheduleDiscoveryFilter) ([]ScheduleDiscoveryRow, int, error) {
	where := `t.project_id = ?`
	args := []any{projectID}
	hasFilters := false

	if taskID := strings.TrimSpace(filter.TaskID); taskID != "" {
		where += ` AND s.task_id = ?`
		args = append(args, taskID)
		hasFilters = true
	}
	if title := strings.TrimSpace(filter.Title); title != "" {
		where += ` AND t.title LIKE ?`
		args = append(args, "%"+title+"%")
		hasFilters = true
	}
	if filter.Enabled != nil {
		where += ` AND s.enabled = ?`
		args = append(args, *filter.Enabled)
		hasFilters = true
	}

	var total int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schedules s JOIN tasks t ON t.id = s.task_id WHERE `+where, args...).
		Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting schedules for discovery: %w", err)
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	if total == 0 {
		return nil, 0, nil
	}
	useOrderedScan := !hasFilters && offset == 0 && limit <= scheduleDiscoveryOrderedScanMaxLimit && total > limit

	selectArgs := append([]any{}, args...)
	selectArgs = append(selectArgs, limit)
	if !useOrderedScan {
		selectArgs = append(selectArgs, offset)
	}

	rows, err := r.db.QueryContext(ctx, scheduleDiscoverySelectSQL(where, useOrderedScan), selectArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing schedules for discovery: %w", err)
	}
	defer rows.Close()

	var out []ScheduleDiscoveryRow
	for rows.Next() {
		var row ScheduleDiscoveryRow
		s := &row.Schedule
		if err := rows.Scan(&s.ID, &s.TaskID, &s.RunAt, &s.RepeatType, &s.RepeatInterval,
			&s.Enabled, &s.ClearContextOnStart, &s.NextRun, &s.LastRun, &s.CreatedAt, &s.UpdatedAt, &row.TaskTitle); err != nil {
			return nil, 0, fmt.Errorf("scanning schedule for discovery: %w", err)
		}
		out = append(out, row)
	}
	return out, total, rows.Err()
}

func scheduleDiscoverySelectSQL(where string, orderedScan bool) string {
	scheduleSource := `schedules s`
	limitClause := `LIMIT ? OFFSET ?`
	if orderedScan {
		scheduleSource = `schedules s INDEXED BY idx_schedules_discovery_order`
		limitClause = `LIMIT ?`
	}
	return `SELECT ` + scheduleDiscoverySelectColumns + `
			 FROM ` + scheduleSource + ` JOIN tasks t ON t.id = s.task_id
			 WHERE ` + where + `
			 ` + scheduleDiscoveryOrderBy + `
			 ` + limitClause
}

func (r *ScheduleRepo) GetByID(ctx context.Context, id string) (*models.Schedule, error) {
	var s models.Schedule
	err := r.db.QueryRowContext(ctx,
		`SELECT id, task_id, run_at, repeat_type, repeat_interval, enabled, clear_context_on_start, next_run, last_run, created_at, updated_at
		 FROM schedules WHERE id = ?`, id).
		Scan(&s.ID, &s.TaskID, &s.RunAt, &s.RepeatType, &s.RepeatInterval, &s.Enabled,
			&s.ClearContextOnStart, &s.NextRun, &s.LastRun, &s.CreatedAt, &s.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting schedule: %w", err)
	}
	return &s, nil
}

func (r *ScheduleRepo) Create(ctx context.Context, s *models.Schedule) error {
	if err := models.ValidateScheduleRepeatInterval(s.RepeatInterval); err != nil {
		return fmt.Errorf("creating schedule: %w", err)
	}
	// Compute initial next_run
	if s.NextRun == nil {
		t := s.RunAt
		s.NextRun = &t
	}
	err := queryRowBoundSQLite(ctx, r.db,
		`INSERT INTO schedules (id, task_id, run_at, repeat_type, repeat_interval, enabled, clear_context_on_start, next_run)
		 VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?)
		 RETURNING id, created_at, updated_at`,
		s.TaskID, s.RunAt, s.RepeatType, s.RepeatInterval, s.Enabled, s.ClearContextOnStart, s.NextRun).
		Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return fmt.Errorf("creating schedule: %w", err)
	}
	return nil
}

func (r *ScheduleRepo) Update(ctx context.Context, s *models.Schedule) error {
	if err := models.ValidateScheduleRepeatInterval(s.RepeatInterval); err != nil {
		return fmt.Errorf("updating schedule: %w", err)
	}
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE schedules SET run_at = ?, repeat_type = ?, repeat_interval = ?,
		 enabled = ?, clear_context_on_start = ?, next_run = ?, updated_at = datetime('now')
		 WHERE id = ?`,
		s.RunAt, s.RepeatType, s.RepeatInterval, s.Enabled, s.ClearContextOnStart, s.NextRun, s.ID)
	if err != nil {
		return fmt.Errorf("updating schedule: %w", err)
	}
	return nil
}

func (r *ScheduleRepo) UpdateClearContextOnStart(ctx context.Context, id, taskID string, clearContextOnStart bool) error {
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE schedules SET clear_context_on_start = ?, updated_at = datetime('now') WHERE id = ? AND task_id = ?`,
		clearContextOnStart, id, taskID)
	if err != nil {
		return fmt.Errorf("updating schedule clear context on start: %w", err)
	}
	return nil
}

func (r *ScheduleRepo) MarkRan(ctx context.Context, id string, lastRun time.Time, nextRun *time.Time) error {
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE schedules SET last_run = ?, next_run = ?, updated_at = datetime('now') WHERE id = ?`,
		lastRun, nextRun, id)
	if err != nil {
		return fmt.Errorf("marking schedule ran: %w", err)
	}
	return nil
}

func (r *ScheduleRepo) Delete(ctx context.Context, id string) error {
	_, err := execBoundSQLite(ctx, r.db, `DELETE FROM schedules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting schedule: %w", err)
	}
	return nil
}

func (r *ScheduleRepo) ToggleEnabled(ctx context.Context, id string, enabled bool) error {
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE schedules SET enabled = ?, updated_at = datetime('now') WHERE id = ?`,
		enabled, id)
	if err != nil {
		return fmt.Errorf("toggling schedule: %w", err)
	}
	return nil
}

func (r *ScheduleRepo) DeleteOrphan(ctx context.Context, id, taskID string) error {
	_, err := execBoundSQLite(ctx, r.db,
		`DELETE FROM schedules
		 WHERE id = ? AND task_id = ? AND NOT EXISTS (SELECT 1 FROM tasks WHERE tasks.id = schedules.task_id)`,
		id, taskID)
	if err != nil {
		return fmt.Errorf("deleting orphan schedule: %w", err)
	}
	return nil
}

func (r *ScheduleRepo) scanRows(rows *sql.Rows) ([]models.Schedule, error) {
	var schedules []models.Schedule
	for rows.Next() {
		var s models.Schedule
		if err := rows.Scan(&s.ID, &s.TaskID, &s.RunAt, &s.RepeatType, &s.RepeatInterval,
			&s.Enabled, &s.ClearContextOnStart, &s.NextRun, &s.LastRun, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning schedule: %w", err)
		}
		schedules = append(schedules, s)
	}
	return schedules, rows.Err()
}

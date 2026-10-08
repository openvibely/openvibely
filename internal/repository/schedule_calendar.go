package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

var ErrScheduleCalendarSelection = errors.New("invalid schedule selection")

func (r *ScheduleRepo) CalendarState(ctx context.Context, projectID string) (models.ScheduleCalendarState, error) {
	state := models.ScheduleCalendarState{Skips: []models.ScheduleSkip{}}
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schedule_project_pauses WHERE project_id = ?)`, projectID).Scan(&state.Paused); err != nil {
		return state, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT schedule_id, start_at, end_at, restored FROM schedule_skips WHERE project_id = ? ORDER BY start_at`, projectID)
	if err != nil {
		return state, err
	}
	defer rows.Close()
	for rows.Next() {
		var skip models.ScheduleSkip
		if err := rows.Scan(&skip.ScheduleID, &skip.StartAt, &skip.EndAt, &skip.Restored); err != nil {
			return state, err
		}
		state.Skips = append(state.Skips, skip)
	}
	return state, rows.Err()
}

// ApplyCalendarAction validates the entire selection before changing anything and
// returns an inverse containing only rows actually changed by this action.
func (r *ScheduleRepo) ApplyCalendarAction(ctx context.Context, projectID string, action models.ScheduleCalendarAction, now time.Time) (models.ScheduleCalendarAction, error) {
	inverse := models.ScheduleCalendarAction{}
	err := withImmediateTx(ctx, r.db, func(exec SQLExecutor) error {
		var exists bool
		if err := exec.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id = ?)`, projectID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrScheduleCalendarSelection
		}
		ids := map[string]bool{}
		for _, id := range action.ScheduleIDs {
			if id == "" {
				return ErrScheduleCalendarSelection
			}
			ids[id] = true
		}
		for _, skip := range action.Skips {
			if skip.ScheduleID != "" {
				ids[skip.ScheduleID] = true
			}
		}
		for _, change := range action.Changes {
			if change.Skip.ScheduleID != "" {
				ids[change.Skip.ScheduleID] = true
			}
		}
		schedules := map[string]models.Schedule{}
		for id := range ids {
			var s models.Schedule
			err := exec.QueryRowContext(ctx, `SELECT s.id, s.run_at, s.repeat_type, s.repeat_interval, s.enabled, s.next_run FROM schedules s JOIN tasks t ON t.id = s.task_id WHERE s.id = ? AND t.project_id = ?`, id, projectID).Scan(&s.ID, &s.RunAt, &s.RepeatType, &s.RepeatInterval, &s.Enabled, &s.NextRun)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrScheduleCalendarSelection
			}
			if err != nil {
				return err
			}
			schedules[id] = s
		}
		switch action.Action {
		case "skip", "restore", "undo_skips":
			inverse.Action = "undo_skips"
			change := func(skip models.ScheduleSkip, present bool) error {
				changed, err := changeScheduleSkip(ctx, exec, projectID, skip, present)
				if err != nil {
					return err
				}
				if changed {
					inverse.Changes = append(inverse.Changes, models.ScheduleSkipChange{Skip: skip, Present: !present})
				}
				return nil
			}
			if action.Action == "undo_skips" {
				for i := len(action.Changes) - 1; i >= 0; i-- {
					c := action.Changes[i]
					if err := change(c.Skip, c.Present); err != nil {
						return err
					}
				}
				break
			}
			for _, requested := range action.Skips {
				rows, err := exec.QueryContext(ctx, `SELECT schedule_id,start_at,end_at,restored FROM schedule_skips WHERE project_id = ? AND start_at < ? AND end_at > ?`, projectID, requested.EndAt, requested.StartAt)
				if err != nil {
					return err
				}
				var overlapping []models.ScheduleSkip
				for rows.Next() {
					var skip models.ScheduleSkip
					if err := rows.Scan(&skip.ScheduleID, &skip.StartAt, &skip.EndAt, &skip.Restored); err != nil {
						rows.Close()
						return err
					}
					overlapping = append(overlapping, skip)
				}
				err = rows.Err()
				rows.Close()
				if err != nil {
					return err
				}
				restoreProjectSkip := false
				for _, existing := range overlapping {
					if requested.ScheduleID != "" && existing.ScheduleID != requested.ScheduleID {
						if existing.ScheduleID == "" && !existing.Restored {
							restoreProjectSkip = true
						}
						continue
					}
					// A new skip only removes restore exceptions; restoring removes
					// exclusions and exceptions in the selected interval. Preserve
					// both sides of a partially intersecting day/hour exclusion.
					if action.Action == "skip" && !existing.Restored {
						continue
					}
					if err := change(existing, false); err != nil {
						return err
					}
					if existing.StartAt < requested.StartAt {
						before := existing
						before.EndAt = requested.StartAt
						if err := change(before, true); err != nil {
							return err
						}
					}
					if existing.EndAt > requested.EndAt {
						after := existing
						after.StartAt = requested.EndAt
						if err := change(after, true); err != nil {
							return err
						}
					}
				}
				if action.Action == "skip" {
					requested.Restored = false
					if err := change(requested, true); err != nil {
						return err
					}
				}
				if action.Action == "restore" && restoreProjectSkip {
					requested.Restored = true
					if err := change(requested, true); err != nil {
						return err
					}
				}
			}
		case "pause", "resume":
			enabled := action.Action == "resume"
			if enabled {
				inverse.Action = "pause"
			} else {
				inverse.Action = "resume"
			}
			for _, id := range action.ScheduleIDs {
				s := schedules[id]
				if s.Enabled == enabled {
					continue
				}
				next := s.NextRun
				if enabled && (next == nil || !next.After(now)) {
					next = s.ComputeNextRun(now)
				}
				if _, err := exec.ExecContext(ctx, `UPDATE schedules SET enabled = ?, next_run = ?, updated_at = ? WHERE id = ?`, enabled, next, now, id); err != nil {
					return err
				}
				inverse.ScheduleIDs = append(inverse.ScheduleIDs, id)
				s.Enabled = enabled
				schedules[id] = s
			}
		case "pause_all":
			result, err := exec.ExecContext(ctx, `INSERT OR IGNORE INTO schedule_project_pauses(project_id,started_at) VALUES(?,?)`, projectID, now.Unix())
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n > 0 {
				inverse.Action = "resume_all"
			}
		case "resume_all":
			var started int64
			err := exec.QueryRowContext(ctx, `SELECT started_at FROM schedule_project_pauses WHERE project_id = ?`, projectID).Scan(&started)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			// Retain the elapsed pause as an exclusion so overdue occurrences cannot
			// catch up after a restart, even if no scheduler tick happened while paused.
			if now.Unix() > started {
				if _, err := exec.ExecContext(ctx, `INSERT OR IGNORE INTO schedule_skips(project_id,schedule_id,start_at,end_at) VALUES(?,'',?,?)`, projectID, started, now.Unix()); err != nil {
					return err
				}
			}
			// Reconcile missed starts even when no scheduler tick occurred during
			// the pause. Keep each schedule's independently enabled/paused state.
			rows, err := exec.QueryContext(ctx, `SELECT s.id, s.run_at, s.repeat_type, s.repeat_interval, s.next_run FROM schedules s JOIN tasks t ON t.id = s.task_id WHERE t.project_id = ? AND s.enabled = 1 AND s.next_run IS NOT NULL AND s.next_run <= ?`, projectID, now)
			if err != nil {
				return err
			}
			var overdue []models.Schedule
			for rows.Next() {
				var schedule models.Schedule
				if err := rows.Scan(&schedule.ID, &schedule.RunAt, &schedule.RepeatType, &schedule.RepeatInterval, &schedule.NextRun); err != nil {
					rows.Close()
					return err
				}
				overdue = append(overdue, schedule)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, schedule := range overdue {
				if _, err := exec.ExecContext(ctx, `UPDATE schedules SET next_run = ?, updated_at = ? WHERE id = ?`, schedule.ComputeNextRun(now), now, schedule.ID); err != nil {
					return err
				}
			}
			if _, err := exec.ExecContext(ctx, `DELETE FROM schedule_project_pauses WHERE project_id = ?`, projectID); err != nil {
				return err
			}
			inverse.Action = "pause_all"
		default:
			return fmt.Errorf("%w: unknown action", ErrScheduleCalendarSelection)
		}
		return nil
	})
	return inverse, err
}

// SuppressedOccurrence is checked before either ordinary or automation dispatch.
// The returned boundary allows a skipped day/hour to advance in one scheduler tick.
func (r *ScheduleRepo) SuppressedOccurrence(ctx context.Context, scheduleID string, occurrence time.Time) (bool, time.Time, error) {
	var paused bool
	var end sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT (s.enabled = 0 OR EXISTS(SELECT 1 FROM schedule_project_pauses p WHERE p.project_id = t.project_id)),
 (SELECT MAX(k.end_at) FROM schedule_skips k WHERE k.project_id = t.project_id AND (k.schedule_id = '' OR k.schedule_id = s.id) AND k.restored = 0 AND k.start_at <= ? AND k.end_at > ? AND NOT EXISTS (SELECT 1 FROM schedule_skips restored WHERE restored.project_id = t.project_id AND restored.schedule_id = s.id AND restored.restored = 1 AND restored.start_at <= ? AND restored.end_at > ?))
 FROM schedules s JOIN tasks t ON t.id = s.task_id WHERE s.id = ?`, occurrence.Unix(), occurrence.Unix(), occurrence.Unix(), occurrence.Unix(), scheduleID).Scan(&paused, &end)
	if errors.Is(err, sql.ErrNoRows) {
		return false, time.Time{}, nil
	}
	if err != nil {
		return false, time.Time{}, err
	}
	if paused {
		return true, time.Time{}, nil
	}
	if end.Valid {
		return true, time.Unix(end.Int64, 0), nil
	}
	return false, time.Time{}, nil
}

// changeScheduleSkip is shared by user actions and their exact inverse. All
// callers run inside the same project-validated write transaction.
func changeScheduleSkip(ctx context.Context, exec SQLExecutor, projectID string, skip models.ScheduleSkip, present bool) (bool, error) {
	var result sql.Result
	var err error
	if present {
		result, err = exec.ExecContext(ctx, `INSERT OR IGNORE INTO schedule_skips(project_id,schedule_id,start_at,end_at,restored) VALUES(?,?,?,?,?)`, projectID, skip.ScheduleID, skip.StartAt, skip.EndAt, skip.Restored)
	} else {
		result, err = exec.ExecContext(ctx, `DELETE FROM schedule_skips WHERE project_id = ? AND schedule_id = ? AND start_at = ? AND end_at = ? AND restored = ?`, projectID, skip.ScheduleID, skip.StartAt, skip.EndAt, skip.Restored)
	}
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

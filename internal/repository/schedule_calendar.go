package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openvibely/openvibely/internal/events"
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
	return suppressedScheduleOccurrence(ctx, r.db, scheduleID, occurrence)
}

func suppressedScheduleOccurrence(ctx context.Context, exec SQLExecutor, scheduleID string, occurrence time.Time) (bool, time.Time, error) {
	var paused bool
	var end, restoredStart sql.NullInt64
	err := exec.QueryRowContext(ctx, `SELECT (s.enabled = 0 OR EXISTS(SELECT 1 FROM schedule_project_pauses p WHERE p.project_id = t.project_id)),
 (SELECT MAX(k.end_at) FROM schedule_skips k WHERE k.project_id = t.project_id AND (k.schedule_id = '' OR k.schedule_id = s.id) AND k.restored = 0 AND k.start_at <= ? AND k.end_at > ? AND NOT EXISTS (SELECT 1 FROM schedule_skips restored WHERE restored.project_id = t.project_id AND restored.schedule_id = s.id AND restored.restored = 1 AND restored.start_at <= ? AND restored.end_at > ?))
 ,(SELECT MIN(restored.start_at) FROM schedule_skips restored WHERE restored.project_id = t.project_id AND restored.schedule_id = s.id AND restored.restored = 1 AND restored.start_at > ?)
 FROM schedules s JOIN tasks t ON t.id = s.task_id WHERE s.id = ?`, occurrence.Unix(), occurrence.Unix(), occurrence.Unix(), occurrence.Unix(), occurrence.Unix(), scheduleID).Scan(&paused, &end, &restoredStart)
	if errors.Is(err, sql.ErrNoRows) {
		return false, time.Time{}, nil
	}
	if err != nil {
		return false, time.Time{}, err
	}
	if paused {
		return true, time.Time{}, nil
	}
	// Never jump over a restored run when reconciling overdue exclusions.
	if end.Valid {
		if restoredStart.Valid && restoredStart.Int64 < end.Int64 {
			end.Int64 = restoredStart.Int64
		}
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

// ClaimCalendarOccurrence serializes ordinary dispatch admission with calendar
// mutations. A blackout committed before admission prevents dispatch; a run
// already admitted is not cancelled by a subsequent pause.
func (r *ScheduleRepo) ClaimCalendarOccurrence(ctx context.Context, schedule models.Schedule, now time.Time, next *time.Time) (bool, error) {
	if schedule.NextRun == nil {
		return false, nil
	}
	claimed := false
	err := withImmediateTx(ctx, r.db, func(exec SQLExecutor) error {
		suppressed, _, err := suppressedScheduleOccurrence(ctx, exec, schedule.ID, *schedule.NextRun)
		if err != nil || suppressed {
			return err
		}
		result, err := exec.ExecContext(ctx, `UPDATE schedules SET last_run = ?, next_run = ?, updated_at = datetime('now')
   WHERE id = ? AND task_id = ? AND enabled = 1 AND next_run = ? AND next_run <= ?
   AND NOT EXISTS (SELECT 1 FROM schedule_dispatch_admissions a WHERE a.task_id = schedules.task_id)
   AND NOT EXISTS (SELECT 1 FROM executions e WHERE e.task_id = schedules.task_id AND e.status IN ('running','queued'))
   AND NOT EXISTS (SELECT 1 FROM automation_task_run_reservations a WHERE a.task_id = schedules.task_id)
   AND EXISTS (SELECT 1 FROM tasks WHERE id = schedules.task_id AND status NOT IN ('running','queued') AND category != 'chat' AND NOT `+taskThreadInputOwnsAdmissionPredicate+`)`,
			normalizeScheduleTime(now), normalizeScheduleNextRun(next), schedule.ID, schedule.TaskID, normalizeScheduleTime(*schedule.NextRun), normalizeScheduleTime(now))
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		task, err := getTaskWithExecutor(ctx, exec, `SELECT `+taskSelectColumns+` FROM tasks WHERE id = ?`, schedule.TaskID)
		if err != nil {
			return err
		}
		category := task.Category
		if category != models.CategoryActive {
			category = models.CategoryScheduled
		}
		order := task.DisplayOrder
		if category != task.Category {
			if err := exec.QueryRowContext(ctx, `SELECT COALESCE(MAX(display_order), -1)+1 FROM tasks WHERE project_id = ? AND category = ?`, task.ProjectID, category).Scan(&order); err != nil {
				return err
			}
		} else if category == models.CategoryActive && task.Status != models.StatusPending {
			if err := exec.QueryRowContext(ctx, activeBoardTailOrderQuery, task.ProjectID).Scan(&order); err != nil {
				return err
			}
		}
		if _, err := exec.ExecContext(ctx, `UPDATE tasks SET status = 'pending', category = ?, display_order = ?, completed_at = NULL, updated_at = datetime('now') WHERE id = ?`, category, order, task.ID); err != nil {
			return err
		}
		var executionID *string
		if task.SwarmRole != models.SwarmRoleParent {
			agent := ""
			if task.AgentID != nil {
				agent = *task.AgentID
			}
			execution := models.Execution{TaskID: task.ID, AgentConfigID: agent, Status: models.ExecQueued, PromptSent: task.Prompt, StartsNewContext: schedule.ClearContextOnStart}
			if err := NewExecutionRepo(r.db).CreateWithExecutor(ctx, exec, &execution); err != nil {
				return err
			}
			executionID = &execution.ID
		}
		if _, err := exec.ExecContext(ctx, `INSERT INTO schedule_dispatch_admissions(task_id,schedule_id,execution_id,starts_new_context) VALUES(?,?,?,?)`, task.ID, schedule.ID, executionID, schedule.ClearContextOnStart); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return claimed && err == nil, err
}

// ReleaseCalendarOccurrence restores a failed planner admission only if no
// intervening scheduler or user edit has changed its occurrence state.
func (r *ScheduleRepo) ReleaseCalendarOccurrence(ctx context.Context, schedule models.Schedule, admittedAt time.Time, next *time.Time) error {
	return withImmediateTx(ctx, r.db, func(exec SQLExecutor) error {
		result, err := exec.ExecContext(ctx, `UPDATE schedules SET last_run = ?, next_run = ?, updated_at = datetime('now') WHERE id = ? AND task_id = ? AND last_run = ? AND next_run IS ? AND EXISTS (SELECT 1 FROM schedule_dispatch_admissions a WHERE a.schedule_id = schedules.id)`, schedule.LastRun, schedule.NextRun, schedule.ID, schedule.TaskID, normalizeScheduleTime(admittedAt), normalizeScheduleNextRun(next))
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		_, err = exec.ExecContext(ctx, `DELETE FROM schedule_dispatch_admissions WHERE schedule_id = ?`, schedule.ID)
		return err
	})
}

// AdvanceSuppressedOccurrence rechecks the current blackout and advances under
// the same write lock as Unskip/Resume. A stale preflight cannot consume a run.
func (r *ScheduleRepo) AdvanceSuppressedOccurrence(ctx context.Context, schedule models.Schedule, now time.Time) (bool, error) {
	if schedule.NextRun == nil {
		return false, nil
	}
	suppressed := false
	err := withImmediateTx(ctx, r.db, func(exec SQLExecutor) error {
		var until time.Time
		var err error
		suppressed, until, err = suppressedScheduleOccurrence(ctx, exec, schedule.ID, *schedule.NextRun)
		if err != nil || !suppressed {
			return err
		}
		from := now
		if !until.IsZero() && !until.After(now) {
			from = until.Add(-time.Nanosecond)
		}
		_, err = exec.ExecContext(ctx, `UPDATE schedules SET next_run = ?, updated_at = datetime('now') WHERE id = ? AND task_id = ? AND enabled = 1 AND next_run = ?`, normalizeScheduleNextRun(schedule.ComputeNextRun(from)), schedule.ID, schedule.TaskID, normalizeScheduleTime(*schedule.NextRun))
		return err
	})
	return suppressed, err
}

func (r *ScheduleRepo) ListCalendarAdmissions(ctx context.Context) ([]ActiveLaneTaskAdmission, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT task_id, COALESCE(execution_id,''), starts_new_context FROM schedule_dispatch_admissions`)
	if err != nil {
		return nil, err
	}
	var refs []struct {
		taskID, executionID string
		clear               bool
	}
	for rows.Next() {
		var ref struct {
			taskID, executionID string
			clear               bool
		}
		if err := rows.Scan(&ref.taskID, &ref.executionID, &ref.clear); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, ref)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var result []ActiveLaneTaskAdmission
	for _, ref := range refs {
		task, err := NewTaskRepo(r.db, nil).GetByID(ctx, ref.taskID)
		if err != nil {
			return nil, err
		}
		if task != nil {
			task.StartsNewContext = ref.clear
			result = append(result, ActiveLaneTaskAdmission{Task: *task, ExecutionID: ref.executionID})
		}
	}
	return result, nil
}

func (r *ScheduleRepo) CompleteCalendarPlannerAdmission(ctx context.Context, taskID string) error {
	_, err := execBoundSQLite(ctx, r.db, `DELETE FROM schedule_dispatch_admissions WHERE task_id = ? AND execution_id IS NULL`, taskID)
	return err
}

// AdmitScheduledCalendarOccurrence keeps task board notifications on TaskRepo
// while ScheduleRepo owns the atomic schedule/execution reservation.
func (r *TaskRepo) AdmitScheduledCalendarOccurrence(ctx context.Context, schedule models.Schedule, now time.Time, next *time.Time) (bool, error) {
	claimed, err := NewScheduleRepo(r.db).ClaimCalendarOccurrence(ctx, schedule, now, next)
	if err != nil || !claimed {
		return claimed, err
	}
	if r.broadcaster != nil {
		task, err := r.GetByID(ctx, schedule.TaskID)
		if err == nil && task != nil {
			r.broadcaster.Publish(events.TaskEvent{Type: events.TaskBoardUpdated, TaskID: task.ID, TaskName: task.Title, ProjectID: task.ProjectID, Category: string(task.Category), Status: string(task.Status)})
		}
	}
	return true, nil
}

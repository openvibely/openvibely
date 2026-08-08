package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type TaskGoalRepo struct {
	db *sql.DB
}

func NewTaskGoalRepo(db *sql.DB) *TaskGoalRepo {
	return &TaskGoalRepo{db: db}
}

const taskGoalSelectColumns = `task_id, goal_id, objective, status, reason, blocker_key, blocker_count, blocker_reason, blocker_last_seen_at, last_checked_at, achieved_at, created_at, updated_at`

type taskGoalScanner interface {
	Scan(dest ...any) error
}

func scanTaskGoal(scanner taskGoalScanner) (*models.TaskGoal, error) {
	var g models.TaskGoal
	var blockerLastSeenAt sql.NullString
	var lastCheckedAt sql.NullString
	var achievedAt sql.NullString
	var createdAt sql.NullString
	var updatedAt sql.NullString
	if err := scanner.Scan(
		&g.TaskID,
		&g.GoalID,
		&g.Objective,
		&g.Status,
		&g.Reason,
		&g.BlockerKey,
		&g.BlockerCount,
		&g.BlockerReason,
		&blockerLastSeenAt,
		&lastCheckedAt,
		&achievedAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	g.BlockerLastSeenAt = parseOptionalSQLiteTime(blockerLastSeenAt)
	g.LastCheckedAt = parseOptionalSQLiteTime(lastCheckedAt)
	g.AchievedAt = parseOptionalSQLiteTime(achievedAt)
	if parsed := parseOptionalSQLiteTime(createdAt); parsed != nil {
		g.CreatedAt = *parsed
	}
	if parsed := parseOptionalSQLiteTime(updatedAt); parsed != nil {
		g.UpdatedAt = *parsed
	}
	return &g, nil
}

func parseOptionalSQLiteTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, value.String); err == nil {
			return &t
		}
	}
	return nil
}

func (r *TaskGoalRepo) CreateOrReplace(ctx context.Context, goal *models.TaskGoal) error {
	if goal == nil {
		return fmt.Errorf("goal is nil")
	}
	if goal.GoalID == "" {
		return fmt.Errorf("goal_id is required")
	}
	if goal.Status == "" {
		goal.Status = models.TaskGoalStatusActive
	}
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO task_goals (task_id, goal_id, objective, status, reason, blocker_key, blocker_count, blocker_reason, blocker_last_seen_at, last_checked_at, achieved_at)
		VALUES (?, ?, ?, ?, ?, '', 0, '', NULL, NULL, NULL)
		ON CONFLICT(task_id) DO UPDATE SET
			goal_id = excluded.goal_id,
			objective = excluded.objective,
			status = excluded.status,
			reason = excluded.reason,
			blocker_key = '',
			blocker_count = 0,
			blocker_reason = '',
			blocker_last_seen_at = NULL,
			last_checked_at = NULL,
			achieved_at = NULL,
			updated_at = datetime('now')
		RETURNING `+taskGoalSelectColumns,
		goal.TaskID, goal.GoalID, goal.Objective, goal.Status, goal.Reason)
	created, err := scanTaskGoal(row)
	if err != nil {
		return fmt.Errorf("creating task goal: %w", err)
	}
	*goal = *created
	return nil
}

func (r *TaskGoalRepo) GetByTaskID(ctx context.Context, taskID string) (*models.TaskGoal, error) {
	goal, err := scanTaskGoal(r.db.QueryRowContext(ctx, `SELECT `+taskGoalSelectColumns+` FROM task_goals WHERE task_id = ?`, taskID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting task goal: %w", err)
	}
	return goal, nil
}

func (r *TaskGoalRepo) UpdateStatus(ctx context.Context, taskID string, goalID string, status models.TaskGoalStatus, reason string, clearAudit bool) (*models.TaskGoal, error) {
	if clearAudit {
		return r.updateReturning(ctx, `
			UPDATE task_goals
			SET status = ?, reason = ?, blocker_key = '', blocker_count = 0, blocker_reason = '', blocker_last_seen_at = NULL,
				last_checked_at = datetime('now'), achieved_at = NULL, updated_at = datetime('now')
			WHERE task_id = ? AND goal_id = ?
			RETURNING `+taskGoalSelectColumns, status, reason, taskID, goalID)
	}
	return r.updateReturning(ctx, `
		UPDATE task_goals
		SET status = ?, reason = ?, last_checked_at = datetime('now'), updated_at = datetime('now')
		WHERE task_id = ? AND goal_id = ?
		RETURNING `+taskGoalSelectColumns, status, reason, taskID, goalID)
}

// ResumeStoppedByUser atomically transitions a goal to active only when it is
// still paused with the given stopped-by-user reason at write time. It returns
// nil without error when no row matches, so a concurrent clear, replacement,
// achieved transition, or pause with another reason is preserved as a no-op.
func (r *TaskGoalRepo) ResumeStoppedByUser(ctx context.Context, taskID string, goalID string, stoppedReason string, reason string) (*models.TaskGoal, error) {
	return r.updateReturning(ctx, `
		UPDATE task_goals
		SET status = 'active', reason = ?, blocker_key = '', blocker_count = 0, blocker_reason = '', blocker_last_seen_at = NULL,
			last_checked_at = datetime('now'), achieved_at = NULL, updated_at = datetime('now')
		WHERE task_id = ? AND goal_id = ? AND status = 'paused' AND reason = ?
		RETURNING `+taskGoalSelectColumns, reason, taskID, goalID, stoppedReason)
}

func (r *TaskGoalRepo) ReactivateAchieved(ctx context.Context, taskID string, reason string) (*models.TaskGoal, error) {
	return r.updateReturning(ctx, `
		UPDATE task_goals
		SET status = 'active', reason = ?, blocker_key = '', blocker_count = 0, blocker_reason = '', blocker_last_seen_at = NULL,
			last_checked_at = datetime('now'), achieved_at = NULL, updated_at = datetime('now')
		WHERE task_id = ? AND status = 'achieved'
		RETURNING `+taskGoalSelectColumns, reason, taskID)
}

func (r *TaskGoalRepo) MarkAchieved(ctx context.Context, taskID string, goalID string, reason string) (*models.TaskGoal, error) {
	return r.updateReturning(ctx, `
		UPDATE task_goals
		SET status = 'achieved', reason = ?, blocker_key = '', blocker_count = 0, blocker_reason = '', blocker_last_seen_at = NULL,
			last_checked_at = datetime('now'), achieved_at = datetime('now'), updated_at = datetime('now')
		WHERE task_id = ? AND goal_id = ? AND status = 'active'
		RETURNING `+taskGoalSelectColumns, reason, taskID, goalID)
}

func (r *TaskGoalRepo) RecordBlockedReport(ctx context.Context, taskID string, goalID string, blockerKey string, reason string) (*models.TaskGoal, error) {
	return r.updateReturning(ctx, `
		UPDATE task_goals
		SET blocker_key = ?,
			blocker_count = CASE WHEN blocker_key = ? THEN blocker_count + 1 ELSE 1 END,
			blocker_reason = ?,
			blocker_last_seen_at = datetime('now'),
			last_checked_at = datetime('now'),
			reason = ?,
			status = CASE WHEN (CASE WHEN blocker_key = ? THEN blocker_count + 1 ELSE 1 END) >= 3 THEN 'blocked' ELSE 'active' END,
			updated_at = datetime('now')
		WHERE task_id = ? AND goal_id = ? AND status = 'active'
		RETURNING `+taskGoalSelectColumns, blockerKey, blockerKey, reason, reason, blockerKey, taskID, goalID)
}

func (r *TaskGoalRepo) Clear(ctx context.Context, taskID string, reason string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE task_goals
		SET status = 'cleared', reason = ?, updated_at = datetime('now')
		WHERE task_id = ?`, reason, taskID)
	if err != nil {
		return fmt.Errorf("clearing task goal: %w", err)
	}
	return nil
}

func (r *TaskGoalRepo) Delete(ctx context.Context, taskID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM task_goals WHERE task_id = ?`, taskID)
	if err != nil {
		return fmt.Errorf("deleting task goal: %w", err)
	}
	return nil
}

func (r *TaskGoalRepo) updateReturning(ctx context.Context, query string, args ...any) (*models.TaskGoal, error) {
	goal, err := scanTaskGoal(r.db.QueryRowContext(ctx, query, args...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("updating task goal: %w", err)
	}
	return goal, nil
}

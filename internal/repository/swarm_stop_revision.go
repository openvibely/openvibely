package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// Preserve the largest parent Stop revision when an orchestration update
// writes a config snapshot read before a newer child execution was admitted.
const monotonicParentStopRevisionSQL = `CASE WHEN ? = 'swarm_parent' THEN
	json_set(?, '$.stop_revision', MAX(
		COALESCE(json_extract(COALESCE(NULLIF(swarm_config, ''), '{}'), '$.stop_revision'), 0),
		COALESCE(json_extract(?, '$.stop_revision'), 0)))
	ELSE ? END`

// lockSwarmParentFollowup gates a child execution admission with the parent's
// Stop observation and cancellation. The caller must take this before a child
// lifecycle lock or database transaction.
func lockSwarmParentFollowup(ctx context.Context, db *sql.DB, taskID string, isFollowup bool) (func(), error) {
	if !isFollowup {
		return func() {}, nil
	}
	var parentID sql.NullString
	err := db.QueryRowContext(ctx, `SELECT parent_task_id FROM tasks
		WHERE id = ? AND swarm_role IN ('planner', 'worker', 'reviewer', 'merger', 'integrator')`, taskID).Scan(&parentID)
	if err == sql.ErrNoRows || (err == nil && (!parentID.Valid || parentID.String == "")) {
		return func() {}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading swarm parent for follow-up admission: %w", err)
	}
	return LockTaskLifecycle(parentID.String), nil
}

// bumpSwarmParentStopRevision runs in the same transaction as the child
// execution INSERT. A Stop observed before that INSERT must not cascade into
// the newly admitted child, even before routing its swarm state completes.
func bumpSwarmParentStopRevision(ctx context.Context, exec SQLExecutor, taskID string) error {
	_, err := exec.ExecContext(ctx, `UPDATE tasks
		SET swarm_config = json_set(
			COALESCE(NULLIF(swarm_config, ''), '{}'), '$.stop_revision',
			COALESCE(json_extract(COALESCE(NULLIF(swarm_config, ''), '{}'), '$.stop_revision'), 0) + 1),
			updated_at = datetime('now')
		WHERE id = (SELECT parent_task_id FROM tasks
			WHERE id = ? AND swarm_role IN ('planner', 'worker', 'reviewer', 'merger', 'integrator'))
		  AND swarm_role = 'swarm_parent'`, taskID)
	if err != nil {
		return fmt.Errorf("advancing swarm parent Stop revision: %w", err)
	}
	return nil
}

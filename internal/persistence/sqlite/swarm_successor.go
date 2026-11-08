package sqlite

import (
	"context"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// PrepareSwarmSuccessor commits readiness, board placement and the audit as one
// unit. Submission remains outside the transaction and rechecks authorization.
func (r *TaskRepo) PrepareSwarmSuccessor(ctx context.Context, parentID, sourceID string, target *models.Task) error {
	if target == nil {
		return persistence.ErrProjectDenied
	}
	var before *models.Task
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		if err := authorizeSwarmWrite(ctx, tx, target.ID); err != nil {
			return err
		}
		parent, err := getTaskWithExecutor(ctx, tx, `SELECT `+taskSelectColumns+` FROM tasks WHERE id=?`, parentID)
		if err != nil {
			return err
		}
		source, err := getTaskWithExecutor(ctx, tx, `SELECT `+taskSelectColumns+` FROM tasks WHERE id=?`, sourceID)
		if err != nil {
			return err
		}
		before, err = getTaskWithExecutor(ctx, tx, `SELECT `+taskSelectColumns+` FROM tasks WHERE id=?`, target.ID)
		if err != nil {
			return err
		}
		if parent == nil || source == nil || before == nil || source.ParentTaskID == nil || before.ParentTaskID == nil ||
			*source.ParentTaskID != parentID || *before.ParentTaskID != parentID || source.ID == before.ID ||
			source.ProjectID != parent.ProjectID || before.ProjectID != parent.ProjectID || target.ProjectID != before.ProjectID ||
			source.Status != models.StatusCompleted || before.Status != target.Status || before.Category != target.Category || before.SwarmRole != target.SwarmRole ||
			(before.SwarmRole != models.SwarmRoleReviewer && before.SwarmRole != models.SwarmRoleMerger && before.SwarmRole != models.SwarmRoleLegacyIntegrator) ||
			before.Status == models.StatusRunning || before.Status == models.StatusPending || before.Status == models.StatusQueued {
			return persistence.ErrProjectDenied
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM executions WHERE task_id=? AND status IN ('queued','running')`, target.ID).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return persistence.ErrProjectDenied
		}
		var order int
		if err := tx.QueryRowContext(ctx, activeBoardTailOrderQuery, parent.ProjectID).Scan(&order); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET swarm_status='ready',swarm_config=?,swarm_sequence=?,category='active',status='pending',display_order=?,completed_at=NULL,updated_at=datetime('now') WHERE id=?`, defaultJSON(target.SwarmConfig), target.SwarmSequence, order, target.ID); err != nil {
			return err
		}
		return auditSwarmWrite(ctx, tx, target.ID, "swarm.successor_prepared")
	})
	if err != nil {
		return err
	}
	if r.broadcaster != nil {
		if before.Category != models.CategoryActive {
			r.broadcaster.Publish(events.TaskEvent{Type: events.TaskCategoryChanged, TaskID: target.ID, TaskName: before.Title, ProjectID: before.ProjectID, Category: string(models.CategoryActive), OldCategory: string(before.Category)})
		}
		r.broadcaster.Publish(events.TaskEvent{Type: events.TaskStatusChanged, TaskID: target.ID, TaskName: before.Title, ProjectID: before.ProjectID, Status: string(models.StatusPending), OldStatus: string(before.Status), Category: string(models.CategoryActive)})
	}
	return nil
}

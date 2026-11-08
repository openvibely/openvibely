package sqlite

import (
	"context"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

func sameSwarmObservation(current, observed *models.Task) bool {
	if current == nil || observed == nil {
		return false
	}
	if current.ProjectID != observed.ProjectID || current.SwarmRole != observed.SwarmRole || current.Status != observed.Status || current.Category != observed.Category || current.SwarmConfig != observed.SwarmConfig || current.SwarmStatus != observed.SwarmStatus || current.SwarmSequence != observed.SwarmSequence {
		return false
	}
	if current.ParentTaskID == nil || observed.ParentTaskID == nil {
		return current.ParentTaskID == nil && observed.ParentTaskID == nil
	}
	return *current.ParentTaskID == *observed.ParentTaskID
}

// PrepareSwarmRerun applies the parent generation change and child readiness
// together. Observed state prevents a stale service decision overwriting a stop
// or another generation change made before the transaction acquired its lock.
func (r *TaskRepo) PrepareSwarmRerun(ctx context.Context, parent, child *models.Task, parentConfig, childConfig string) error {
	if parent == nil || child == nil || parent.ID == child.ID {
		return persistence.ErrProjectDenied
	}
	parentStatus, childStatus := "needs_integration", "rerun_integration"
	if child.SwarmRole == models.SwarmRoleReviewer {
		parentStatus, childStatus = "needs_review", "rerun_review"
	} else if child.SwarmRole != models.SwarmRoleMerger && child.SwarmRole != models.SwarmRoleLegacyIntegrator {
		return persistence.ErrProjectDenied
	}
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		if err := authorizeSwarmWrite(ctx, tx, parent.ID); err != nil {
			return err
		}
		if err := authorizeSwarmWrite(ctx, tx, child.ID); err != nil {
			return err
		}
		savedParent, err := getTaskWithExecutor(ctx, tx, `SELECT `+taskSelectColumns+` FROM tasks WHERE id=?`, parent.ID)
		if err != nil {
			return err
		}
		savedChild, err := getTaskWithExecutor(ctx, tx, `SELECT `+taskSelectColumns+` FROM tasks WHERE id=?`, child.ID)
		if err != nil {
			return err
		}
		if !sameSwarmObservation(savedParent, parent) || !sameSwarmObservation(savedChild, child) || child.ParentTaskID == nil || *child.ParentTaskID != parent.ID || child.ProjectID != parent.ProjectID || parent.SwarmRole != models.SwarmRoleParent || child.Status == models.StatusPending || child.Status == models.StatusQueued || child.Status == models.StatusRunning {
			return persistence.ErrProjectDenied
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM executions WHERE task_id=? AND status IN ('queued','running')`, child.ID).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return persistence.ErrProjectDenied
		}
		// Both configs must be valid before any state is changed.
		if _, err := models.ParseSwarmConfig(parentConfig); err != nil {
			return err
		}
		if _, err := models.ParseSwarmConfig(childConfig); err != nil {
			return err
		}
		var order int
		if err := tx.QueryRowContext(ctx, activeBoardTailOrderQuery, parent.ProjectID).Scan(&order); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET swarm_status=?,swarm_config=?,category='active',status='running',display_order=?,completed_at=NULL,updated_at=datetime('now') WHERE id=?`, parentStatus, defaultJSON(parentConfig), order, parent.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET swarm_status=?,swarm_config=?,category='active',status='pending',display_order=?,completed_at=NULL,updated_at=datetime('now') WHERE id=?`, childStatus, defaultJSON(childConfig), order+1, child.ID); err != nil {
			return err
		}
		return auditSwarmWrite(ctx, tx, child.ID, "swarm.rerun_prepared")
	})
	if err != nil {
		return err
	}
	if r.broadcaster != nil {
		for _, task := range []*models.Task{parent, child} {
			status := models.StatusPending
			if task.ID == parent.ID {
				status = models.StatusRunning
			}
			if task.Category != models.CategoryActive {
				r.broadcaster.Publish(events.TaskEvent{Type: events.TaskCategoryChanged, TaskID: task.ID, TaskName: task.Title, ProjectID: task.ProjectID, Category: string(models.CategoryActive), OldCategory: string(task.Category)})
			}
			r.broadcaster.Publish(events.TaskEvent{Type: events.TaskStatusChanged, TaskID: task.ID, TaskName: task.Title, ProjectID: task.ProjectID, Category: string(models.CategoryActive), Status: string(status), OldStatus: string(task.Status)})
		}
	}
	return nil
}

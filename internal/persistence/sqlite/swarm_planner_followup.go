package sqlite

import (
	"context"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

func (r *TaskRepo) PreparePlannerFollowup(ctx context.Context, parent, child, next *models.Task, parentConfig string) error {
	if parent == nil || child == nil || next == nil || next.ID != child.ID || parent.ID == child.ID {
		return persistence.ErrProjectDenied
	}
	parentStatus, childStatus := "needs_coordination", "coordinating"
	if child.SwarmRole != models.SwarmRolePlanner {
		return persistence.ErrProjectDenied
	}
	childConfig := next.SwarmConfig
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
		if !sameSwarmObservation(savedParent, parent) || !sameSwarmObservation(savedChild, child) || child.ParentTaskID == nil || *child.ParentTaskID != parent.ID || child.ProjectID != parent.ProjectID || parent.SwarmRole != models.SwarmRoleParent || child.Status == models.StatusQueued || child.Status == models.StatusRunning {
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
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET swarm_status=?,swarm_config=?,prompt=?,agent_id=?,category='active',status='pending',display_order=?,completed_at=NULL,updated_at=datetime('now') WHERE id=?`, childStatus, defaultJSON(childConfig), next.Prompt, next.AgentID, order+1, child.ID); err != nil {
			return err
		}
		return auditSwarmWrite(ctx, tx, child.ID, "swarm.planner_followup_prepared")
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

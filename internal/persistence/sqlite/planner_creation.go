package sqlite

import (
	"context"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

func prepareFirstPlannerParent(ctx context.Context, tx sqlExecutor, child models.Task) error {
	observed, ok := persistence.PlannerCreationParent(ctx)
	if !ok {
		return nil
	}
	if child.SwarmRole != models.SwarmRolePlanner || child.ParentTaskID == nil || *child.ParentTaskID != observed.ID || child.ProjectID != observed.ProjectID || observed.SwarmRole != models.SwarmRoleParent {
		return persistence.ErrProjectDenied
	}
	if err := authorizeSwarmWrite(ctx, tx, observed.ID); err != nil {
		return err
	}
	current, err := getTaskWithExecutor(ctx, tx, `SELECT `+taskSelectColumns+` FROM tasks WHERE id=?`, observed.ID)
	if err != nil {
		return err
	}
	if !sameSwarmObservation(current, &observed) {
		return persistence.ErrProjectDenied
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE parent_task_id=? AND swarm_role='planner'`, observed.ID).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return persistence.ErrProjectDenied
	}
	var order int
	if err := tx.QueryRowContext(ctx, activeBoardTailOrderQuery, observed.ProjectID).Scan(&order); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET category='active',status='blocked',display_order=?,completed_at=NULL,updated_at=datetime('now') WHERE id=?`, order, observed.ID); err != nil {
		return err
	}
	return auditSwarmWrite(ctx, tx, observed.ID, "swarm.first_planner_prepared")
}

func (r *TaskRepo) publishFirstPlannerParent(ctx context.Context) {
	parent, ok := persistence.PlannerCreationParent(ctx)
	if !ok || r.broadcaster == nil {
		return
	}
	if parent.Category != models.CategoryActive {
		r.broadcaster.Publish(events.TaskEvent{Type: events.TaskCategoryChanged, TaskID: parent.ID, TaskName: parent.Title, ProjectID: parent.ProjectID, Category: string(models.CategoryActive), OldCategory: string(parent.Category)})
	}
	if parent.Status != models.StatusBlocked {
		r.broadcaster.Publish(events.TaskEvent{Type: events.TaskStatusChanged, TaskID: parent.ID, TaskName: parent.Title, ProjectID: parent.ProjectID, Category: string(models.CategoryActive), Status: string(models.StatusBlocked), OldStatus: string(parent.Status)})
	}
}

package repository

import (
	"context"
	"errors"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
)

// ReserveCalendarPlanner checks the admission, prepares the planner, and saves
// its queued execution under the same writer lock as cancellation.
func (r *TaskRepo) ReserveCalendarPlanner(ctx context.Context, parentID string, makePlanner func(*models.Task) (*models.Task, error)) (*ActiveLaneTaskAdmission, error) {
	var admission *ActiveLaneTaskAdmission
	var parent *models.Task
	err := withImmediateTx(ctx, r.db, func(exec SQLExecutor) error {
		var live, clear bool
		if err := exec.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schedule_dispatch_admissions a JOIN tasks t ON t.id = a.task_id
   WHERE a.task_id = ? AND a.execution_id IS NULL AND t.swarm_role = 'swarm_parent' AND t.status = 'pending' AND t.category IN ('active','scheduled'))`, parentID).Scan(&live); err != nil {
			return err
		}
		if !live {
			return nil
		}
		if err := exec.QueryRowContext(ctx, `SELECT starts_new_context FROM schedule_dispatch_admissions WHERE task_id = ?`, parentID).Scan(&clear); err != nil {
			return err
		}
		var err error
		parent, err = getTaskWithExecutor(ctx, exec, `SELECT `+taskSelectColumns+` FROM tasks WHERE id = ?`, parentID)
		if err != nil {
			return err
		}
		planner, err := getTaskWithExecutor(ctx, exec, `SELECT `+taskSelectColumns+` FROM tasks WHERE parent_task_id = ? AND swarm_role = 'planner' ORDER BY swarm_sequence, created_at LIMIT 1`, parentID)
		if err != nil {
			return err
		}
		if planner == nil {
			planner, err = makePlanner(parent)
			if err != nil {
				return err
			}
			title := planner.Title
			for attempt := 0; ; attempt++ {
				if attempt > 0 {
					planner.Title = title + " · " + NewID()
				}
				err = r.CreateWithExecutor(ctx, exec, planner)
				if !errors.Is(err, ErrDuplicateTask) {
					break
				}
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if err != nil {
				return err
			}
		} else {
			var busy bool
			if err := exec.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM executions WHERE task_id = ? AND status IN ('queued','running')) OR EXISTS(SELECT 1 FROM automation_task_run_reservations WHERE task_id = ?)`, planner.ID, planner.ID).Scan(&busy); err != nil {
				return err
			}
			if busy || planner.Status == models.StatusRunning || planner.Status == models.StatusQueued || planner.Status == models.StatusBlocked {
				return nil
			}
			if _, err := exec.ExecContext(ctx, `UPDATE tasks SET status = 'pending', category = 'active', completed_at = NULL, display_order = `+activeBoardTailOrderExpression+`, updated_at = datetime('now') WHERE id = ?`, planner.ID); err != nil {
				return err
			}
			planner.Status, planner.Category = models.StatusPending, models.CategoryActive
		}
		if _, err := exec.ExecContext(ctx, `UPDATE tasks SET status = 'blocked', category = 'active', completed_at = NULL, display_order = `+activeBoardTailOrderExpression+`, updated_at = datetime('now') WHERE id = ?`, parentID); err != nil {
			return err
		}
		parent.Status, parent.Category = models.StatusBlocked, models.CategoryActive
		agent := ""
		if planner.AgentID != nil {
			agent = *planner.AgentID
		}
		execution := models.Execution{TaskID: planner.ID, AgentConfigID: agent, Status: models.ExecQueued, PromptSent: planner.Prompt, StartsNewContext: clear}
		if err := NewExecutionRepo(r.db).CreateWithExecutor(ctx, exec, &execution); err != nil {
			return err
		}
		if _, err := exec.ExecContext(ctx, `UPDATE schedule_dispatch_admissions SET task_id = ?, execution_id = ? WHERE task_id = ? AND execution_id IS NULL`, planner.ID, execution.ID, parentID); err != nil {
			return err
		}
		planner.StartsNewContext = clear
		admission = &ActiveLaneTaskAdmission{Task: *planner, ExecutionID: execution.ID}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if admission != nil && r.broadcaster != nil {
		for _, task := range []*models.Task{parent, &admission.Task} {
			r.broadcaster.Publish(events.TaskEvent{Type: events.TaskBoardUpdated, TaskID: task.ID, TaskName: task.Title, ProjectID: task.ProjectID, Category: string(task.Category), Status: string(task.Status)})
		}
	}
	return admission, nil
}

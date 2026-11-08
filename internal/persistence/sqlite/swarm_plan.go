package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// ApplySwarmPlan commits a planner result (parent, planner, existing and new
// children) in one transaction so a failure or revocation leaves no partial plan.
func (r *TaskRepo) ApplySwarmPlan(ctx context.Context, commit persistence.SwarmPlanCommit) error {
	parent, planner := commit.Parent, commit.Planner
	if parent.ID == "" || planner.ID == "" || parent.SwarmRole != models.SwarmRoleParent || planner.SwarmRole != models.SwarmRolePlanner || planner.ParentTaskID == nil || *planner.ParentTaskID != parent.ID || planner.ProjectID != parent.ProjectID {
		return persistence.ErrProjectDenied
	}
	var changed []events.TaskEvent
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		changed = nil
		for _, id := range []string{parent.ID, planner.ID} {
			if err := authorizeSwarmWrite(ctx, tx, id); err != nil {
				return err
			}
		}
		savedParent, err := getTaskWithExecutor(ctx, tx, `SELECT `+taskSelectColumns+` FROM tasks WHERE id=?`, parent.ID)
		if err != nil {
			return err
		}
		savedPlanner, err := getTaskWithExecutor(ctx, tx, `SELECT `+taskSelectColumns+` FROM tasks WHERE id=?`, planner.ID)
		if err != nil {
			return err
		}
		if !sameSwarmObservation(savedParent, &parent) || !sameSwarmObservation(savedPlanner, &planner) {
			return persistence.ErrProjectDenied
		}
		for _, write := range commit.Writes {
			event, err := applySwarmTaskWrite(ctx, tx, parent, write, commit.Title)
			if err != nil {
				return err
			}
			if event != nil {
				changed = append(changed, *event)
			}
		}
		for _, child := range commit.Creates {
			if err := createSwarmPlanChild(ctx, r, tx, parent, child, commit); err != nil {
				return err
			}
		}
		return auditSwarmWrite(ctx, tx, parent.ID, commit.Action)
	})
	if err != nil {
		return err
	}
	if r.broadcaster != nil {
		for _, event := range changed {
			r.broadcaster.Publish(event)
		}
	}
	return nil
}

func applySwarmTaskWrite(ctx context.Context, tx sqlExecutor, parent models.Task, write persistence.SwarmTaskWrite, title func(int, string, int) string) (*events.TaskEvent, error) {
	current, err := getTaskWithExecutor(ctx, tx, `SELECT `+taskSelectColumns+` FROM tasks WHERE id=?`, write.ID)
	if err != nil {
		return nil, err
	}
	if current == nil || current.ProjectID != parent.ProjectID || (current.ID != parent.ID && (current.ParentTaskID == nil || *current.ParentTaskID != parent.ID)) {
		return nil, persistence.ErrProjectDenied
	}
	if _, err := models.ParseSwarmConfig(write.SwarmConfig); err != nil {
		return nil, err
	}
	status, category := current.Status, current.Category
	if write.Status != "" {
		status = write.Status
	}
	if write.Category != "" {
		category = write.Category
	}
	order := current.DisplayOrder
	if (status != current.Status || category != current.Category) && category == models.CategoryActive && isActiveBoardStatus(status) {
		if err := tx.QueryRowContext(ctx, activeBoardTailOrderQuery, current.ProjectID).Scan(&order); err != nil {
			return nil, err
		}
	}
	prompt := current.Prompt
	if write.Prompt != "" {
		prompt = write.Prompt
	}
	config := defaultJSON(write.SwarmConfig)
	for attempt := 0; ; attempt++ {
		name := current.Title
		if write.Title != "" {
			name = title(current.SwarmSequence, write.Title, attempt)
		}
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET swarm_status=?,swarm_config=`+monotonicParentStopRevisionSQL+`,status=?,category=?,display_order=?,prompt=?,title=?,
			title_explicitly_set=CASE WHEN ? THEN 1 ELSE title_explicitly_set END,updated_at=datetime('now') WHERE id=?`,
			write.SwarmStatus, current.SwarmRole, config, config, config, status, category, order, prompt, name, write.Title != "", write.ID)
		if err == nil || write.Title == "" || !isDuplicateTaskTitle(err) {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, fmt.Errorf("applying swarm plan to task %s: %w", write.ID, err)
	}
	if status != models.StatusRunning && status != current.Status {
		if _, err := tx.ExecContext(ctx, `UPDATE executions SET status='cancelled', error_message='Task left the reserved running lane', completed_at=datetime('now')
			WHERE task_id=? AND status='queued' AND is_followup=0 AND dispatch_id IS NULL`, write.ID); err != nil {
			return nil, err
		}
	}
	if status == current.Status && category == current.Category {
		return nil, nil
	}
	return &events.TaskEvent{Type: events.TaskStatusChanged, TaskID: current.ID, TaskName: current.Title, ProjectID: current.ProjectID, Category: string(category), Status: string(status), OldStatus: string(current.Status), OldCategory: string(current.Category)}, nil
}

func createSwarmPlanChild(ctx context.Context, r *TaskRepo, tx sqlExecutor, parent models.Task, child *models.Task, commit persistence.SwarmPlanCommit) error {
	if child.ParentTaskID == nil || *child.ParentTaskID != parent.ID || child.ProjectID != parent.ProjectID || !models.IsSwarmChildRole(child.SwarmRole) || child.SwarmRole == models.SwarmRolePlanner {
		return persistence.ErrProjectDenied
	}
	base := child.Title
	for attempt := 0; ; attempt++ {
		child.Title = commit.Title(child.SwarmSequence, base, attempt)
		err := r.createWithExecutor(ctx, tx, child)
		if err == nil {
			break
		}
		if !errors.Is(err, persistence.ErrDuplicateTask) {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if commit.Worktree == nil || child.WorktreePath == "" {
		return nil
	}
	child.WorktreePath, child.WorktreeBranch = commit.Worktree(child)
	_, err := tx.ExecContext(ctx, `UPDATE tasks SET worktree_path=?,worktree_branch=? WHERE id=?`, child.WorktreePath, child.WorktreeBranch, child.ID)
	return err
}

func isDuplicateTaskTitle(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: tasks.project_id, tasks.title")
}

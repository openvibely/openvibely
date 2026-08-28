package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
)

var ErrAutomationChainChildBusy = errors.New("automation chained task is already running")

func AutomationCompilerTaskCreatedVia(automationID, nodeKey string) string {
	return "automation:" + automationID + ":" + nodeKey
}

func IsAutomationTaskCreatedVia(createdVia string) bool {
	parts := strings.Split(strings.TrimSpace(createdVia), ":")
	return len(parts) >= 3 && parts[0] == "automation" && strings.TrimSpace(parts[1]) != "" && strings.TrimSpace(parts[2]) != ""
}

// ActivateAutomationChainedTask reuses the existing Task state transition and
// records the connected Automation handoff in the same local transaction.
func (r *TaskRepo) ActivateAutomationChainedTask(ctx context.Context, parent models.Task, child *models.Task, event AutomationProjectionEvent) (*models.AutomationWorkItem, *models.AutomationActivity, bool, error) {
	if r == nil || child == nil || parent.ID == "" || child.ID == "" || event.Context.ProjectID == "" || event.EventKey == "" {
		return nil, nil, false, errors.New("complete automation task handoff is required")
	}
	conn, finishImmediate, err := beginImmediateConn(ctx, r.db)
	if err != nil {
		return nil, nil, false, err
	}
	defer finishImmediate()

	var parentProject, parentCreatedVia string
	if err := conn.QueryRowContext(ctx, `SELECT project_id, created_via FROM tasks WHERE id = ?`, parent.ID).Scan(&parentProject, &parentCreatedVia); err != nil {
		return nil, nil, false, fmt.Errorf("loading automation chain parent: %w", err)
	}
	var childProject, childCreatedVia, childParentID string
	var previousStatus models.TaskStatus
	var previousCategory models.TaskCategory
	if err := conn.QueryRowContext(ctx, `SELECT project_id, status, category, COALESCE(parent_task_id, ''), created_via FROM tasks WHERE id = ?`, child.ID).
		Scan(&childProject, &previousStatus, &previousCategory, &childParentID, &childCreatedVia); err != nil {
		return nil, nil, false, fmt.Errorf("loading automation chain child: %w", err)
	}
	var targetNodeKey string
	if err := conn.QueryRowContext(ctx, `SELECT node_key FROM automation_nodes WHERE id = ? AND project_id = ? AND automation_id = ? AND version_id = ?`,
		event.Binding.NodeID, event.Context.ProjectID, event.Binding.AutomationID, event.Binding.VersionID).Scan(&targetNodeKey); err != nil {
		return nil, nil, false, fmt.Errorf("loading automation chain target node: %w", err)
	}
	expectedChildCreatedVia := AutomationCompilerTaskCreatedVia(event.Binding.AutomationID, targetNodeKey)
	if parentProject != event.Context.ProjectID || childProject != event.Context.ProjectID || childParentID != parent.ID ||
		!strings.HasPrefix(parentCreatedVia, "automation:"+event.Binding.AutomationID+":") || childCreatedVia != expectedChildCreatedVia {
		return nil, nil, false, errors.New("automation task handoff does not match the published topology")
	}
	var publishedHandoff int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_edges edge
		JOIN automation_nodes source ON source.id = edge.source_node_id AND source.project_id = edge.project_id
			AND source.automation_id = edge.automation_id AND source.version_id = edge.version_id
			AND (source.node_type = 'trigger' OR source.node_type = 'agent_task' AND source.role = 'task')
		JOIN automation_nodes target ON target.id = edge.target_node_id AND target.project_id = edge.project_id
			AND target.automation_id = edge.automation_id AND target.version_id = edge.version_id AND target.node_type = 'agent_task'
		JOIN automation_definition_resources source_resource ON source_resource.project_id = edge.project_id
			AND source_resource.automation_id = edge.automation_id AND source_resource.version_id = edge.version_id
			AND source_resource.node_id = source.id AND source_resource.resource_type = 'task' AND source_resource.resource_id = ?
		JOIN automation_definition_resources target_resource ON target_resource.project_id = edge.project_id
			AND target_resource.automation_id = edge.automation_id AND target_resource.version_id = edge.version_id
			AND target_resource.node_id = target.id AND target_resource.resource_type = 'task' AND target_resource.resource_id = ?
		WHERE edge.project_id = ? AND edge.automation_id = ? AND edge.version_id = ?
		AND edge.source_node_id = ? AND edge.target_node_id = ?`, parent.ID, child.ID, event.Context.ProjectID,
		event.Binding.AutomationID, event.Binding.VersionID, event.FromNodeID, event.Binding.NodeID).Scan(&publishedHandoff); err != nil {
		return nil, nil, false, err
	}
	if publishedHandoff != 1 {
		return nil, nil, false, errors.New("automation task handoff is not connected in the published topology")
	}
	var lifecycle models.AutomationLifecycleState
	var currentVersionID string
	if err := conn.QueryRowContext(ctx, `SELECT lifecycle_state, COALESCE(published_version_id, '') FROM automations
		WHERE project_id = ? AND id = ?`, event.Context.ProjectID, event.Binding.AutomationID).Scan(&lifecycle, &currentVersionID); err != nil {
		return nil, nil, false, fmt.Errorf("loading automation lifecycle for task handoff: %w", err)
	}
	if currentVersionID != event.Binding.VersionID {
		return nil, nil, false, errors.New("automation task handoff no longer belongs to the current saved graph")
	}
	var existingEvent int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_transitions
		WHERE automation_id = ? AND version_id = ? AND event_key = ?`, event.Binding.AutomationID, event.Binding.VersionID, event.EventKey).Scan(&existingEvent); err != nil {
		return nil, nil, false, err
	}
	if existingEvent > 0 {
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return nil, nil, false, err
		}
		child.Status = previousStatus
		child.Category = previousCategory
		return nil, nil, previousStatus == models.StatusPending && previousCategory == models.CategoryActive, nil
	}
	if previousStatus == models.StatusRunning || previousStatus == models.StatusQueued {
		return nil, nil, false, ErrAutomationChainChildBusy
	}
	effectiveCategory := child.Category
	admitted := lifecycle == models.AutomationActive
	if !admitted && effectiveCategory == models.CategoryActive {
		effectiveCategory = models.CategoryBacklog
	}
	result, err := conn.ExecContext(ctx, `UPDATE tasks SET prompt = ?, category = ?, status = 'pending', base_branch = ?, base_commit_sha = ?,
		lineage_depth = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND project_id = ? AND parent_task_id = ?`,
		child.Prompt, effectiveCategory, child.BaseBranch, child.BaseCommitSHA, child.LineageDepth, child.ID, child.ProjectID, parent.ID)
	if err != nil {
		return nil, nil, false, fmt.Errorf("activating automation chain child: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, nil, false, errors.New("automation chain child is unavailable")
	}
	workItem, activity, err := recordProjectionEventWithExecutor(ctx, conn, event)
	if err != nil {
		return nil, nil, false, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, nil, false, err
	}
	child.Status = models.StatusPending
	child.Category = effectiveCategory
	if r.broadcaster != nil {
		if previousStatus != models.StatusPending {
			r.broadcaster.Publish(events.TaskEvent{Type: events.TaskStatusChanged, ProjectID: child.ProjectID, TaskID: child.ID,
				TaskName: child.Title, Status: string(models.StatusPending), OldStatus: string(previousStatus), Category: string(child.Category)})
		}
		if previousCategory != child.Category {
			r.broadcaster.Publish(events.TaskEvent{Type: events.TaskCategoryChanged, ProjectID: child.ProjectID, TaskID: child.ID,
				TaskName: child.Title, Status: string(models.StatusPending), Category: string(child.Category), OldCategory: string(previousCategory)})
		}
	}
	return workItem, activity, admitted, nil
}

// ConfirmAutomationChainedTaskAdmission is the final persisted admission gate
// between a committed handoff and its in-memory worker submission. Pause and
// Archive demote pending graph tasks in the same database serialization domain,
// so a handoff that lost the race is returned as non-admitted.
func (r *TaskRepo) ConfirmAutomationChainedTaskAdmission(ctx context.Context, projectID string, binding models.AutomationBinding, taskID string) (bool, error) {
	if r == nil || strings.TrimSpace(projectID) == "" || strings.TrimSpace(binding.AutomationID) == "" ||
		strings.TrimSpace(binding.VersionID) == "" || strings.TrimSpace(taskID) == "" {
		return false, errors.New("complete automation task admission is required")
	}
	var admitted int
	err := r.db.QueryRowContext(ctx, `SELECT CASE WHEN a.lifecycle_state = 'active'
		AND a.published_version_id = ? AND t.category = 'active' AND t.status = 'pending' THEN 1 ELSE 0 END
		FROM automations a
		JOIN automation_definition_resources resource ON resource.project_id = a.project_id
			AND resource.automation_id = a.id AND resource.version_id = a.published_version_id
			AND resource.resource_type = 'task' AND resource.resource_id = ?
		JOIN tasks t ON t.id = resource.resource_id AND t.project_id = resource.project_id
		WHERE a.project_id = ? AND a.id = ?`, binding.VersionID, taskID, projectID, binding.AutomationID).Scan(&admitted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return admitted == 1, nil
}

// ClaimAutomationDispatch consumes a leased Automation reservation, applies the
// existing pending-to-running task transition, and creates or resolves exactly
// one execution by dispatch ID in one BEGIN IMMEDIATE transaction.
func (r *TaskRepo) ClaimAutomationDispatch(ctx context.Context, dispatchID, claimant string) (*models.Execution, error) {
	return r.claimAutomationDispatch(ctx, dispatchID, claimant, false, nil)
}

// QueuedAutomationDispatchClaim contains the execution and authoritative Task
// admitted by a queued Automation dispatch.
type QueuedAutomationDispatchClaim struct {
	Execution models.Execution
	Task      models.Task
}

// ClaimQueuedAutomationDispatch performs the pending-to-running transition only
// after WorkerService has reserved global, project, and model capacity. The
// returned Task is captured in the atomic claim transaction so execution uses
// the exact persisted assignment that was admitted.
func (r *TaskRepo) ClaimQueuedAutomationDispatch(ctx context.Context, dispatchID string) (*QueuedAutomationDispatchClaim, error) {
	var task *models.Task
	execution, err := r.claimAutomationDispatch(ctx, dispatchID, "", true, &task)
	if err != nil {
		return nil, err
	}
	if task == nil || task.Status != models.StatusRunning {
		return nil, errors.New("claimed automation task disappeared")
	}
	return &QueuedAutomationDispatchClaim{Execution: *execution, Task: *task}, nil
}

func (r *TaskRepo) claimAutomationDispatch(ctx context.Context, dispatchID, claimant string, queued bool, claimedTask **models.Task) (*models.Execution, error) {
	conn, finishImmediate, err := beginImmediateConn(ctx, r.db)
	if err != nil {
		return nil, err
	}
	defer finishImmediate()

	var invocationID, taskID, projectID, versionID, automationID, nodeID string
	var leaseExpiry sql.NullTime
	claimPredicate := `d.status = 'processing' AND d.claimed_by = ? AND d.claim_expires_at > ?`
	claimArgs := []any{dispatchID, claimant, time.Now().UTC()}
	if queued {
		claimPredicate = `d.status = 'submitted' AND d.execution_id IS NULL`
		claimArgs = []any{dispatchID}
	}
	err = conn.QueryRowContext(ctx, `SELECT d.invocation_id, d.task_id, i.project_id, i.version_id, i.automation_id,
		COALESCE((SELECT dr.node_id FROM automation_definition_resources dr
			WHERE dr.version_id = i.version_id AND dr.resource_type = 'task' AND dr.resource_id = d.task_id
			ORDER BY dr.created_at, dr.id LIMIT 1), i.trigger_node_id), d.claim_expires_at
		FROM automation_dispatch_outbox d
		JOIN automation_invocations i ON i.id = d.invocation_id
		JOIN automation_task_run_reservations r ON r.dispatch_id = d.id AND r.task_id = d.task_id AND r.project_id = i.project_id
		WHERE d.id = ? AND `+claimPredicate, claimArgs...).
		Scan(&invocationID, &taskID, &projectID, &versionID, &automationID, &nodeID, &leaseExpiry)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAutomationDispatchLease
	}
	if err != nil {
		return nil, fmt.Errorf("validating automation dispatch claim: %w", err)
	}

	var executionID string
	err = conn.QueryRowContext(ctx, `SELECT id FROM executions WHERE dispatch_id = ?`, dispatchID).Scan(&executionID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("resolving automation execution: %w", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		var taskStatus models.TaskStatus
		var taskCategory models.TaskCategory
		var taskProject, prompt, agentConfigID string
		if err := conn.QueryRowContext(ctx, `SELECT project_id, category, status, prompt, COALESCE(agent_id, '') FROM tasks WHERE id = ?`, taskID).
			Scan(&taskProject, &taskCategory, &taskStatus, &prompt, &agentConfigID); err != nil {
			return nil, fmt.Errorf("loading automation task claim: %w", err)
		}
		if taskProject != projectID || taskStatus != models.StatusPending ||
			(taskCategory != models.CategoryActive && taskCategory != models.CategoryScheduled) {
			return nil, ErrAutomationTaskBusy
		}
		result, err := conn.ExecContext(ctx, `UPDATE tasks SET status = 'running', updated_at = CURRENT_TIMESTAMP
			WHERE id = ? AND project_id = ? AND status = 'pending'`, taskID, projectID)
		if err != nil {
			return nil, fmt.Errorf("claiming automation task: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return nil, ErrAutomationTaskBusy
		}
		if err := conn.QueryRowContext(ctx, `INSERT INTO executions
				(task_id, agent_config_id, status, prompt_sent, is_followup, dispatch_id, starts_new_context)
				VALUES (?, NULLIF(?, ''), 'running', ?, 0, ?, COALESCE((
					SELECT s.clear_context_on_start FROM automation_invocations i
					JOIN schedules s ON i.trigger_resource_type IN ('schedule', 'manual') AND s.id = i.trigger_resource_id
					WHERE i.id = ?
				), 0)) RETURNING id`, taskID, agentConfigID, prompt, dispatchID, invocationID).Scan(&executionID); err != nil {
			return nil, fmt.Errorf("creating automation execution: %w", err)
		}
	} else {
		var existingTaskID string
		if err := conn.QueryRowContext(ctx, `SELECT task_id FROM executions WHERE id = ?`, executionID).Scan(&existingTaskID); err != nil || existingTaskID != taskID {
			return nil, errors.New("automation execution task mismatch")
		}
	}
	outboxPredicate := `status = 'processing' AND claimed_by = ?`
	outboxArgs := []any{executionID, dispatchID, claimant}
	if queued {
		outboxPredicate = `status = 'submitted' AND execution_id IS NULL`
		outboxArgs = []any{executionID, dispatchID}
	}
	if _, err := conn.ExecContext(ctx, `UPDATE automation_dispatch_outbox SET execution_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND `+outboxPredicate, outboxArgs...); err != nil {
		return nil, err
	}
	if queued {
		if _, err := conn.ExecContext(ctx, `UPDATE automation_task_run_reservations SET state = 'claimed', lease_owner = 'worker-service',
			lease_expires_at = ?, updated_at = CURRENT_TIMESTAMP WHERE dispatch_id = ?`, time.Now().UTC().Add(10*time.Minute), dispatchID); err != nil {
			return nil, err
		}
	} else if _, err := conn.ExecContext(ctx, `UPDATE automation_task_run_reservations SET state = 'claimed', lease_owner = ?,
		lease_expires_at = ?, updated_at = CURRENT_TIMESTAMP WHERE dispatch_id = ?`, claimant, leaseExpiry.Time, dispatchID); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE automation_invocations SET status = 'running', started_at = COALESCE(started_at, CURRENT_TIMESTAMP),
		updated_at = CURRENT_TIMESTAMP WHERE id = ?`, invocationID); err != nil {
		return nil, err
	}
	activityKey := "dispatch:" + dispatchID + ":execute"
	var activityID string
	if err := conn.QueryRowContext(ctx, `INSERT INTO automation_activities
		(project_id, automation_id, version_id, node_id, invocation_id, activity_key, activity_type, status)
		VALUES (?, ?, ?, ?, ?, ?, 'task_execution', 'running')
		ON CONFLICT(automation_id, version_id, activity_key) DO UPDATE SET status = 'running'
		RETURNING id`, projectID, automationID, versionID, nodeID, invocationID, activityKey).Scan(&activityID); err != nil {
		return nil, fmt.Errorf("recording automation execution activity: %w", err)
	}
	for _, resource := range []struct{ kind, id string }{{"task", taskID}, {"execution", executionID}} {
		if _, err := conn.ExecContext(ctx, `INSERT INTO automation_activity_resources (activity_id, resource_type, resource_id, relation)
				VALUES (?, ?, ?, 'subject') ON CONFLICT(activity_id, resource_type, resource_id, relation) DO NOTHING`,
			activityID, resource.kind, resource.id); err != nil {
			return nil, err
		}
	}
	if err := syncAutomationLiveActivityState(ctx, conn, activityID); err != nil {
		return nil, err
	}
	if queued && claimedTask != nil {
		task, err := getTaskWithExecutor(ctx, conn, `SELECT `+taskSelectColumns+` FROM tasks WHERE id = ?`, taskID)
		if err != nil {
			return nil, fmt.Errorf("loading claimed automation task: %w", err)
		}
		if task == nil {
			return nil, errors.New("claimed automation task disappeared")
		}
		*claimedTask = task
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, err
	}
	finishImmediate()

	execution, err := NewExecutionRepo(r.db).GetByID(ctx, executionID)
	if err != nil {
		return nil, err
	}
	if execution == nil {
		return nil, errors.New("claimed automation execution disappeared")
	}
	if r.broadcaster != nil {
		task, _ := r.GetByID(ctx, taskID)
		event := events.TaskEvent{Type: events.TaskStatusChanged, TaskID: taskID, ProjectID: projectID,
			Status: string(models.StatusRunning), OldStatus: string(models.StatusPending)}
		if task != nil {
			event.TaskName, event.Category = task.Title, string(task.Category)
		}
		r.broadcaster.Publish(event)
	}
	return execution, nil
}

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

// ClaimAutomationDispatch consumes a leased Automation reservation, applies the
// existing pending-to-running task transition, and creates or resolves exactly
// one execution by dispatch ID in one BEGIN IMMEDIATE transaction.
func (r *TaskRepo) ClaimAutomationDispatch(ctx context.Context, dispatchID, claimant string) (*models.Execution, error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	var invocationID, taskID, projectID, versionID, automationID, nodeID string
	var leaseExpiry time.Time
	err = conn.QueryRowContext(ctx, `SELECT d.invocation_id, d.task_id, i.project_id, i.version_id, i.automation_id,
		COALESCE((SELECT dr.node_id FROM automation_definition_resources dr
			WHERE dr.version_id = i.version_id AND dr.resource_type = 'task' AND dr.resource_id = d.task_id
			ORDER BY dr.created_at, dr.id LIMIT 1), i.trigger_node_id), d.claim_expires_at
		FROM automation_dispatch_outbox d
		JOIN automation_invocations i ON i.id = d.invocation_id
		JOIN automation_task_run_reservations r ON r.dispatch_id = d.id AND r.task_id = d.task_id AND r.project_id = i.project_id
		WHERE d.id = ? AND d.status = 'processing' AND d.claimed_by = ? AND d.claim_expires_at > ?`,
		dispatchID, claimant, time.Now().UTC()).
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
		var taskProject, prompt, agentConfigID string
		if err := conn.QueryRowContext(ctx, `SELECT project_id, status, prompt, COALESCE(agent_id, '') FROM tasks WHERE id = ?`, taskID).
			Scan(&taskProject, &taskStatus, &prompt, &agentConfigID); err != nil {
			return nil, fmt.Errorf("loading automation task claim: %w", err)
		}
		if taskProject != projectID || taskStatus != models.StatusPending {
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
			(task_id, agent_config_id, status, prompt_sent, is_followup, dispatch_id)
			VALUES (?, NULLIF(?, ''), 'running', ?, 0, ?) RETURNING id`, taskID, agentConfigID, prompt, dispatchID).
			Scan(&executionID); err != nil {
			return nil, fmt.Errorf("creating automation execution: %w", err)
		}
	} else {
		var existingTaskID string
		if err := conn.QueryRowContext(ctx, `SELECT task_id FROM executions WHERE id = ?`, executionID).Scan(&existingTaskID); err != nil || existingTaskID != taskID {
			return nil, errors.New("automation execution task mismatch")
		}
	}
	if _, err := conn.ExecContext(ctx, `UPDATE automation_dispatch_outbox SET execution_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'processing' AND claimed_by = ?`, executionID, dispatchID, claimant); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE automation_task_run_reservations SET state = 'claimed', lease_owner = ?,
		lease_expires_at = ?, updated_at = CURRENT_TIMESTAMP WHERE dispatch_id = ?`, claimant, leaseExpiry, dispatchID); err != nil {
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
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, err
	}
	committed = true
	_ = conn.Close()

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

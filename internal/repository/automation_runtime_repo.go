package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/automationobs"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
)

var (
	ErrAutomationScheduleChanged        = errors.New("automation schedule occurrence changed")
	ErrAutomationNotActive              = errors.New("automation must be active to run now")
	ErrAutomationNoScheduleEntries      = errors.New("automation has no runnable schedule entries")
	ErrAutomationDispatchLease          = errors.New("automation dispatch lease is not owned")
	ErrAutomationTaskBusy               = errors.New("automation task is not available")
	ErrAutomationExternalReconciliation = errors.New("automation external mutation requires reconciliation")
	ErrAutomationGitHubIssueDedupBusy   = errors.New("another Automation run is already checking or creating this GitHub issue")
	githubResourceNamePattern           = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

const AutomationExternalStaleAfter = 15 * time.Minute

const listAutomationsWithStaleExternalPullRequestsSQL = `SELECT DISTINCT a.project_id, a.automation_id
				FROM task_pull_requests pr INDEXED BY idx_task_pull_requests_updated_at_task_id
				CROSS JOIN automation_activity_resources task_resource INDEXED BY idx_automation_activity_resources_type_resource_activity
				CROSS JOIN automation_activities a
				WHERE pr.updated_at < ?
					AND task_resource.resource_type = 'task'
					AND task_resource.resource_id = pr.task_id
					AND a.id = task_resource.activity_id
					AND EXISTS (
						SELECT 1 FROM automation_activity_resources pull_resource INDEXED BY idx_automation_activity_resources_activity_type
						WHERE pull_resource.activity_id = a.id AND pull_resource.resource_type = 'pull_request'
					)
				ORDER BY a.project_id, a.automation_id
				LIMIT ?`

const liveNodeCountsSQL = `WITH operational_state AS (
				SELECT node_id, CASE activity_status
					WHEN 'pending' THEN 'running' WHEN 'running' THEN 'running' WHEN 'waiting' THEN 'waiting'
					WHEN 'failed' THEN 'failed' END AS state,
					state_key
				FROM automation_live_activity_states
				WHERE project_id = ? AND automation_id = ? AND version_id = ?
					AND activity_status IN ('pending','running','waiting','failed')
			UNION ALL
			SELECT node_id, 'recent', state_key
			FROM automation_live_activity_states
			WHERE project_id = ? AND automation_id = ? AND version_id = ?
				AND activity_status = 'completed' AND completed_at >= ?
			UNION ALL
			SELECT i.trigger_node_id, 'running', 'invocation:' || i.id
			FROM automation_invocations i
			WHERE i.project_id = ? AND i.automation_id = ? AND i.version_id = ?
				AND i.status IN ('claimed','dispatched','running')
				AND NOT EXISTS (SELECT 1 FROM automation_activities a INDEXED BY idx_automation_activities_invocation WHERE a.invocation_id = i.id)
			UNION ALL
			SELECT binding.node_id, 'running', CASE WHEN binding.work_item_id IS NOT NULL
				THEN 'work:' || binding.work_item_id ELSE 'input:' || binding.thread_input_id END
			FROM automation_thread_input_bindings binding
			JOIN thread_inputs input ON input.id = binding.thread_input_id
			WHERE binding.project_id = ? AND binding.automation_id = ? AND binding.version_id = ?
				AND input.input_status = 'pending'
			UNION ALL
			SELECT position.node_id,
				CASE WHEN position.state = 'active' THEN 'running' WHEN position.state = 'waiting' THEN 'waiting'
					WHEN position.state = 'blocked' THEN 'blocked' WHEN position.state = 'failed' THEN 'failed' END,
				'work:' || position.work_item_id
			FROM automation_work_item_positions position
			JOIN automation_nodes node ON node.id = position.node_id AND node.version_id = position.version_id
				AND node.automation_id = position.automation_id AND node.project_id = position.project_id
			WHERE position.project_id = ? AND position.automation_id = ? AND position.version_id = ?
				AND position.state IN ('active','waiting','blocked','failed')
				AND NOT (position.state = 'active' AND node.role = 'github_inbox')
			UNION ALL
			SELECT to_node_id, 'recent', 'work:' || work_item_id
			FROM automation_transitions
			WHERE project_id = ? AND automation_id = ? AND version_id = ? AND state = 'completed' AND occurred_at >= ?
			), identity_state AS (
				SELECT node_id, state_key, MAX(CASE state
					WHEN 'failed' THEN 5 WHEN 'blocked' THEN 4 WHEN 'waiting' THEN 3
					WHEN 'running' THEN 2 WHEN 'recent' THEN 1 ELSE 0 END) AS state_priority
				FROM operational_state GROUP BY node_id, state_key
			)
			SELECT node_id,
				SUM(CASE WHEN state_priority = 2 THEN 1 ELSE 0 END),
				SUM(CASE WHEN state_priority = 3 THEN 1 ELSE 0 END),
				SUM(CASE WHEN state_priority = 4 THEN 1 ELSE 0 END),
				SUM(CASE WHEN state_priority = 5 THEN 1 ELSE 0 END),
				SUM(CASE WHEN state_priority = 1 THEN 1 ELSE 0 END)
			FROM identity_state GROUP BY node_id`

type AutomationGitHubIssueDedupSource struct {
	Context     models.AutomationContext `json:"context"`
	TaskID      string                   `json:"task_id"`
	ExecutionID string                   `json:"execution_id"`
}

type AutomationGitHubIssueDedupClaim struct {
	IssueNumber int
	OwnerToken  string
	Source      AutomationGitHubIssueDedupSource
}

func sameAutomationGitHubIssueDedupSource(left, right AutomationGitHubIssueDedupSource) bool {
	if strings.TrimSpace(left.Context.ProjectID) != strings.TrimSpace(right.Context.ProjectID) ||
		strings.TrimSpace(left.TaskID) != strings.TrimSpace(right.TaskID) ||
		len(left.Context.Bindings) != len(right.Context.Bindings) {
		return false
	}
	bindingCounts := make(map[string]int, len(left.Context.Bindings))
	for _, binding := range left.Context.Bindings {
		key := strings.TrimSpace(binding.AutomationID) + "\x00" + strings.TrimSpace(binding.VersionID) + "\x00" + strings.TrimSpace(binding.NodeID)
		bindingCounts[key]++
	}
	for _, binding := range right.Context.Bindings {
		key := strings.TrimSpace(binding.AutomationID) + "\x00" + strings.TrimSpace(binding.VersionID) + "\x00" + strings.TrimSpace(binding.NodeID)
		if bindingCounts[key] == 0 {
			return false
		}
		bindingCounts[key]--
	}
	return true
}

type AutomationProjectionEvent struct {
	Context        models.AutomationContext
	Binding        models.AutomationBinding
	WorkItemKey    string
	WorkItemKind   string
	WorkItemTitle  string
	WorkItemStatus models.AutomationWorkItemStatus
	ActivityKey    string
	ActivityType   string
	ActivityStatus models.AutomationActivityStatus
	Resources      []models.AutomationActivityResource
	EventKey       string
	FromNodeID     string
	ToNodeID       string
	EdgeID         string
	Transition     models.AutomationTransitionState
	MetadataJSON   string
}

func (r *AutomationRepo) PublishInvalidation(eventType events.TaskEventType, projectID string, binding models.AutomationBinding) {
	if r == nil || r.broadcaster == nil {
		return
	}
	r.broadcaster.Publish(events.TaskEvent{
		Type: eventType, ProjectID: projectID, AutomationID: binding.AutomationID,
		VersionID: binding.VersionID, InvocationID: binding.InvocationID,
		WorkItemID: binding.WorkItemID, NodeID: binding.NodeID,
	})
}

func (r *AutomationRepo) PublishResourceInvalidations(ctx context.Context, eventType events.TaskEventType, projectID, resourceType, resourceID string) {
	if r == nil || r.broadcaster == nil || projectID == "" || resourceType == "" || resourceID == "" {
		return
	}
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT a.automation_id, a.version_id,
		COALESCE(a.invocation_id, ''),
		COALESCE((SELECT p.node_id FROM automation_work_item_positions p
			WHERE p.work_item_id = a.work_item_id ORDER BY p.entered_at, p.node_id LIMIT 1), a.node_id),
		COALESCE(a.work_item_id, '')
		FROM automation_activities a
		JOIN automation_activity_resources ar ON ar.activity_id = a.id
		WHERE a.project_id = ? AND ar.resource_type = ? AND ar.resource_id = ?
		ORDER BY a.automation_id, a.version_id, a.node_id`, projectID, resourceType, resourceID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var binding models.AutomationBinding
		if err := rows.Scan(&binding.AutomationID, &binding.VersionID, &binding.InvocationID, &binding.NodeID, &binding.WorkItemID); err != nil {
			return
		}
		r.PublishInvalidation(eventType, projectID, binding)
	}
}

func (r *AutomationRepo) GetTriggerOwner(ctx context.Context, scheduleID string) (*models.AutomationTriggerOwner, error) {
	var owner models.AutomationTriggerOwner
	err := r.db.QueryRowContext(ctx, `SELECT schedule_id, project_id, automation_id, version_id, node_id,
		ownership_state, created_at, updated_at FROM automation_trigger_owners WHERE schedule_id = ?`, scheduleID).
		Scan(&owner.ScheduleID, &owner.ProjectID, &owner.AutomationID, &owner.VersionID, &owner.NodeID,
			&owner.OwnershipState, &owner.CreatedAt, &owner.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting automation trigger owner: %w", err)
	}
	return &owner, nil
}

func automationOccurrenceKey(scheduleID string, due time.Time) string {
	return "schedule:" + scheduleID + ":" + due.UTC().Format(time.RFC3339Nano)
}

func (r *AutomationRepo) ClaimManualAutomationRun(ctx context.Context, projectID, automationID string, now time.Time) ([]models.AutomationInvocation, []models.AutomationDispatch, error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	var versionID, adapterKey string
	var lifecycle models.AutomationLifecycleState
	if err := conn.QueryRowContext(ctx, `SELECT a.published_version_id, a.lifecycle_state, v.adapter_key
		FROM automations a JOIN automation_versions v ON v.id = a.published_version_id
			AND v.automation_id = a.id AND v.project_id = a.project_id
		WHERE a.project_id = ? AND a.id = ?`, projectID, automationID).Scan(&versionID, &lifecycle, &adapterKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, errors.New("automation not found")
		}
		return nil, nil, fmt.Errorf("loading Automation for manual run: %w", err)
	}
	if lifecycle != models.AutomationActive {
		return nil, nil, ErrAutomationNotActive
	}

	type manualEntry struct {
		scheduleID string
		nodeID     string
		taskID     string
		status     models.TaskStatus
		category   models.TaskCategory
	}
	rows, err := conn.QueryContext(ctx, `SELECT o.schedule_id, o.node_id, s.task_id, t.status, t.category
		FROM automation_trigger_owners o
		JOIN schedules s ON s.id = o.schedule_id AND s.enabled = 1
		JOIN tasks t ON t.id = s.task_id AND t.project_id = o.project_id
		JOIN automation_definition_resources sr ON sr.project_id = o.project_id
			AND sr.automation_id = o.automation_id AND sr.version_id = o.version_id
			AND sr.node_id = o.node_id AND sr.resource_type = 'schedule' AND sr.resource_id = o.schedule_id
		JOIN automation_definition_resources tr ON tr.project_id = o.project_id
			AND tr.automation_id = o.automation_id AND tr.version_id = o.version_id
			AND tr.node_id = o.node_id AND tr.resource_type = 'task' AND tr.resource_id = s.task_id
		WHERE o.project_id = ? AND o.automation_id = ? AND o.version_id = ? AND o.ownership_state = 'active'
		ORDER BY o.schedule_id`, projectID, automationID, versionID)
	if err != nil {
		return nil, nil, fmt.Errorf("loading Automation manual run entries: %w", err)
	}
	var entries []manualEntry
	for rows.Next() {
		var entry manualEntry
		if err := rows.Scan(&entry.scheduleID, &entry.nodeID, &entry.taskID, &entry.status, &entry.category); err != nil {
			rows.Close()
			return nil, nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	if len(entries) == 0 {
		return nil, nil, ErrAutomationNoScheduleEntries
	}

	invocations := make([]models.AutomationInvocation, 0, len(entries))
	dispatches := make([]models.AutomationDispatch, 0, len(entries))
	for _, entry := range entries {
		occurrenceKey := "manual:" + NewID()
		if occurrenceKey == "manual:" {
			return nil, nil, errors.New("generating Automation manual run identity")
		}
		skippedReason := ""
		if entry.status == models.StatusRunning || entry.status == models.StatusQueued {
			skippedReason = "task_running"
		} else {
			var reservationCount int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_task_run_reservations WHERE task_id = ?`, entry.taskID).Scan(&reservationCount); err != nil {
				return nil, nil, err
			}
			if reservationCount > 0 {
				skippedReason = "task_reserved"
			}
		}

		invocation := models.AutomationInvocation{
			ProjectID: projectID, AutomationID: automationID, VersionID: versionID,
			TriggerNodeID: entry.nodeID, TriggerResourceType: "manual", TriggerResourceID: entry.scheduleID,
			OccurrenceKey: occurrenceKey,
		}
		if skippedReason != "" {
			invocation.Status = models.AutomationInvocationSkipped
			invocation.SkippedReason = skippedReason
			started := now.UTC()
			invocation.StartedAt, invocation.CompletedAt = &started, &started
			if err := conn.QueryRowContext(ctx, `INSERT INTO automation_invocations
				(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id,
				 occurrence_key, status, skipped_reason, started_at, completed_at)
				VALUES (?, ?, ?, ?, 'manual', ?, ?, 'skipped', ?, ?, ?)
				RETURNING id, created_at, updated_at`, projectID, automationID, versionID, entry.nodeID,
				entry.scheduleID, occurrenceKey, skippedReason, started, started).
				Scan(&invocation.ID, &invocation.CreatedAt, &invocation.UpdatedAt); err != nil {
				return nil, nil, fmt.Errorf("creating skipped Automation manual invocation: %w", err)
			}
			invocations = append(invocations, invocation)
			continue
		}

		preparedCategory := entry.category
		if adapterKey == "custom" {
			if preparedCategory != models.CategoryScheduled {
				return nil, nil, ErrAutomationScheduleChanged
			}
		} else if preparedCategory != models.CategoryActive && preparedCategory != models.CategoryScheduled {
			preparedCategory = models.CategoryScheduled
		}
		invocation.Status = models.AutomationInvocationClaimed
		if err := conn.QueryRowContext(ctx, `INSERT INTO automation_invocations
			(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id,
			 occurrence_key, status) VALUES (?, ?, ?, ?, 'manual', ?, ?, 'claimed')
			RETURNING id, created_at, updated_at`, projectID, automationID, versionID, entry.nodeID,
			entry.scheduleID, occurrenceKey).Scan(&invocation.ID, &invocation.CreatedAt, &invocation.UpdatedAt); err != nil {
			return nil, nil, fmt.Errorf("creating Automation manual invocation: %w", err)
		}
		dispatch := models.AutomationDispatch{InvocationID: invocation.ID, TaskID: entry.taskID, Status: "pending"}
		if err := conn.QueryRowContext(ctx, `INSERT INTO automation_dispatch_outbox (invocation_id, task_id)
			VALUES (?, ?) RETURNING id, next_attempt_at, created_at, updated_at`, invocation.ID, entry.taskID).
			Scan(&dispatch.ID, &dispatch.NextAttemptAt, &dispatch.CreatedAt, &dispatch.UpdatedAt); err != nil {
			return nil, nil, fmt.Errorf("creating Automation manual dispatch: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO automation_task_run_reservations (task_id, dispatch_id, project_id)
			VALUES (?, ?, ?)`, entry.taskID, dispatch.ID, projectID); err != nil {
			return nil, nil, fmt.Errorf("reserving Automation manual task: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `UPDATE tasks SET status = 'pending', category = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ? AND project_id = ?`, preparedCategory, entry.taskID, projectID); err != nil {
			return nil, nil, fmt.Errorf("preparing Automation manual task: %w", err)
		}
		invocations = append(invocations, invocation)
		dispatches = append(dispatches, dispatch)
	}

	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, nil, err
	}
	committed = true
	for _, invocation := range invocations {
		automationobs.Event("automation.invocation.created",
			automationobs.String("project_id", projectID), automationobs.String("automation_id", automationID),
			automationobs.String("version_id", versionID), automationobs.String("invocation_id", invocation.ID),
			automationobs.String("node_id", invocation.TriggerNodeID), automationobs.String("status", string(invocation.Status)),
			automationobs.String("trigger", "manual"))
		r.PublishInvalidation(events.AutomationDefinitionUpdated, projectID, models.AutomationBinding{
			AutomationID: automationID, VersionID: versionID, InvocationID: invocation.ID, NodeID: invocation.TriggerNodeID,
		})
	}
	return invocations, dispatches, nil
}

func (r *AutomationRepo) ClaimScheduledOccurrence(ctx context.Context, schedule models.Schedule, now time.Time, nextRun *time.Time) (*models.AutomationInvocation, *models.AutomationDispatch, error) {
	if schedule.NextRun == nil {
		return nil, nil, ErrAutomationScheduleChanged
	}
	due := schedule.NextRun.UTC()
	occurrenceKey := automationOccurrenceKey(schedule.ID, due)
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	if existing, dispatch, err := loadInvocationForOccurrence(ctx, conn, schedule.ID, occurrenceKey); err != nil {
		return nil, nil, err
	} else if existing != nil {
		if automationInvocationTerminal(existing.Status) {
			if _, err := conn.ExecContext(ctx, `UPDATE schedules SET next_run = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ? AND enabled = 1 AND next_run = ? AND next_run <= ?`, nextRun, schedule.ID, due, now.UTC()); err != nil {
				return nil, nil, fmt.Errorf("advancing completed automation occurrence: %w", err)
			}
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return nil, nil, err
		}
		committed = true
		return existing, dispatch, nil
	}

	var owner models.AutomationTriggerOwner
	var lifecycle models.AutomationLifecycleState
	var adapterKey string
	err = conn.QueryRowContext(ctx, `SELECT o.schedule_id, o.project_id, o.automation_id, o.version_id, o.node_id,
		o.ownership_state, o.created_at, o.updated_at, a.lifecycle_state, v.adapter_key
		FROM automation_trigger_owners o JOIN automations a ON a.id = o.automation_id AND a.project_id = o.project_id
		JOIN automation_versions v ON v.id = o.version_id AND v.automation_id = o.automation_id AND v.project_id = o.project_id
		WHERE o.schedule_id = ? AND o.ownership_state = 'active' AND a.lifecycle_state = 'active'`, schedule.ID).
		Scan(&owner.ScheduleID, &owner.ProjectID, &owner.AutomationID, &owner.VersionID, &owner.NodeID,
			&owner.OwnershipState, &owner.CreatedAt, &owner.UpdatedAt, &lifecycle, &adapterKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrAutomationScheduleChanged
	}
	if err != nil {
		return nil, nil, fmt.Errorf("loading active trigger ownership: %w", err)
	}
	var enabled bool
	var currentDue time.Time
	var scheduledTaskID, scheduledTaskProject string
	if err := conn.QueryRowContext(ctx, `SELECT s.enabled, s.next_run, s.task_id, t.project_id
		FROM schedules s JOIN tasks t ON t.id = s.task_id WHERE s.id = ?`, schedule.ID).
		Scan(&enabled, &currentDue, &scheduledTaskID, &scheduledTaskProject); err != nil {
		return nil, nil, fmt.Errorf("loading due automation schedule: %w", err)
	}
	if !enabled || !currentDue.UTC().Equal(due) || currentDue.After(now) || scheduledTaskProject != owner.ProjectID {
		return nil, nil, ErrAutomationScheduleChanged
	}

	taskID := scheduledTaskID
	if adapterKey == "custom" {
		var scheduledTaskMemberships int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_definition_resources
			WHERE project_id = ? AND automation_id = ? AND version_id = ? AND node_id = ?
			AND resource_type = 'task' AND resource_id = ?`, owner.ProjectID, owner.AutomationID, owner.VersionID,
			owner.NodeID, scheduledTaskID).Scan(&scheduledTaskMemberships); err != nil {
			return nil, nil, err
		}
		if scheduledTaskMemberships != 1 {
			return nil, nil, ErrAutomationScheduleChanged
		}
	}

	var taskProject, taskTitle string
	var taskStatus models.TaskStatus
	var taskCategory models.TaskCategory
	if err := conn.QueryRowContext(ctx, `SELECT project_id, title, status, category FROM tasks WHERE id = ?`, taskID).
		Scan(&taskProject, &taskTitle, &taskStatus, &taskCategory); err != nil {
		return nil, nil, fmt.Errorf("loading automation dispatch task: %w", err)
	}
	if taskProject != owner.ProjectID {
		return nil, nil, ErrAutomationScheduleChanged
	}
	preparedCategory := taskCategory
	if adapterKey == "custom" {
		if preparedCategory != models.CategoryScheduled {
			return nil, nil, ErrAutomationScheduleChanged
		}
	} else if preparedCategory != models.CategoryActive && preparedCategory != models.CategoryScheduled {
		preparedCategory = models.CategoryScheduled
	}

	skippedReason := ""
	if taskStatus == models.StatusRunning || taskStatus == models.StatusQueued {
		skippedReason = "task_running"
	} else {
		var reservationCount int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_task_run_reservations
				WHERE task_id = ?`, taskID).Scan(&reservationCount); err != nil {
			return nil, nil, err
		}
		if reservationCount > 0 {
			skippedReason = "task_reserved"
		}
	}

	invocation := &models.AutomationInvocation{
		ProjectID: owner.ProjectID, AutomationID: owner.AutomationID, VersionID: owner.VersionID,
		TriggerNodeID: owner.NodeID, TriggerResourceType: "schedule", TriggerResourceID: schedule.ID,
		OccurrenceKey: occurrenceKey, ScheduledFor: &due,
	}
	if skippedReason != "" {
		invocation.Status = models.AutomationInvocationSkipped
		invocation.SkippedReason = skippedReason
		if err := conn.QueryRowContext(ctx, `INSERT INTO automation_invocations
			(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id,
			 occurrence_key, scheduled_for, status, skipped_reason, started_at, completed_at)
			VALUES (?, ?, ?, ?, 'schedule', ?, ?, ?, 'skipped', ?, ?, ?)
			RETURNING id, created_at, updated_at`, owner.ProjectID, owner.AutomationID, owner.VersionID, owner.NodeID,
			schedule.ID, occurrenceKey, due, skippedReason, now.UTC(), now.UTC()).
			Scan(&invocation.ID, &invocation.CreatedAt, &invocation.UpdatedAt); err != nil {
			return nil, nil, fmt.Errorf("creating skipped automation invocation: %w", err)
		}
		started := now.UTC()
		invocation.StartedAt, invocation.CompletedAt = &started, &started
	} else {
		invocation.Status = models.AutomationInvocationClaimed
		if err := conn.QueryRowContext(ctx, `INSERT INTO automation_invocations
			(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id,
			 occurrence_key, scheduled_for, status)
			VALUES (?, ?, ?, ?, 'schedule', ?, ?, ?, 'claimed') RETURNING id, created_at, updated_at`,
			owner.ProjectID, owner.AutomationID, owner.VersionID, owner.NodeID, schedule.ID, occurrenceKey, due).
			Scan(&invocation.ID, &invocation.CreatedAt, &invocation.UpdatedAt); err != nil {
			return nil, nil, fmt.Errorf("creating automation invocation: %w", err)
		}
	}

	var dispatch *models.AutomationDispatch
	if skippedReason == "" {
		dispatch = &models.AutomationDispatch{InvocationID: invocation.ID, TaskID: taskID, Status: "pending"}
		if err := conn.QueryRowContext(ctx, `INSERT INTO automation_dispatch_outbox (invocation_id, task_id)
			VALUES (?, ?) RETURNING id, next_attempt_at, created_at, updated_at`, invocation.ID, taskID).
			Scan(&dispatch.ID, &dispatch.NextAttemptAt, &dispatch.CreatedAt, &dispatch.UpdatedAt); err != nil {
			return nil, nil, fmt.Errorf("creating automation dispatch: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO automation_task_run_reservations (task_id, dispatch_id, project_id)
			VALUES (?, ?, ?)`, taskID, dispatch.ID, owner.ProjectID); err != nil {
			return nil, nil, fmt.Errorf("reserving automation task: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `UPDATE tasks SET status = 'pending', category = ?,
			updated_at = CURRENT_TIMESTAMP WHERE id = ? AND project_id = ?`, preparedCategory, taskID, owner.ProjectID); err != nil {
			return nil, nil, fmt.Errorf("preparing automation task: %w", err)
		}
	}

	result, err := conn.ExecContext(ctx, `UPDATE schedules SET last_run = ?, next_run = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND enabled = 1 AND next_run = ?`, now.UTC(), nextRun, schedule.ID, due)
	if err != nil {
		return nil, nil, fmt.Errorf("advancing automation schedule: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return nil, nil, ErrAutomationScheduleChanged
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, nil, err
	}
	committed = true
	automationobs.Event("automation.invocation.created",
		automationobs.String("project_id", owner.ProjectID), automationobs.String("automation_id", owner.AutomationID),
		automationobs.String("version_id", owner.VersionID), automationobs.String("invocation_id", invocation.ID),
		automationobs.String("node_id", owner.NodeID), automationobs.String("status", string(invocation.Status)))
	if skippedReason != "" {
		automationobs.Event("automation.invocation.skipped",
			automationobs.String("automation_id", owner.AutomationID), automationobs.String("version_id", owner.VersionID),
			automationobs.String("invocation_id", invocation.ID), automationobs.String("node_id", owner.NodeID),
			automationobs.String("reason", skippedReason))
	}
	if dispatch != nil {
		automationobs.Event("automation.dispatch.created",
			automationobs.String("automation_id", owner.AutomationID), automationobs.String("version_id", owner.VersionID),
			automationobs.String("invocation_id", invocation.ID), automationobs.String("dispatch_id", dispatch.ID),
			automationobs.String("node_id", owner.NodeID))
	}
	if dispatch != nil && r.broadcaster != nil {
		if taskStatus != models.StatusPending {
			r.broadcaster.Publish(events.TaskEvent{Type: events.TaskStatusChanged, TaskID: taskID, TaskName: taskTitle,
				ProjectID: owner.ProjectID, Status: string(models.StatusPending), OldStatus: string(taskStatus), Category: string(preparedCategory)})
		}
		if taskCategory != preparedCategory {
			r.broadcaster.Publish(events.TaskEvent{Type: events.TaskCategoryChanged, TaskID: taskID, TaskName: taskTitle,
				ProjectID: owner.ProjectID, Status: string(models.StatusPending), Category: string(preparedCategory), OldCategory: string(taskCategory)})
		}
	}
	r.PublishInvalidation(events.AutomationInvocationStarted, owner.ProjectID, models.AutomationBinding{
		AutomationID: owner.AutomationID, VersionID: owner.VersionID, InvocationID: invocation.ID, NodeID: owner.NodeID,
	})
	return invocation, dispatch, nil
}

func automationInvocationTerminal(status models.AutomationInvocationStatus) bool {
	switch status {
	case models.AutomationInvocationCompleted, models.AutomationInvocationFailed, models.AutomationInvocationCancelled, models.AutomationInvocationSkipped:
		return true
	default:
		return false
	}
}

func loadInvocationForOccurrence(ctx context.Context, exec SQLExecutor, scheduleID, occurrenceKey string) (*models.AutomationInvocation, *models.AutomationDispatch, error) {
	invocation, err := scanAutomationInvocation(exec.QueryRowContext(ctx, `SELECT id, project_id, automation_id, version_id,
		trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, scheduled_for, status, skipped_reason,
		started_at, completed_at, created_at, updated_at, error_message FROM automation_invocations
		WHERE trigger_resource_type = 'schedule' AND trigger_resource_id = ? AND occurrence_key = ?`, scheduleID, occurrenceKey))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	dispatch, err := scanAutomationDispatch(exec.QueryRowContext(ctx, `SELECT id, invocation_id, task_id,
		COALESCE(execution_id, ''), status, attempts, claimed_by, claim_expires_at, next_attempt_at, last_error,
		created_at, updated_at FROM automation_dispatch_outbox WHERE invocation_id = ?`, invocation.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return invocation, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return invocation, dispatch, nil
}

func (r *AutomationRepo) LeaseNextDispatch(ctx context.Context, claimant string, now time.Time, lease time.Duration) (*models.AutomationDispatch, error) {
	claimant = strings.TrimSpace(claimant)
	if claimant == "" {
		return nil, errors.New("dispatch claimant is required")
	}
	if lease <= 0 || lease > 10*time.Minute {
		lease = time.Minute
	}
	dispatch, err := scanAutomationDispatch(r.db.QueryRowContext(ctx, `UPDATE automation_dispatch_outbox
		SET status = 'processing', claimed_by = ?, claim_expires_at = ?, attempts = attempts + 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = (SELECT id FROM automation_dispatch_outbox
			WHERE next_attempt_at <= ? AND (status = 'pending' OR (status = 'processing' AND claim_expires_at <= ?))
			ORDER BY next_attempt_at, created_at, id LIMIT 1)
		RETURNING id, invocation_id, task_id, COALESCE(execution_id, ''), status, attempts, claimed_by,
		claim_expires_at, next_attempt_at, last_error, created_at, updated_at`, claimant, now.UTC().Add(lease), now.UTC(), now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("leasing automation dispatch: %w", err)
	}
	automationobs.Observe("automation.dispatch.outbox_age_seconds", int64(now.UTC().Sub(dispatch.CreatedAt.UTC()).Seconds()),
		automationobs.String("dispatch_id", dispatch.ID), automationobs.String("invocation_id", dispatch.InvocationID))
	if dispatch.Attempts > 1 {
		automationobs.Event("automation.dispatch.recovered",
			automationobs.String("dispatch_id", dispatch.ID), automationobs.String("invocation_id", dispatch.InvocationID),
			automationobs.String("attempts", strconv.Itoa(dispatch.Attempts)))
	}
	return dispatch, nil
}

func (r *AutomationRepo) RenewDispatchLease(ctx context.Context, dispatchID, claimant string, expires time.Time) error {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	result, err := conn.ExecContext(ctx, `UPDATE automation_dispatch_outbox SET claim_expires_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'processing' AND claimed_by = ?`, expires.UTC(), dispatchID, claimant)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrAutomationDispatchLease
	}
	result, err = conn.ExecContext(ctx, `UPDATE automation_task_run_reservations SET lease_expires_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE dispatch_id = ? AND state = 'claimed' AND lease_owner = ?`, expires.UTC(), dispatchID, claimant)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrAutomationDispatchLease
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

func (r *AutomationRepo) MarkDispatchQueued(ctx context.Context, dispatchID, claimant string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE automation_dispatch_outbox SET status = 'submitted', execution_id = NULL,
		claimed_by = '', claim_expires_at = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'processing' AND claimed_by = ?`, dispatchID, claimant)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrAutomationDispatchLease
	}
	return nil
}

func (r *AutomationRepo) AbandonQueuedDispatch(ctx context.Context, dispatchID, message string) error {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	var invocationID string
	if err := conn.QueryRowContext(ctx, `SELECT invocation_id FROM automation_dispatch_outbox
		WHERE id = ? AND status = 'submitted' AND execution_id IS NULL`, dispatchID).Scan(&invocationID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	message = strings.TrimSpace(message)
	if _, err := conn.ExecContext(ctx, `UPDATE automation_dispatch_outbox SET status = 'failed', last_error = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`, message, dispatchID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE automation_invocations SET status = 'cancelled', error_message = ?,
		completed_at = COALESCE(completed_at, CURRENT_TIMESTAMP), updated_at = CURRENT_TIMESTAMP WHERE id = ?`, message, invocationID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM automation_task_run_reservations WHERE dispatch_id = ?`, dispatchID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

func (r *AutomationRepo) ListAbandonedQueuedDispatches(ctx context.Context, limit int) ([]models.AutomationDispatch, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT d.id, d.invocation_id, d.task_id, '', d.status, d.attempts,
		d.claimed_by, d.claim_expires_at, d.next_attempt_at, d.last_error, d.created_at, d.updated_at
		FROM automation_dispatch_outbox d JOIN tasks t ON t.id = d.task_id
		WHERE d.status = 'submitted' AND d.execution_id IS NULL
		AND (t.status != 'pending' OR t.category NOT IN ('active','scheduled'))
		ORDER BY d.updated_at, d.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.AutomationDispatch
	for rows.Next() {
		var value models.AutomationDispatch
		if err := rows.Scan(&value.ID, &value.InvocationID, &value.TaskID, &value.ExecutionID, &value.Status,
			&value.Attempts, &value.ClaimedBy, &value.ClaimExpiresAt, &value.NextAttemptAt, &value.LastError,
			&value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (r *AutomationRepo) MarkDispatchSubmitted(ctx context.Context, dispatchID, claimant, executionID string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE automation_dispatch_outbox SET status = 'submitted', execution_id = ?,
		claimed_by = '', claim_expires_at = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'processing' AND claimed_by = ?`, executionID, dispatchID, claimant)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrAutomationDispatchLease
	}
	return nil
}

func (r *AutomationRepo) FailDispatch(ctx context.Context, dispatchID, claimant, message string, maxAttempts int, now time.Time) error {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	var attempts int
	var invocationID string
	if err := conn.QueryRowContext(ctx, `SELECT attempts, invocation_id FROM automation_dispatch_outbox
		WHERE id = ? AND status = 'processing' AND claimed_by = ?`, dispatchID, claimant).Scan(&attempts, &invocationID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAutomationDispatchLease
		}
		return err
	}
	if attempts >= maxAttempts {
		if _, err := conn.ExecContext(ctx, `UPDATE automation_dispatch_outbox SET status = 'failed', last_error = ?,
			claimed_by = '', claim_expires_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, strings.TrimSpace(message), dispatchID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE automation_invocations SET status = 'failed', error_message = ?,
			completed_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, strings.TrimSpace(message), now.UTC(), invocationID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM automation_task_run_reservations WHERE dispatch_id = ?`, dispatchID); err != nil {
			return err
		}
	} else {
		delay := time.Second << min(attempts-1, 6)
		if _, err := conn.ExecContext(ctx, `UPDATE automation_dispatch_outbox SET status = 'pending', last_error = ?,
			claimed_by = '', claim_expires_at = NULL, next_attempt_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			strings.TrimSpace(message), now.UTC().Add(delay), dispatchID); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	if attempts >= maxAttempts {
		automationobs.Event("automation.dispatch.failed",
			automationobs.String("dispatch_id", dispatchID), automationobs.String("invocation_id", invocationID),
			automationobs.String("attempts", strconv.Itoa(attempts)))
	} else {
		automationobs.Event("automation.dispatch.retry_scheduled",
			automationobs.String("dispatch_id", dispatchID), automationobs.String("invocation_id", invocationID),
			automationobs.String("attempts", strconv.Itoa(attempts)))
	}
	return nil
}

func (r *AutomationRepo) GetDispatchEnvelope(ctx context.Context, dispatchID string) (*models.AutomationDispatchEnvelope, error) {
	var envelope models.AutomationDispatchEnvelope
	var binding models.AutomationBinding
	var taskID, projectID string
	err := r.db.QueryRowContext(ctx, `SELECT d.id, d.task_id, i.project_id, i.automation_id, i.version_id,
		i.id, COALESCE((SELECT dr.node_id FROM automation_definition_resources dr
			WHERE dr.version_id = i.version_id AND dr.resource_type = 'task' AND dr.resource_id = d.task_id
			ORDER BY dr.created_at, dr.id LIMIT 1), i.trigger_node_id)
		FROM automation_dispatch_outbox d JOIN automation_invocations i ON i.id = d.invocation_id WHERE d.id = ?`, dispatchID).
		Scan(&envelope.DispatchID, &taskID, &projectID, &binding.AutomationID, &binding.VersionID, &binding.InvocationID, &binding.NodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading automation dispatch envelope: %w", err)
	}
	task, err := NewTaskRepo(r.db, nil).GetByID(ctx, taskID)
	if err != nil || task == nil {
		return nil, fmt.Errorf("loading automation dispatch task: %w", err)
	}
	if task.ProjectID != projectID {
		return nil, errors.New("automation dispatch task project mismatch")
	}
	envelope.Task = *task
	envelope.Context = models.AutomationContext{ProjectID: projectID, Bindings: []models.AutomationBinding{binding}, OriginTask: IsAutomationTaskCreatedVia(task.CreatedVia)}
	return &envelope, nil
}

func (r *AutomationRepo) CompleteDispatch(ctx context.Context, dispatchID, executionID string, status models.ExecutionStatus, message string) error {
	var taskStatus models.TaskStatus
	switch status {
	case models.ExecCompleted:
		taskStatus = models.StatusCompleted
	case models.ExecFailed:
		taskStatus = models.StatusFailed
	case models.ExecCancelled:
		taskStatus = models.StatusCancelled
	default:
		return fmt.Errorf("automation dispatch requires terminal execution status, got %q", status)
	}

	conn, err := r.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	var invocationID, projectID, automationID, versionID, nodeID, taskID, taskTitle string
	var taskCategory models.TaskCategory
	if err := conn.QueryRowContext(ctx, `SELECT i.id, i.project_id, i.automation_id, i.version_id, i.trigger_node_id,
		d.task_id, t.title, t.category
		FROM automation_dispatch_outbox d
		JOIN automation_invocations i ON i.id = d.invocation_id
		JOIN tasks t ON t.id = d.task_id
		WHERE d.id = ? AND d.execution_id = ?`, dispatchID, executionID).
		Scan(&invocationID, &projectID, &automationID, &versionID, &nodeID, &taskID, &taskTitle, &taskCategory); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE executions SET status = ?, error_message = ?, completed_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'running'`, status, strings.TrimSpace(message), executionID); err != nil {
		return err
	}
	taskResult, err := conn.ExecContext(ctx, `UPDATE tasks SET status = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'running'`, taskStatus, taskID)
	if err != nil {
		return err
	}
	taskChanged, err := taskResult.RowsAffected()
	if err != nil {
		return err
	}
	invocationStatus := models.AutomationInvocationCompleted
	activityStatus := models.AutomationActivityCompleted
	dispatchStatus := "completed"
	if status == models.ExecFailed {
		invocationStatus, activityStatus, dispatchStatus = models.AutomationInvocationFailed, models.AutomationActivityFailed, "failed"
	} else if status == models.ExecCancelled {
		invocationStatus, activityStatus, dispatchStatus = models.AutomationInvocationCancelled, models.AutomationActivityCancelled, "failed"
	}
	if _, err := conn.ExecContext(ctx, `UPDATE automation_dispatch_outbox SET status = ?, claimed_by = '',
		claim_expires_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, dispatchStatus, dispatchID); err != nil {
		return err
	}
	var activityID string
	if err := conn.QueryRowContext(ctx, `UPDATE automation_activities SET status = ?, completed_at = CURRENT_TIMESTAMP,
		error_message = ? WHERE invocation_id = ? AND activity_key = ? RETURNING id`, activityStatus, strings.TrimSpace(message), invocationID, "dispatch:"+dispatchID+":execute").Scan(&activityID); err != nil {
		return err
	}
	if err := syncAutomationLiveActivityState(ctx, conn, activityID); err != nil {
		return err
	}
	if status == models.ExecCompleted {
		var nonterminal, failed int
		if err := conn.QueryRowContext(ctx, `SELECT
			SUM(CASE WHEN status IN ('pending','running','waiting') THEN 1 ELSE 0 END),
			SUM(CASE WHEN status IN ('failed','cancelled') THEN 1 ELSE 0 END)
			FROM automation_activities WHERE invocation_id = ?`, invocationID).Scan(&nonterminal, &failed); err != nil {
			return err
		}
		if nonterminal > 0 {
			invocationStatus = models.AutomationInvocationRunning
		} else if failed > 0 {
			invocationStatus = models.AutomationInvocationFailed
		}
	}
	terminalInvocation := invocationStatus != models.AutomationInvocationRunning
	if _, err := conn.ExecContext(ctx, `UPDATE automation_invocations SET status = ?,
		completed_at = CASE WHEN ? THEN CURRENT_TIMESTAMP ELSE NULL END,
		error_message = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, invocationStatus, terminalInvocation,
		strings.TrimSpace(message), invocationID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM automation_task_run_reservations WHERE dispatch_id = ?`, dispatchID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	if taskChanged == 1 && r.broadcaster != nil {
		r.broadcaster.Publish(events.TaskEvent{
			Type: events.TaskStatusChanged, TaskID: taskID, TaskName: taskTitle, ProjectID: projectID,
			Status: string(taskStatus), OldStatus: string(models.StatusRunning), Category: string(taskCategory),
		})
	}
	automationobs.Event("automation.activity.completed",
		automationobs.String("project_id", projectID), automationobs.String("automation_id", automationID),
		automationobs.String("version_id", versionID), automationobs.String("invocation_id", invocationID),
		automationobs.String("activity_id", activityID), automationobs.String("node_id", nodeID),
		automationobs.String("status", string(activityStatus)))
	if terminalInvocation {
		automationobs.Event("automation.invocation.completed",
			automationobs.String("project_id", projectID), automationobs.String("automation_id", automationID),
			automationobs.String("version_id", versionID), automationobs.String("invocation_id", invocationID),
			automationobs.String("node_id", nodeID), automationobs.String("status", string(invocationStatus)))
	}
	r.PublishInvalidation(events.AutomationInvocationUpdated, projectID, models.AutomationBinding{
		AutomationID: automationID, VersionID: versionID, InvocationID: invocationID, NodeID: nodeID,
	})
	return nil
}

func (r *AutomationRepo) ReconcileInvocationCompletions(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	result, err := r.db.ExecContext(ctx, `UPDATE automation_invocations SET
		status = CASE WHEN EXISTS (SELECT 1 FROM automation_activities a
			WHERE a.invocation_id = automation_invocations.id AND a.status IN ('failed','cancelled'))
			THEN 'failed' ELSE 'completed' END,
		completed_at = CURRENT_TIMESTAMP,
		updated_at = CURRENT_TIMESTAMP
		WHERE id IN (
			SELECT i.id FROM automation_invocations i
			JOIN automation_dispatch_outbox d ON d.invocation_id = i.id
			JOIN executions e ON e.id = d.execution_id
			WHERE i.status IN ('claimed','dispatched','running')
			AND e.status IN ('completed','failed','cancelled')
			AND NOT EXISTS (SELECT 1 FROM automation_activities a
				WHERE a.invocation_id = i.id AND a.status IN ('pending','running','waiting'))
			ORDER BY i.updated_at, i.id LIMIT ?
		)`, limit)
	if err != nil {
		return 0, fmt.Errorf("reconciling automation invocation completion: %w", err)
	}
	return result.RowsAffected()
}

func (r *AutomationRepo) FinalizeExecutionProjection(ctx context.Context, projectID, executionID string, status models.ExecutionStatus) error {
	if r == nil || projectID == "" || executionID == "" || status == models.ExecRunning {
		return nil
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	type target struct {
		binding    models.AutomationBinding
		adapterKey string
		nodeKey    string
	}
	rows, err := conn.QueryContext(ctx, `SELECT DISTINCT a.automation_id, a.version_id,
		COALESCE(a.invocation_id, ''), a.node_id, COALESCE(a.work_item_id, ''), v.adapter_key, n.node_key
		FROM automation_activities a
		JOIN automation_activity_resources ar ON ar.activity_id = a.id
		JOIN automation_versions v ON v.id = a.version_id AND v.automation_id = a.automation_id AND v.project_id = a.project_id
		JOIN automation_nodes n ON n.id = a.node_id AND n.version_id = a.version_id
		WHERE a.project_id = ? AND ar.resource_type = 'execution' AND ar.resource_id = ?`, projectID, executionID)
	if err != nil {
		return err
	}
	var targets []target
	for rows.Next() {
		var value target
		if err := rows.Scan(&value.binding.AutomationID, &value.binding.VersionID, &value.binding.InvocationID,
			&value.binding.NodeID, &value.binding.WorkItemID, &value.adapterKey, &value.nodeKey); err != nil {
			_ = rows.Close()
			return err
		}
		targets = append(targets, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var changed []models.AutomationBinding
	for _, value := range targets {
		createdTerminalItem := false
		if value.binding.WorkItemID == "" {
			if value.adapterKey != "custom" {
				continue
			}
			seed := AutomationProjectionEvent{
				Context: models.AutomationContext{ProjectID: projectID, Bindings: []models.AutomationBinding{value.binding}},
				Binding: value.binding, WorkItemKey: "execution:" + executionID + ":custom-work", WorkItemKind: "task_execution",
			}
			item, binding, seedErr := upsertAutomationWorkItem(ctx, conn, seed, value.binding)
			if seedErr != nil {
				return seedErr
			}
			value.binding = binding
			value.binding.WorkItemID = item.ID
			createdTerminalItem = true
		}
		var positioned int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_work_item_positions
			WHERE work_item_id = ? AND node_id = ?`, value.binding.WorkItemID, value.binding.NodeID).Scan(&positioned); err != nil {
			return err
		}
		if positioned == 0 && !createdTerminalItem {
			continue
		}
		event := AutomationProjectionEvent{
			Context:    models.AutomationContext{ProjectID: projectID, Bindings: []models.AutomationBinding{value.binding}},
			Binding:    value.binding,
			EventKey:   "execution:" + executionID + ":terminal:" + string(status),
			FromNodeID: value.binding.NodeID,
			ToNodeID:   value.binding.NodeID,
		}
		switch status {
		case models.ExecFailed:
			event.Transition = models.AutomationTransitionFailed
		case models.ExecCancelled:
			event.Transition = models.AutomationTransitionCancelled
		case models.ExecCompleted:
			switch {
			case value.adapterKey == "native_sdlc" && value.nodeKey == "implementation":
				if err := conn.QueryRowContext(ctx, `SELECT id FROM automation_nodes
					WHERE project_id = ? AND automation_id = ? AND version_id = ? AND node_key = 'completed'`,
					projectID, value.binding.AutomationID, value.binding.VersionID).Scan(&event.ToNodeID); err != nil {
					return err
				}
			case value.adapterKey == "custom":
				err := conn.QueryRowContext(ctx, `SELECT target.id FROM automation_edges edge
					JOIN automation_nodes target ON target.id = edge.target_node_id AND target.version_id = edge.version_id
					WHERE edge.project_id = ? AND edge.automation_id = ? AND edge.version_id = ?
					AND edge.source_node_id = ? AND target.node_type = 'outcome'
					ORDER BY edge.display_order, edge.id LIMIT 1`, projectID, value.binding.AutomationID,
					value.binding.VersionID, value.binding.NodeID).Scan(&event.ToNodeID)
				if errors.Is(err, sql.ErrNoRows) {
					event.ToNodeID = value.binding.NodeID
				} else if err != nil {
					return err
				}
			default:
				continue
			}
			event.Transition = models.AutomationTransitionCompleted
		default:
			continue
		}
		item, err := scanAutomationWorkItem(conn.QueryRowContext(ctx, `SELECT id, project_id, automation_id, origin_version_id,
			COALESCE(origin_invocation_id, ''), COALESCE(parent_work_item_id, ''), work_item_key, kind, title, status,
			created_at, updated_at, completed_at FROM automation_work_items
			WHERE id = ? AND project_id = ? AND automation_id = ? AND origin_version_id = ?`, value.binding.WorkItemID,
			projectID, value.binding.AutomationID, value.binding.VersionID))
		if err != nil {
			return err
		}
		if err := appendAutomationTransition(ctx, conn, event, value.binding, item, nil); err != nil {
			automationobs.Event("automation.transition.append_failure",
				automationobs.String("project_id", projectID), automationobs.String("automation_id", value.binding.AutomationID),
				automationobs.String("version_id", value.binding.VersionID), automationobs.String("invocation_id", value.binding.InvocationID),
				automationobs.String("node_id", value.binding.NodeID), automationobs.String("work_item_id", value.binding.WorkItemID))
			return err
		}
		changed = append(changed, value.binding)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	for _, binding := range changed {
		r.PublishInvalidation(events.AutomationTransitionCreated, projectID, binding)
	}
	return nil
}

type AutomationGitHubIssueTaskCreation struct {
	ProjectID       string
	ExecutionID     string
	IssueResourceID string
	SourceBinding   models.AutomationBinding
	TargetNodeID    string
	Task            *models.Task
	Goal            *models.TaskGoal
}

type AutomationGitHubIssueTaskProvenance struct {
	ProjectID             string
	AutomationID          string
	TaskID                string
	IssueResourceID       string
	ImplementationNodeKey string
}

// CreateOrGetGitHubIssueTask validates the exact current inbox discovery and
// atomically creates its canonical implementation task plus Automation
// provenance. The stable work-item activity is the durable one-task claim.
func (r *AutomationRepo) CreateOrGetGitHubIssueTask(ctx context.Context, taskRepo *TaskRepo, in AutomationGitHubIssueTaskCreation) (*models.Task, bool, error) {
	if r == nil || taskRepo == nil || in.Task == nil || strings.TrimSpace(in.ProjectID) == "" ||
		strings.TrimSpace(in.ExecutionID) == "" || strings.TrimSpace(in.IssueResourceID) == "" ||
		strings.TrimSpace(in.SourceBinding.AutomationID) == "" || strings.TrimSpace(in.SourceBinding.VersionID) == "" ||
		strings.TrimSpace(in.SourceBinding.InvocationID) == "" || strings.TrimSpace(in.SourceBinding.NodeID) == "" ||
		strings.TrimSpace(in.SourceBinding.WorkItemID) == "" || strings.TrimSpace(in.TargetNodeID) == "" {
		return nil, false, errors.New("complete Automation GitHub issue task provenance is required")
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	binding := in.SourceBinding
	var valid int
	err = conn.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM automations automation
		JOIN automation_nodes source ON source.project_id = automation.project_id AND source.automation_id = automation.id
			AND source.version_id = automation.published_version_id AND source.id = ? AND source.role = 'github_inbox'
		JOIN automation_edges handoff ON handoff.project_id = source.project_id AND handoff.automation_id = source.automation_id
			AND handoff.version_id = source.version_id AND handoff.source_node_id = source.id
		JOIN automation_nodes target ON target.project_id = handoff.project_id AND target.automation_id = handoff.automation_id
			AND target.version_id = handoff.version_id AND target.id = handoff.target_node_id
			AND target.id = ? AND target.role IN ('implementation', 'task')
		JOIN automation_edges delivery ON delivery.project_id = target.project_id AND delivery.automation_id = target.automation_id
			AND delivery.version_id = target.version_id AND delivery.source_node_id = target.id
		JOIN automation_nodes pull_request ON pull_request.project_id = delivery.project_id AND pull_request.automation_id = delivery.automation_id
			AND pull_request.version_id = delivery.version_id AND pull_request.id = delivery.target_node_id AND pull_request.role = 'open_pull_request'
		JOIN automation_work_items work_item ON work_item.project_id = automation.project_id AND work_item.automation_id = automation.id
			AND work_item.origin_version_id = source.version_id AND work_item.id = ?
		JOIN automation_activities discovery ON discovery.project_id = automation.project_id AND discovery.automation_id = automation.id
			AND discovery.version_id = source.version_id AND discovery.node_id = source.id
			AND discovery.invocation_id = ? AND discovery.work_item_id = work_item.id AND discovery.activity_type = 'discover_assigned_issue'
		JOIN automation_activity_resources execution_resource ON execution_resource.activity_id = discovery.id
			AND execution_resource.resource_type = 'execution' AND execution_resource.resource_id = ?
		JOIN automation_activity_resources issue_resource ON issue_resource.activity_id = discovery.id
			AND issue_resource.resource_type = 'github_issue' AND issue_resource.resource_id = ?
		WHERE automation.project_id = ? AND automation.id = ? AND automation.published_version_id = ?
			AND automation.lifecycle_state = 'active'`, binding.NodeID, in.TargetNodeID, binding.WorkItemID,
		binding.InvocationID, in.ExecutionID, in.IssueResourceID, in.ProjectID,
		binding.AutomationID, binding.VersionID).Scan(&valid)
	if err != nil {
		return nil, false, fmt.Errorf("validating Automation GitHub issue task provenance: %w", err)
	}
	if valid != 1 {
		return nil, false, errors.New("source GitHub issue was not discovered by this exact current Automation execution")
	}

	existing, err := getTaskWithExecutor(ctx, conn, `SELECT `+taskSelectColumns+`
		FROM tasks WHERE project_id = ? AND id = (
			SELECT resource.resource_id FROM automation_activity_resources resource
			JOIN automation_activities activity ON activity.id = resource.activity_id
			WHERE resource.resource_type = 'task' AND activity.project_id = ? AND activity.automation_id = ?
				AND activity.version_id = ? AND activity.work_item_id = ? AND activity.node_id = ?
				AND activity.activity_type = 'create_task'
			ORDER BY activity.started_at, activity.id LIMIT 1
		)`, in.ProjectID, in.ProjectID, binding.AutomationID,
		binding.VersionID, binding.WorkItemID, in.TargetNodeID)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		if err := recordGitHubIssueTaskProvenanceWithExecutor(ctx, conn, in.ProjectID, binding.AutomationID, existing.ID, in.IssueResourceID, binding.VersionID, in.TargetNodeID); err != nil {
			return nil, false, err
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return nil, false, err
		}
		committed = true
		return existing, false, nil
	}

	in.Task.ProjectID = in.ProjectID
	if err := taskRepo.createWithExecutor(ctx, conn, in.Task); err != nil {
		return nil, false, err
	}
	if in.Goal != nil && strings.TrimSpace(in.Goal.Objective) != "" {
		if err := createTaskGoalWithExecutor(ctx, conn, in.Task.ID, in.Goal); err != nil {
			return nil, false, err
		}
	}
	targetBinding := binding
	targetBinding.NodeID = in.TargetNodeID
	activityKey := "work-item:" + binding.WorkItemID + ":implementation-task"
	_, _, err = recordProjectionEventWithExecutor(ctx, conn, AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: in.ProjectID, Bindings: []models.AutomationBinding{targetBinding}},
		Binding: targetBinding, ActivityKey: activityKey, ActivityType: "create_task",
		ActivityStatus: models.AutomationActivityCompleted,
		Resources: []models.AutomationActivityResource{
			{ResourceType: "task", ResourceID: in.Task.ID, Relation: "child"},
			{ResourceType: "github_issue", ResourceID: in.IssueResourceID},
		},
		EventKey:   "work-item:" + binding.WorkItemID + ":implementation-task-entered",
		FromNodeID: binding.NodeID, ToNodeID: in.TargetNodeID, Transition: models.AutomationTransitionEntered,
	})
	if err != nil {
		return nil, false, err
	}
	if err := recordGitHubIssueTaskProvenanceWithExecutor(ctx, conn, in.ProjectID, binding.AutomationID, in.Task.ID, in.IssueResourceID, binding.VersionID, in.TargetNodeID); err != nil {
		return nil, false, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, false, err
	}
	committed = true
	r.PublishInvalidation(events.AutomationTransitionCreated, in.ProjectID, targetBinding)
	return in.Task, true, nil
}

func (r *AutomationRepo) RecordProjectionEvent(ctx context.Context, in AutomationProjectionEvent) (*models.AutomationWorkItem, *models.AutomationActivity, error) {
	if in.Context.ProjectID == "" || in.Binding.AutomationID == "" || in.Binding.VersionID == "" || in.Binding.NodeID == "" {
		return nil, nil, errors.New("complete automation binding is required")
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	workItem, activity, err := recordProjectionEventWithExecutor(ctx, conn, in)
	if err != nil {
		if strings.TrimSpace(in.EventKey) != "" {
			automationobs.Event("automation.transition.append_failure",
				automationobs.String("project_id", in.Context.ProjectID),
				automationobs.String("automation_id", in.Binding.AutomationID),
				automationobs.String("version_id", in.Binding.VersionID),
				automationobs.String("invocation_id", in.Binding.InvocationID),
				automationobs.String("node_id", in.Binding.NodeID),
				automationobs.String("work_item_id", in.Binding.WorkItemID))
		}
		return nil, nil, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, nil, err
	}
	committed = true
	binding := in.Binding
	if workItem != nil {
		binding.WorkItemID = workItem.ID
		binding.VersionID = workItem.OriginVersionID
		automationobs.DebugEvent("automation.work_item.created_or_updated",
			automationobs.String("project_id", in.Context.ProjectID), automationobs.String("automation_id", binding.AutomationID),
			automationobs.String("version_id", binding.VersionID), automationobs.String("invocation_id", binding.InvocationID),
			automationobs.String("node_id", binding.NodeID), automationobs.String("work_item_id", workItem.ID),
			automationobs.String("status", string(workItem.Status)))
		if workItem.Status == models.AutomationWorkItemCompleted || workItem.Status == models.AutomationWorkItemFailed || workItem.Status == models.AutomationWorkItemCancelled {
			automationobs.Event("automation.work_item.completed",
				automationobs.String("project_id", in.Context.ProjectID), automationobs.String("automation_id", binding.AutomationID),
				automationobs.String("version_id", binding.VersionID), automationobs.String("invocation_id", binding.InvocationID),
				automationobs.String("node_id", binding.NodeID), automationobs.String("work_item_id", workItem.ID),
				automationobs.String("status", string(workItem.Status)))
		}
	}
	activityID := ""
	if activity != nil {
		activityID = activity.ID
		automationobs.DebugEvent("automation.activity.created_or_updated",
			automationobs.String("project_id", in.Context.ProjectID), automationobs.String("automation_id", binding.AutomationID),
			automationobs.String("version_id", binding.VersionID), automationobs.String("invocation_id", binding.InvocationID),
			automationobs.String("activity_id", activity.ID), automationobs.String("node_id", binding.NodeID),
			automationobs.String("work_item_id", binding.WorkItemID), automationobs.String("status", string(activity.Status)))
		if activity.Status == models.AutomationActivityCompleted || activity.Status == models.AutomationActivityFailed || activity.Status == models.AutomationActivityCancelled {
			automationobs.Event("automation.activity.completed",
				automationobs.String("project_id", in.Context.ProjectID), automationobs.String("automation_id", binding.AutomationID),
				automationobs.String("version_id", binding.VersionID), automationobs.String("invocation_id", binding.InvocationID),
				automationobs.String("activity_id", activity.ID), automationobs.String("node_id", binding.NodeID),
				automationobs.String("work_item_id", binding.WorkItemID), automationobs.String("status", string(activity.Status)))
		}
	}
	if strings.TrimSpace(in.EventKey) != "" {
		automationobs.DebugEvent("automation.transition.appended",
			automationobs.String("project_id", in.Context.ProjectID), automationobs.String("automation_id", binding.AutomationID),
			automationobs.String("version_id", binding.VersionID), automationobs.String("invocation_id", binding.InvocationID),
			automationobs.String("activity_id", activityID),
			automationobs.String("node_id", binding.NodeID), automationobs.String("work_item_id", binding.WorkItemID),
			automationobs.String("state", string(in.Transition)))
	}
	eventType := events.AutomationResourceLinked
	if strings.TrimSpace(in.EventKey) != "" {
		eventType = events.AutomationTransitionCreated
	} else if workItem != nil {
		eventType = events.AutomationWorkItemUpdated
	}
	r.PublishInvalidation(eventType, in.Context.ProjectID, binding)
	return workItem, activity, nil
}

func recordProjectionEventWithExecutor(ctx context.Context, exec SQLExecutor, in AutomationProjectionEvent) (*models.AutomationWorkItem, *models.AutomationActivity, error) {
	if in.Context.ProjectID == "" || in.Binding.AutomationID == "" || in.Binding.VersionID == "" || in.Binding.NodeID == "" {
		return nil, nil, errors.New("complete automation binding is required")
	}
	binding := in.Binding
	var valid int
	if err := exec.QueryRowContext(ctx, `SELECT 1 FROM automation_nodes WHERE id = ? AND version_id = ? AND automation_id = ? AND project_id = ?`,
		binding.NodeID, binding.VersionID, binding.AutomationID, in.Context.ProjectID).Scan(&valid); err != nil {
		return nil, nil, fmt.Errorf("validating automation binding: %w", err)
	}
	var workItem *models.AutomationWorkItem
	var err error
	if strings.TrimSpace(in.WorkItemKey) != "" {
		workItem, binding, err = upsertAutomationWorkItem(ctx, exec, in, binding)
		if err != nil {
			return nil, nil, err
		}
	} else if strings.TrimSpace(binding.WorkItemID) != "" {
		workItem, err = scanAutomationWorkItem(exec.QueryRowContext(ctx, `SELECT id, project_id, automation_id, origin_version_id,
			COALESCE(origin_invocation_id, ''), COALESCE(parent_work_item_id, ''), work_item_key, kind, title, status,
			created_at, updated_at, completed_at FROM automation_work_items
			WHERE id = ? AND project_id = ? AND automation_id = ? AND origin_version_id = ?`,
			binding.WorkItemID, in.Context.ProjectID, binding.AutomationID, binding.VersionID))
		if err != nil {
			return nil, nil, fmt.Errorf("loading automation work item binding: %w", err)
		}
	}
	var activity *models.AutomationActivity
	if strings.TrimSpace(in.ActivityKey) != "" {
		activity, err = upsertAutomationActivity(ctx, exec, in, binding, workItem)
		if err != nil {
			return nil, nil, err
		}
	}
	if workItem != nil && strings.TrimSpace(in.EventKey) != "" {
		if err := appendAutomationTransition(ctx, exec, in, binding, workItem, activity); err != nil {
			return nil, nil, err
		}
	}
	return workItem, activity, nil
}

func upsertAutomationWorkItem(ctx context.Context, exec SQLExecutor, in AutomationProjectionEvent, binding models.AutomationBinding) (*models.AutomationWorkItem, models.AutomationBinding, error) {
	status := in.WorkItemStatus
	if status == "" {
		status = models.AutomationWorkItemActive
	}
	kind := strings.TrimSpace(in.WorkItemKind)
	if kind == "" {
		kind = "work"
	}
	workItemKey := strings.TrimSpace(in.WorkItemKey)
	if err := discardStaleAutomationWorkItemProjection(ctx, exec, in.Context.ProjectID, binding.AutomationID, workItemKey); err != nil {
		return nil, binding, err
	}
	_, err := exec.ExecContext(ctx, `INSERT INTO automation_work_items
		(project_id, automation_id, origin_version_id, origin_invocation_id, work_item_key, kind, title, status)
		VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?)
		ON CONFLICT(automation_id, work_item_key) DO UPDATE SET title = CASE WHEN excluded.title = '' THEN title ELSE excluded.title END,
		updated_at = CURRENT_TIMESTAMP`, in.Context.ProjectID, binding.AutomationID, binding.VersionID, binding.InvocationID,
		workItemKey, kind, strings.TrimSpace(in.WorkItemTitle), status)
	if err != nil {
		return nil, binding, fmt.Errorf("upserting automation work item: %w", err)
	}
	item, err := scanAutomationWorkItem(exec.QueryRowContext(ctx, `SELECT id, project_id, automation_id, origin_version_id,
		COALESCE(origin_invocation_id, ''), COALESCE(parent_work_item_id, ''), work_item_key, kind, title, status,
		created_at, updated_at, completed_at FROM automation_work_items WHERE automation_id = ? AND work_item_key = ?`,
		binding.AutomationID, strings.TrimSpace(in.WorkItemKey)))
	if err != nil {
		return nil, binding, err
	}
	if item.ProjectID != in.Context.ProjectID {
		return nil, binding, errors.New("automation work item project mismatch")
	}
	if item.OriginVersionID != binding.VersionID {
		var nodeKey string
		if err := exec.QueryRowContext(ctx, `SELECT node_key FROM automation_nodes WHERE id = ? AND version_id = ? AND automation_id = ? AND project_id = ?`,
			binding.NodeID, binding.VersionID, binding.AutomationID, in.Context.ProjectID).Scan(&nodeKey); err != nil {
			return nil, binding, err
		}
		if err := exec.QueryRowContext(ctx, `SELECT id FROM automation_nodes WHERE version_id = ? AND automation_id = ? AND project_id = ? AND node_key = ?`,
			item.OriginVersionID, binding.AutomationID, in.Context.ProjectID, nodeKey).Scan(&binding.NodeID); err != nil {
			return nil, binding, fmt.Errorf("mapping work item to origin topology: %w", err)
		}
		binding.VersionID = item.OriginVersionID
	}
	binding.WorkItemID = item.ID
	return item, binding, nil
}

func discardStaleAutomationWorkItemProjection(ctx context.Context, exec SQLExecutor, projectID, automationID, workItemKey string) error {
	if strings.TrimSpace(workItemKey) == "" {
		return nil
	}
	item, err := scanAutomationWorkItem(exec.QueryRowContext(ctx, `SELECT id, project_id, automation_id, origin_version_id,
		COALESCE(origin_invocation_id, ''), COALESCE(parent_work_item_id, ''), work_item_key, kind, title, status,
		created_at, updated_at, completed_at FROM automation_work_items WHERE automation_id = ? AND work_item_key = ?`,
		automationID, workItemKey))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if item.ProjectID != projectID {
		return errors.New("automation work item project mismatch")
	}
	var versionExists int
	if err := exec.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_versions WHERE id = ? AND automation_id = ? AND project_id = ?`,
		item.OriginVersionID, automationID, projectID).Scan(&versionExists); err != nil {
		return err
	}
	if versionExists > 0 {
		return nil
	}
	workItemIDs, err := staleAutomationWorkItemDescendantIDs(ctx, exec, projectID, automationID, item.ID)
	if err != nil {
		return err
	}
	if len(workItemIDs) == 0 {
		return nil
	}
	placeholders, args := automationWorkItemIDArgs(workItemIDs)
	if _, err := exec.ExecContext(ctx, `DELETE FROM automation_thread_input_bindings WHERE work_item_id IN (`+placeholders+`)`, args...); err != nil {
		return fmt.Errorf("discarding stale automation thread input bindings: %w", err)
	}
	if _, err := exec.ExecContext(ctx, `DELETE FROM automation_transitions WHERE work_item_id IN (`+placeholders+`)`, args...); err != nil {
		return fmt.Errorf("discarding stale automation transitions: %w", err)
	}
	if _, err := exec.ExecContext(ctx, `DELETE FROM automation_work_item_positions WHERE work_item_id IN (`+placeholders+`)`, args...); err != nil {
		return fmt.Errorf("discarding stale automation work item positions: %w", err)
	}
	if _, err := exec.ExecContext(ctx, `DELETE FROM automation_activity_resources
		WHERE activity_id IN (SELECT id FROM automation_activities WHERE work_item_id IN (`+placeholders+`))`, args...); err != nil {
		return fmt.Errorf("discarding stale automation activity resources: %w", err)
	}
	if _, err := exec.ExecContext(ctx, `DELETE FROM automation_activities WHERE work_item_id IN (`+placeholders+`)`, args...); err != nil {
		return fmt.Errorf("discarding stale automation activities: %w", err)
	}
	for _, workItemID := range workItemIDs {
		if _, err := exec.ExecContext(ctx, `DELETE FROM automation_work_items WHERE id = ? AND automation_id = ? AND project_id = ?`, workItemID, automationID, projectID); err != nil {
			return fmt.Errorf("discarding stale automation work item: %w", err)
		}
	}
	return nil
}

func staleAutomationWorkItemDescendantIDs(ctx context.Context, exec SQLExecutor, projectID, automationID, rootWorkItemID string) ([]string, error) {
	rows, err := exec.QueryContext(ctx, `WITH RECURSIVE descendants(id, depth) AS (
		SELECT id, 0 FROM automation_work_items WHERE id = ? AND automation_id = ? AND project_id = ?
		UNION ALL
		SELECT child.id, descendants.depth + 1 FROM automation_work_items child
		JOIN descendants ON child.parent_work_item_id = descendants.id
		WHERE child.automation_id = ? AND child.project_id = ?
	)
	SELECT id FROM descendants ORDER BY depth DESC`, rootWorkItemID, automationID, projectID, automationID, projectID)
	if err != nil {
		return nil, fmt.Errorf("loading stale automation work item descendants: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func automationWorkItemIDArgs(ids []string) (string, []any) {
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	return placeholders, args
}

func upsertAutomationActivity(ctx context.Context, exec SQLExecutor, in AutomationProjectionEvent, binding models.AutomationBinding, item *models.AutomationWorkItem) (*models.AutomationActivity, error) {
	status := in.ActivityStatus
	if status == "" {
		status = models.AutomationActivityRunning
	}
	workItemID := binding.WorkItemID
	if item != nil {
		workItemID = item.ID
	}
	_, err := exec.ExecContext(ctx, `INSERT INTO automation_activities
		(project_id, automation_id, version_id, node_id, invocation_id, work_item_id, activity_key, activity_type, status, completed_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?,
			CASE WHEN ? IN ('completed','failed','cancelled') THEN CURRENT_TIMESTAMP ELSE NULL END)
		ON CONFLICT(automation_id, version_id, activity_key) DO UPDATE SET status = excluded.status,
		invocation_id = COALESCE(automation_activities.invocation_id, excluded.invocation_id),
		work_item_id = COALESCE(automation_activities.work_item_id, excluded.work_item_id),
		completed_at = CASE WHEN excluded.status IN ('completed','failed','cancelled') THEN CURRENT_TIMESTAMP ELSE completed_at END`,
		in.Context.ProjectID, binding.AutomationID, binding.VersionID, binding.NodeID, binding.InvocationID, workItemID,
		strings.TrimSpace(in.ActivityKey), strings.TrimSpace(in.ActivityType), status, status)
	if err != nil {
		return nil, fmt.Errorf("upserting automation activity: %w", err)
	}
	activity, err := scanAutomationActivity(exec.QueryRowContext(ctx, `SELECT id, project_id, automation_id, version_id, node_id,
		COALESCE(invocation_id, ''), COALESCE(work_item_id, ''), activity_key, activity_type, status, started_at, completed_at,
		error_message FROM automation_activities WHERE automation_id = ? AND version_id = ? AND activity_key = ?`,
		binding.AutomationID, binding.VersionID, strings.TrimSpace(in.ActivityKey)))
	if err != nil {
		return nil, err
	}
	for _, resource := range in.Resources {
		if strings.TrimSpace(resource.ResourceType) == "" || strings.TrimSpace(resource.ResourceID) == "" {
			continue
		}
		if err := validateAutomationActivityResource(ctx, exec, in.Context.ProjectID, resource.ResourceType, resource.ResourceID); err != nil {
			return nil, err
		}
		relation := strings.TrimSpace(resource.Relation)
		if relation == "" {
			relation = "subject"
		}
		if _, err := exec.ExecContext(ctx, `INSERT INTO automation_activity_resources
			(activity_id, resource_type, resource_id, relation) VALUES (?, ?, ?, ?)
			ON CONFLICT(activity_id, resource_type, resource_id, relation) DO NOTHING`, activity.ID,
			resource.ResourceType, resource.ResourceID, relation); err != nil {
			return nil, fmt.Errorf("linking automation activity resource: %w", err)
		}
	}
	if err := syncAutomationLiveActivityState(ctx, exec, activity.ID); err != nil {
		return nil, err
	}
	if err := recordAutomationArtifactMailboxOwners(ctx, exec, in, binding); err != nil {
		return nil, err
	}
	return activity, nil
}

func syncAutomationLiveActivityState(ctx context.Context, exec SQLExecutor, activityID string) error {
	if strings.TrimSpace(activityID) == "" {
		return nil
	}
	if _, err := exec.ExecContext(ctx, `DELETE FROM automation_live_activity_states WHERE activity_id = ?`, activityID); err != nil {
		return fmt.Errorf("clearing automation live activity state: %w", err)
	}
	rows, err := exec.QueryContext(ctx, `SELECT a.rowid, a.id, a.project_id, a.automation_id, a.version_id, a.node_id,
		COALESCE(a.invocation_id, ''), COALESCE(a.work_item_id, ''), a.status, a.completed_at,
		CASE WHEN a.work_item_id IS NOT NULL THEN 'work:' || a.work_item_id
			WHEN task_resource.resource_id IS NOT NULL THEN 'task:' || task_resource.resource_id
			ELSE 'activity:' || a.id END AS state_key
		FROM automation_activities a
		LEFT JOIN automation_activity_resources task_resource ON task_resource.activity_id = a.id
			AND task_resource.resource_type = 'task' AND task_resource.relation = 'subject'
		WHERE a.id = ?`, activityID)
	if err != nil {
		return fmt.Errorf("loading automation live activity state: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rowID int64
		var id, projectID, automationID, versionID, nodeID, invocationID, workItemID, status, stateKey string
		var completedAt sql.NullTime
		if err := rows.Scan(&rowID, &id, &projectID, &automationID, &versionID, &nodeID, &invocationID, &workItemID, &status, &completedAt, &stateKey); err != nil {
			return err
		}
		var completed any
		if completedAt.Valid {
			completed = completedAt.Time
		}
		if _, err := exec.ExecContext(ctx, `INSERT INTO automation_live_activity_states
			(project_id, automation_id, version_id, node_id, state_key, activity_id, invocation_id, work_item_id, activity_status, completed_at, activity_rowid)
			VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)
			ON CONFLICT(project_id, automation_id, version_id, node_id, state_key) DO UPDATE SET
				activity_id = excluded.activity_id,
				invocation_id = excluded.invocation_id,
				work_item_id = excluded.work_item_id,
				activity_status = excluded.activity_status,
				completed_at = excluded.completed_at,
				activity_rowid = excluded.activity_rowid,
				updated_at = CURRENT_TIMESTAMP
			WHERE excluded.activity_rowid >= automation_live_activity_states.activity_rowid`,
			projectID, automationID, versionID, nodeID, stateKey, id, invocationID, workItemID, status, completed, rowID); err != nil {
			return fmt.Errorf("upserting automation live activity state: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func syncAutomationLiveActivityStateRows(ctx context.Context, exec SQLExecutor, rows *sql.Rows) error {
	var activityIDs []string
	for rows.Next() {
		var activityID string
		if err := rows.Scan(&activityID); err != nil {
			_ = rows.Close()
			return err
		}
		activityIDs = append(activityIDs, activityID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, activityID := range activityIDs {
		if err := syncAutomationLiveActivityState(ctx, exec, activityID); err != nil {
			return err
		}
	}
	return nil
}

func recordAutomationArtifactMailboxOwners(ctx context.Context, exec SQLExecutor, in AutomationProjectionEvent, binding models.AutomationBinding) error {
	if strings.TrimSpace(in.ActivityType) != "create_notification" {
		return nil
	}
	for _, resource := range in.Resources {
		if strings.TrimSpace(resource.ResourceType) != "alert" || strings.TrimSpace(resource.ResourceID) == "" {
			continue
		}
		if _, err := exec.ExecContext(ctx, `INSERT INTO automation_artifact_mailbox_owners
			(project_id, automation_id, artifact_type, artifact_id, producer_node_key, action_node_key, gate_node_key, mailbox_node_key)
			VALUES (?, ?, 'alert', ?, '', '', '', '')
			ON CONFLICT(project_id, automation_id, artifact_type, artifact_id, producer_node_key, action_node_key, gate_node_key, mailbox_node_key) DO NOTHING`,
			in.Context.ProjectID, binding.AutomationID, strings.TrimSpace(resource.ResourceID)); err != nil {
			return fmt.Errorf("recording Automation artifact mailbox ownership: %w", err)
		}
	}
	return nil
}

func validateAutomationActivityResource(ctx context.Context, exec SQLExecutor, projectID, resourceType, resourceID string) error {
	resourceType = strings.TrimSpace(resourceType)
	resourceID = strings.TrimSpace(resourceID)
	var query string
	switch resourceType {
	case "task":
		query = `SELECT 1 FROM tasks WHERE id = ? AND project_id = ?`
	case "execution":
		query = `SELECT 1 FROM executions e JOIN tasks t ON t.id = e.task_id WHERE e.id = ? AND t.project_id = ?`
	case "alert":
		query = `SELECT 1 FROM alerts WHERE id = ? AND project_id = ?`
	case "goal":
		query = `SELECT 1 FROM task_goals g JOIN tasks t ON t.id = g.task_id WHERE g.task_id = ? AND t.project_id = ?`
	case "workflow_execution":
		query = `SELECT 1 FROM workflow_executions we JOIN workflows w ON w.id = we.workflow_id WHERE we.id = ? AND w.project_id = ?`
	case "pull_request", "github_issue", "review":
		if err := validateCanonicalGitHubResourceID(resourceType, resourceID); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported automation activity resource type %q", resourceType)
	}
	var exists int
	if err := exec.QueryRowContext(ctx, query, resourceID, projectID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("automation %s resource %q does not belong to project", resourceType, resourceID)
		}
		return err
	}
	return nil
}

func validateCanonicalGitHubResourceID(resourceType, resourceID string) error {
	parts := strings.Split(resourceID, ":")
	expectedParts := 4
	if resourceType == "review" {
		expectedParts = 5
	}
	if len(parts) != expectedParts || parts[0] != "github" || !strings.Contains(parts[1], "/") {
		return fmt.Errorf("%s resource identity must be canonical and repository-qualified", resourceType)
	}
	expectedKind := "issue"
	if resourceType == "pull_request" {
		expectedKind = "pull"
	} else if resourceType == "review" {
		expectedKind = "review"
	}
	if parts[2] != expectedKind {
		return fmt.Errorf("%s resource identity has invalid kind", resourceType)
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number < 1 {
		return fmt.Errorf("%s resource identity has invalid number", resourceType)
	}
	if resourceType == "review" {
		reviewID, reviewErr := strconv.Atoi(parts[4])
		if reviewErr != nil || reviewID < 1 {
			return fmt.Errorf("review resource identity has invalid review id")
		}
	}
	repoParts := strings.Split(parts[1], "/")
	if len(repoParts) != 2 || !githubResourceNamePattern.MatchString(repoParts[0]) ||
		!githubResourceNamePattern.MatchString(repoParts[1]) || repoParts[0] == "." || repoParts[0] == ".." ||
		repoParts[1] == "." || repoParts[1] == ".." {
		return fmt.Errorf("%s resource identity must include a valid owner/repository", resourceType)
	}
	return nil
}

func appendAutomationTransition(ctx context.Context, exec SQLExecutor, in AutomationProjectionEvent, binding models.AutomationBinding, item *models.AutomationWorkItem, activity *models.AutomationActivity) error {
	toNodeID := in.ToNodeID
	if toNodeID == "" {
		toNodeID = binding.NodeID
	}
	activityID := ""
	if activity != nil {
		activityID = activity.ID
	}
	metadata := strings.TrimSpace(in.MetadataJSON)
	if metadata == "" {
		metadata = "{}"
	}
	edgeID := strings.TrimSpace(in.EdgeID)
	if edgeID == "" && strings.TrimSpace(in.FromNodeID) != "" {
		var resolved string
		err := exec.QueryRowContext(ctx, `SELECT id FROM automation_edges
			WHERE project_id = ? AND automation_id = ? AND version_id = ? AND source_node_id = ? AND target_node_id = ?
			ORDER BY display_order, id LIMIT 1`, in.Context.ProjectID, binding.AutomationID, binding.VersionID,
			in.FromNodeID, toNodeID).Scan(&resolved)
		if err == nil {
			edgeID = resolved
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	result, err := exec.ExecContext(ctx, `INSERT INTO automation_transitions
		(project_id, automation_id, version_id, work_item_id, invocation_id, activity_id, from_node_id, to_node_id,
		 edge_id, event_key, state, metadata_json)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?, ?)
		ON CONFLICT(automation_id, version_id, event_key) DO NOTHING`, in.Context.ProjectID, binding.AutomationID,
		binding.VersionID, item.ID, binding.InvocationID, activityID, in.FromNodeID, toNodeID, edgeID,
		strings.TrimSpace(in.EventKey), in.Transition, metadata)
	if err != nil {
		return fmt.Errorf("appending automation transition: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return nil
	}
	if in.FromNodeID != "" {
		if _, err := exec.ExecContext(ctx, `DELETE FROM automation_work_item_positions WHERE work_item_id = ? AND node_id = ?`, item.ID, in.FromNodeID); err != nil {
			return err
		}
	}
	workStatus := models.AutomationWorkItemActive
	positionState := models.AutomationPositionActive
	terminal := false
	switch in.Transition {
	case models.AutomationTransitionWaiting:
		workStatus, positionState = models.AutomationWorkItemWaiting, models.AutomationPositionWaiting
	case models.AutomationTransitionBlocked:
		workStatus, positionState = models.AutomationWorkItemBlocked, models.AutomationPositionBlocked
	case models.AutomationTransitionFailed:
		workStatus, positionState = models.AutomationWorkItemFailed, models.AutomationPositionFailed
	case models.AutomationTransitionCompleted:
		workStatus, terminal = models.AutomationWorkItemCompleted, true
	case models.AutomationTransitionCancelled:
		workStatus, terminal = models.AutomationWorkItemCancelled, true
	}
	if terminal {
		var positionStatus string
		err := exec.QueryRowContext(ctx, `SELECT CASE
			WHEN SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END) > 0 THEN 'failed'
			WHEN SUM(CASE WHEN state = 'blocked' THEN 1 ELSE 0 END) > 0 THEN 'blocked'
			WHEN SUM(CASE WHEN state = 'waiting' THEN 1 ELSE 0 END) > 0 THEN 'waiting'
			WHEN COUNT(*) > 0 THEN 'active'
			ELSE '' END
			FROM automation_work_item_positions WHERE work_item_id = ?`, item.ID).Scan(&positionStatus)
		if err != nil {
			return err
		}
		if positionStatus != "" {
			_, err = exec.ExecContext(ctx, `UPDATE automation_work_items SET status = ?, completed_at = NULL,
				updated_at = CURRENT_TIMESTAMP WHERE id = ?`, positionStatus, item.ID)
			return err
		}

		var pendingActivities, waitingActivities, claimedAlerts, queuedInputs int
		if err := exec.QueryRowContext(ctx, `SELECT
				COALESCE(SUM(CASE WHEN status IN ('pending','running','waiting') THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN status = 'waiting' THEN 1 ELSE 0 END), 0)
				FROM automation_activities WHERE work_item_id = ?`, item.ID).Scan(&pendingActivities, &waitingActivities); err != nil {
			return err
		}
		if err := exec.QueryRowContext(ctx, `SELECT COUNT(DISTINCT al.id)
			FROM alerts al
			JOIN automation_activity_resources ar ON ar.resource_type = 'alert' AND ar.resource_id = al.id
			JOIN automation_activities a ON a.id = ar.activity_id
			WHERE a.work_item_id = ? AND al.processing_state = 'claimed'`, item.ID).Scan(&claimedAlerts); err != nil {
			return err
		}
		if err := exec.QueryRowContext(ctx, `SELECT COUNT(DISTINCT ti.id)
			FROM automation_thread_input_bindings b
			JOIN thread_inputs ti ON ti.id = b.thread_input_id
			WHERE b.work_item_id = ? AND ti.input_status = 'pending'`, item.ID).Scan(&queuedInputs); err != nil {
			return err
		}
		if pendingActivities > 0 || claimedAlerts > 0 || queuedInputs > 0 {
			openStatus := models.AutomationWorkItemActive
			if waitingActivities > 0 || claimedAlerts > 0 || queuedInputs > 0 {
				openStatus = models.AutomationWorkItemWaiting
			}
			_, err = exec.ExecContext(ctx, `UPDATE automation_work_items SET status = ?, completed_at = NULL,
				updated_at = CURRENT_TIMESTAMP WHERE id = ?`, openStatus, item.ID)
			return err
		}
		_, err = exec.ExecContext(ctx, `UPDATE automation_work_items SET status = ?, completed_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP WHERE id = ?`, workStatus, item.ID)
		return err
	}
	if _, err := exec.ExecContext(ctx, `INSERT INTO automation_work_item_positions
		(work_item_id, project_id, automation_id, version_id, node_id, state) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(work_item_id, node_id) DO UPDATE SET state = excluded.state, updated_at = CURRENT_TIMESTAMP`,
		item.ID, in.Context.ProjectID, binding.AutomationID, binding.VersionID, toNodeID, positionState); err != nil {
		return err
	}
	_, err = exec.ExecContext(ctx, `UPDATE automation_work_items SET status = ?, completed_at = NULL,
		updated_at = CURRENT_TIMESTAMP WHERE id = ?`, workStatus, item.ID)
	return err
}

func (r *AutomationRepo) LiveNodeCounts(ctx context.Context, projectID, automationID, versionID string, recentCutoff time.Time) (map[string]models.AutomationNodeCounts, int, int, error) {
	rows, err := r.db.QueryContext(ctx, liveNodeCountsSQL, projectID, automationID, versionID,
		projectID, automationID, versionID, recentCutoff.UTC(),
		projectID, automationID, versionID,
		projectID, automationID, versionID,
		projectID, automationID, versionID,
		projectID, automationID, versionID, recentCutoff.UTC())
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	counts := make(map[string]models.AutomationNodeCounts)
	for rows.Next() {
		var nodeID string
		var value models.AutomationNodeCounts
		if err := rows.Scan(&nodeID, &value.Running, &value.Waiting, &value.Blocked, &value.Failed, &value.CompletedRecently); err != nil {
			return nil, 0, 0, err
		}
		counts[nodeID] = value
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	var activeInvocations, activeWorkItems int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_invocations
		WHERE project_id = ? AND automation_id = ? AND status IN ('claimed','dispatched','running')`, projectID, automationID).Scan(&activeInvocations); err != nil {
		return nil, 0, 0, err
	}
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_work_items
		WHERE project_id = ? AND automation_id = ? AND status IN ('active','waiting','blocked','failed')`, projectID, automationID).Scan(&activeWorkItems); err != nil {
		return nil, 0, 0, err
	}
	return counts, activeInvocations, activeWorkItems, nil
}

func (r *AutomationRepo) PortfolioOperationalCounts(ctx context.Context, projectID string, recentCutoff time.Time) (map[string]models.AutomationNodeCounts, error) {
	rows, err := r.db.QueryContext(ctx, `WITH ranked_activities AS (
			SELECT a.automation_id, a.work_item_id, a.id, a.status, a.completed_at, task_resource.resource_id AS task_id,
				ROW_NUMBER() OVER (PARTITION BY a.automation_id, CASE
					WHEN a.work_item_id IS NOT NULL THEN 'work:' || a.work_item_id
					WHEN task_resource.resource_id IS NOT NULL THEN 'task:' || task_resource.resource_id
					ELSE 'activity:' || a.id END
					ORDER BY a.rowid DESC) AS activity_rank
			FROM automation_activities a
			LEFT JOIN automation_activity_resources task_resource ON task_resource.activity_id = a.id
				AND task_resource.resource_type = 'task' AND task_resource.relation = 'subject'
			WHERE a.project_id = ?
		), operational_state AS (
			SELECT automation_id, CASE status
				WHEN 'pending' THEN 'running' WHEN 'running' THEN 'running' WHEN 'waiting' THEN 'waiting'
				WHEN 'failed' THEN 'failed' WHEN 'completed' THEN 'recent' END AS state,
				CASE WHEN work_item_id IS NOT NULL THEN 'work:' || work_item_id
					WHEN task_id IS NOT NULL THEN 'task:' || task_id ELSE 'activity:' || id END AS state_key
			FROM ranked_activities
			WHERE activity_rank = 1
				AND (status IN ('pending','running','waiting','failed') OR (status = 'completed' AND completed_at >= ?))
		UNION
		SELECT binding.automation_id, 'running', CASE WHEN binding.work_item_id IS NOT NULL
			THEN 'work:' || binding.work_item_id ELSE 'input:' || binding.thread_input_id END
		FROM automation_thread_input_bindings binding
		JOIN thread_inputs input ON input.id = binding.thread_input_id
		WHERE binding.project_id = ? AND input.input_status = 'pending'
		UNION
		SELECT position.automation_id,
			CASE WHEN position.state = 'active' THEN 'running' WHEN position.state = 'waiting' THEN 'waiting'
				WHEN position.state = 'blocked' THEN 'blocked' WHEN position.state = 'failed' THEN 'failed' END,
			'work:' || position.work_item_id
		FROM automation_work_item_positions position
		JOIN automation_nodes node ON node.id = position.node_id AND node.version_id = position.version_id
			AND node.automation_id = position.automation_id AND node.project_id = position.project_id
		WHERE position.project_id = ? AND position.state IN ('active','waiting','blocked','failed')
			AND NOT (position.state = 'active' AND node.role = 'github_inbox')
		UNION
		SELECT automation_id, 'recent', 'work:' || work_item_id
		FROM automation_transitions
		WHERE project_id = ? AND state = 'completed' AND occurred_at >= ?
		), identity_state AS (
			SELECT automation_id, state_key, MAX(CASE state
				WHEN 'failed' THEN 5 WHEN 'blocked' THEN 4 WHEN 'waiting' THEN 3
				WHEN 'running' THEN 2 WHEN 'recent' THEN 1 ELSE 0 END) AS state_priority
			FROM operational_state GROUP BY automation_id, state_key
		)
		SELECT automation_id,
			SUM(CASE WHEN state_priority = 2 THEN 1 ELSE 0 END),
			SUM(CASE WHEN state_priority = 3 THEN 1 ELSE 0 END),
			SUM(CASE WHEN state_priority = 4 THEN 1 ELSE 0 END),
			SUM(CASE WHEN state_priority = 5 THEN 1 ELSE 0 END),
			SUM(CASE WHEN state_priority = 1 THEN 1 ELSE 0 END)
		FROM identity_state GROUP BY automation_id`, projectID, recentCutoff.UTC(), projectID, projectID, projectID, recentCutoff.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]models.AutomationNodeCounts)
	for rows.Next() {
		var automationID string
		var counts models.AutomationNodeCounts
		if err := rows.Scan(&automationID, &counts.Running, &counts.Waiting, &counts.Blocked, &counts.Failed, &counts.CompletedRecently); err != nil {
			return nil, err
		}
		out[automationID] = counts
	}
	return out, rows.Err()
}

func (r *AutomationRepo) LiveEdgeCounts(ctx context.Context, projectID, automationID, versionID string, recentCutoff time.Time) (map[string][2]int, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT edge_id, COUNT(*),
		SUM(CASE WHEN occurred_at >= ? THEN 1 ELSE 0 END)
		FROM automation_transitions WHERE project_id = ? AND automation_id = ? AND version_id = ? AND edge_id IS NOT NULL
		GROUP BY edge_id`, recentCutoff.UTC(), projectID, automationID, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][2]int)
	for rows.Next() {
		var edgeID string
		var total, recent int
		if err := rows.Scan(&edgeID, &total, &recent); err != nil {
			return nil, err
		}
		out[edgeID] = [2]int{total, recent}
	}
	return out, rows.Err()
}

func (r *AutomationRepo) GetNodeByKey(ctx context.Context, projectID, automationID, versionID, nodeKey string) (*models.AutomationNode, error) {
	var node models.AutomationNode
	err := r.db.QueryRowContext(ctx, `SELECT id, project_id, automation_id, version_id, node_key, name, node_type,
		role, config_json, position_x, position_y, created_at, updated_at FROM automation_nodes
		WHERE project_id = ? AND automation_id = ? AND version_id = ? AND node_key = ?`,
		projectID, automationID, versionID, nodeKey).Scan(&node.ID, &node.ProjectID, &node.AutomationID, &node.VersionID,
		&node.NodeKey, &node.Name, &node.NodeType, &node.Role, &node.ConfigJSON, &node.PositionX, &node.PositionY,
		&node.CreatedAt, &node.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &node, err
}

func (r *AutomationRepo) IsCurrentActiveBinding(ctx context.Context, projectID string, binding models.AutomationBinding) (bool, error) {
	if r == nil || strings.TrimSpace(projectID) == "" || strings.TrimSpace(binding.AutomationID) == "" ||
		strings.TrimSpace(binding.VersionID) == "" || strings.TrimSpace(binding.NodeID) == "" {
		return false, errors.New("complete automation binding is required")
	}
	var current int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automations a
		JOIN automation_nodes n ON n.project_id = a.project_id AND n.automation_id = a.id
			AND n.version_id = a.published_version_id AND n.id = ?
		WHERE a.project_id = ? AND a.id = ? AND a.published_version_id = ? AND a.lifecycle_state = 'active'`,
		binding.NodeID, projectID, binding.AutomationID, binding.VersionID).Scan(&current)
	return current == 1, err
}

func (r *AutomationRepo) CurrentActiveBindingForLaunchNode(ctx context.Context, projectID string, binding models.AutomationBinding, targetRole string) (models.AutomationBinding, bool, error) {
	if r == nil || strings.TrimSpace(projectID) == "" || strings.TrimSpace(binding.AutomationID) == "" ||
		strings.TrimSpace(binding.VersionID) == "" || strings.TrimSpace(binding.NodeID) == "" || strings.TrimSpace(binding.InvocationID) == "" || strings.TrimSpace(targetRole) == "" {
		return models.AutomationBinding{}, false, errors.New("complete launched automation binding is required")
	}
	var launched int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_invocations
		WHERE project_id = ? AND automation_id = ? AND version_id = ? AND id = ?`,
		projectID, binding.AutomationID, binding.VersionID, binding.InvocationID).Scan(&launched); err != nil {
		return models.AutomationBinding{}, false, err
	}
	if launched != 1 {
		return models.AutomationBinding{}, false, nil
	}
	var sourceKey, sourceRole string
	if err := r.db.QueryRowContext(ctx, `SELECT node_key, role FROM automation_nodes
		WHERE project_id = ? AND automation_id = ? AND version_id = ? AND id = ?`,
		projectID, binding.AutomationID, binding.VersionID, binding.NodeID).Scan(&sourceKey, &sourceRole); errors.Is(err, sql.ErrNoRows) {
		return models.AutomationBinding{}, false, nil
	} else if err != nil {
		return models.AutomationBinding{}, false, err
	}
	var currentVersionID, currentNodeID string
	err := r.db.QueryRowContext(ctx, `SELECT a.published_version_id, n.id
		FROM automations a JOIN automation_nodes n ON n.project_id = a.project_id AND n.automation_id = a.id
			AND n.version_id = a.published_version_id AND n.node_key = ? AND n.role = ?
		WHERE a.project_id = ? AND a.id = ? AND a.lifecycle_state = 'active'`,
		sourceKey, sourceRole, projectID, binding.AutomationID).Scan(&currentVersionID, &currentNodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return models.AutomationBinding{}, false, nil
	}
	if err != nil {
		return models.AutomationBinding{}, false, err
	}
	connected, err := r.GetConnectedNodeByRole(ctx, projectID, binding.AutomationID, currentVersionID, currentNodeID, strings.TrimSpace(targetRole), true)
	if err != nil || connected == nil {
		return models.AutomationBinding{}, false, err
	}
	return models.AutomationBinding{AutomationID: binding.AutomationID, VersionID: currentVersionID, InvocationID: binding.InvocationID, NodeID: currentNodeID, WorkItemID: binding.WorkItemID}, true, nil
}

func (r *AutomationRepo) CurrentActiveBindingForNodeKey(ctx context.Context, projectID, automationID, nodeKey, targetRole string) (models.AutomationBinding, bool, error) {
	if r == nil || strings.TrimSpace(projectID) == "" || strings.TrimSpace(automationID) == "" || strings.TrimSpace(nodeKey) == "" || strings.TrimSpace(targetRole) == "" {
		return models.AutomationBinding{}, false, errors.New("complete automation node key binding is required")
	}
	var currentVersionID, currentNodeID string
	err := r.db.QueryRowContext(ctx, `SELECT a.published_version_id, n.id
		FROM automations a JOIN automation_nodes n ON n.project_id = a.project_id AND n.automation_id = a.id
			AND n.version_id = a.published_version_id AND n.node_key = ?
		WHERE a.project_id = ? AND a.id = ? AND a.lifecycle_state = 'active'`,
		strings.TrimSpace(nodeKey), projectID, automationID).Scan(&currentVersionID, &currentNodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return models.AutomationBinding{}, false, nil
	}
	if err != nil {
		return models.AutomationBinding{}, false, err
	}
	connected, err := r.GetConnectedNodeByRole(ctx, projectID, automationID, currentVersionID, currentNodeID, strings.TrimSpace(targetRole), true)
	if err != nil || connected == nil {
		return models.AutomationBinding{}, false, err
	}
	return models.AutomationBinding{AutomationID: automationID, VersionID: currentVersionID, NodeID: currentNodeID}, true, nil
}

func (r *AutomationRepo) GetConnectedNodeByRole(ctx context.Context, projectID, automationID, versionID, nodeID, role string, outgoing bool) (*models.AutomationNode, error) {
	anchorColumn := "source.id"
	selectedAlias := "target"
	if !outgoing {
		anchorColumn = "target.id"
		selectedAlias = "source"
	}
	query := fmt.Sprintf(`SELECT %[1]s.id, %[1]s.project_id, %[1]s.automation_id, %[1]s.version_id,
		%[1]s.node_key, %[1]s.name, %[1]s.node_type, %[1]s.role, %[1]s.config_json, %[1]s.position_x, %[1]s.position_y,
		%[1]s.created_at, %[1]s.updated_at
		FROM automation_edges edge
		JOIN automation_nodes source ON source.project_id = edge.project_id AND source.automation_id = edge.automation_id
			AND source.version_id = edge.version_id AND source.id = edge.source_node_id
		JOIN automation_nodes target ON target.project_id = edge.project_id AND target.automation_id = edge.automation_id
			AND target.version_id = edge.version_id AND target.id = edge.target_node_id
		WHERE edge.project_id = ? AND edge.automation_id = ? AND edge.version_id = ? AND %[2]s = ? AND %[1]s.role = ?
		ORDER BY edge.display_order, edge.id LIMIT 1`, selectedAlias, anchorColumn)
	var node models.AutomationNode
	err := r.db.QueryRowContext(ctx, query, projectID, automationID, versionID, nodeID, role).
		Scan(&node.ID, &node.ProjectID, &node.AutomationID, &node.VersionID, &node.NodeKey, &node.Name, &node.NodeType,
			&node.Role, &node.ConfigJSON, &node.PositionX, &node.PositionY, &node.CreatedAt, &node.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &node, err
}

type CustomAutomationTaskHandoff struct {
	Node   models.AutomationNode
	TaskID string
}

func (r *AutomationRepo) ListCustomTaskHandoffs(ctx context.Context, projectID, automationID, versionID, sourceNodeID string) (bool, []CustomAutomationTaskHandoff, error) {
	var adapterKey string
	err := r.db.QueryRowContext(ctx, `SELECT adapter_key FROM automation_versions
		WHERE project_id = ? AND automation_id = ? AND id = ? AND state = 'published'`,
		projectID, automationID, versionID).Scan(&adapterKey)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if adapterKey != "custom" {
		return false, nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT target.id, target.project_id, target.automation_id, target.version_id,
		target.node_key, target.name, target.node_type, target.role, target.config_json, target.position_x, target.position_y,
		target.created_at, target.updated_at, resource.resource_id
		FROM automation_edges edge
		JOIN automation_nodes source ON source.id = edge.source_node_id AND source.project_id = edge.project_id
			AND source.automation_id = edge.automation_id AND source.version_id = edge.version_id
			AND (source.node_type = 'trigger' OR source.node_type = 'agent_task' AND source.role = 'task')
		JOIN automation_nodes target ON target.id = edge.target_node_id AND target.project_id = edge.project_id
			AND target.automation_id = edge.automation_id AND target.version_id = edge.version_id
			AND target.node_type = 'agent_task' AND target.role IN ('task', 'github_inbox')
		JOIN automation_definition_resources resource ON resource.project_id = edge.project_id
			AND resource.automation_id = edge.automation_id AND resource.version_id = edge.version_id AND resource.node_id = target.id
			AND resource.resource_type = 'task'
		WHERE edge.project_id = ? AND edge.automation_id = ? AND edge.version_id = ? AND edge.source_node_id = ?
		ORDER BY edge.display_order, edge.id LIMIT 100`, projectID, automationID, versionID, sourceNodeID)
	if err != nil {
		return true, nil, err
	}
	defer rows.Close()
	handoffs := make([]CustomAutomationTaskHandoff, 0)
	for rows.Next() {
		var handoff CustomAutomationTaskHandoff
		node := &handoff.Node
		if err := rows.Scan(&node.ID, &node.ProjectID, &node.AutomationID, &node.VersionID, &node.NodeKey, &node.Name,
			&node.NodeType, &node.Role, &node.ConfigJSON, &node.PositionX, &node.PositionY, &node.CreatedAt, &node.UpdatedAt,
			&handoff.TaskID); err != nil {
			return true, nil, err
		}
		handoffs = append(handoffs, handoff)
	}
	if err := rows.Err(); err != nil {
		return true, nil, err
	}
	return true, handoffs, nil
}

func (r *AutomationRepo) GetCustomTaskHandoff(ctx context.Context, projectID, automationID, versionID, sourceNodeID string) (bool, *models.AutomationNode, string, error) {
	custom, handoffs, err := r.ListCustomTaskHandoffs(ctx, projectID, automationID, versionID, sourceNodeID)
	if err != nil || !custom || len(handoffs) == 0 {
		return custom, nil, "", err
	}
	return true, &handoffs[0].Node, handoffs[0].TaskID, nil
}

func (r *AutomationRepo) GetCustomNotificationHandoff(ctx context.Context, projectID, automationID, versionID, sourceNodeID string) (*models.AutomationNode, error) {
	var node models.AutomationNode
	err := r.db.QueryRowContext(ctx, `SELECT target.id, target.project_id, target.automation_id, target.version_id,
		target.node_key, target.name, target.node_type, target.role, target.config_json, target.position_x, target.position_y,
		target.created_at, target.updated_at
		FROM automation_edges edge
		JOIN automation_nodes target ON target.id = edge.target_node_id AND target.project_id = edge.project_id
			AND target.automation_id = edge.automation_id AND target.version_id = edge.version_id
		WHERE edge.project_id = ? AND edge.automation_id = ? AND edge.version_id = ? AND edge.source_node_id = ?
			AND target.node_type = 'action' AND target.role = 'create_notification'
		ORDER BY edge.display_order, edge.id LIMIT 1`, projectID, automationID, versionID, sourceNodeID).
		Scan(&node.ID, &node.ProjectID, &node.AutomationID, &node.VersionID, &node.NodeKey, &node.Name, &node.NodeType,
			&node.Role, &node.ConfigJSON, &node.PositionX, &node.PositionY, &node.CreatedAt, &node.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &node, err
}

func (r *AutomationRepo) AcquireGitHubIssueDedupLease(ctx context.Context, projectID, repositoryFullName, titleFingerprint, ownerToken string, source AutomationGitHubIssueDedupSource, now time.Time, leaseDuration time.Duration) (AutomationGitHubIssueDedupClaim, error) {
	projectID = strings.TrimSpace(projectID)
	repositoryFullName = strings.ToLower(strings.TrimSpace(repositoryFullName))
	titleFingerprint = strings.TrimSpace(titleFingerprint)
	ownerToken = strings.TrimSpace(ownerToken)
	if projectID == "" || repositoryFullName == "" || titleFingerprint == "" || ownerToken == "" || leaseDuration <= 0 {
		return AutomationGitHubIssueDedupClaim{}, errors.New("complete GitHub issue duplicate lease identity is required")
	}
	if source.Context.ProjectID != projectID || len(source.Context.Bindings) == 0 || strings.TrimSpace(source.TaskID) == "" || strings.TrimSpace(source.ExecutionID) == "" {
		return AutomationGitHubIssueDedupClaim{}, errors.New("complete GitHub issue projection source is required")
	}
	for _, binding := range source.Context.Bindings {
		if strings.TrimSpace(binding.AutomationID) == "" || strings.TrimSpace(binding.VersionID) == "" || strings.TrimSpace(binding.InvocationID) == "" || strings.TrimSpace(binding.NodeID) == "" {
			return AutomationGitHubIssueDedupClaim{}, errors.New("complete GitHub issue projection binding is required")
		}
	}
	projectionSourceJSON, err := json.Marshal(source)
	if err != nil {
		return AutomationGitHubIssueDedupClaim{}, fmt.Errorf("encoding GitHub issue projection source: %w", err)
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return AutomationGitHubIssueDedupClaim{}, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return AutomationGitHubIssueDedupClaim{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	expiresAt := now.UTC().Add(leaseDuration)
	result, err := conn.ExecContext(ctx, `INSERT INTO automation_github_issue_dedup_leases
		(project_id, repository_full_name, title_fingerprint, owner_token, lease_expires_at, mutation_state, projection_source_json)
		VALUES (?, ?, ?, ?, ?, 'reserved', ?) ON CONFLICT(project_id, repository_full_name, title_fingerprint) DO NOTHING`,
		projectID, repositoryFullName, titleFingerprint, ownerToken, expiresAt, string(projectionSourceJSON))
	if err != nil {
		return AutomationGitHubIssueDedupClaim{}, fmt.Errorf("acquiring GitHub issue duplicate lease: %w", err)
	}
	acquired, err := result.RowsAffected()
	if err != nil {
		return AutomationGitHubIssueDedupClaim{}, err
	}
	claim := AutomationGitHubIssueDedupClaim{OwnerToken: ownerToken, Source: source}
	if acquired == 0 {
		var createdIssueNumber sql.NullInt64
		var mutationState, existingSourceJSON string
		var leaseExpiresAt time.Time
		if err := conn.QueryRowContext(ctx, `SELECT created_issue_number, mutation_state, lease_expires_at, owner_token, projection_source_json
			FROM automation_github_issue_dedup_leases
			WHERE project_id = ? AND repository_full_name = ? AND title_fingerprint = ?`,
			projectID, repositoryFullName, titleFingerprint).Scan(&createdIssueNumber, &mutationState, &leaseExpiresAt, &claim.OwnerToken, &existingSourceJSON); err != nil {
			return AutomationGitHubIssueDedupClaim{}, err
		}
		if strings.TrimSpace(existingSourceJSON) != "" {
			if err := json.Unmarshal([]byte(existingSourceJSON), &claim.Source); err != nil {
				return AutomationGitHubIssueDedupClaim{}, fmt.Errorf("decoding GitHub issue projection source: %w", err)
			}
		} else {
			claim.Source = AutomationGitHubIssueDedupSource{}
		}
		if createdIssueNumber.Valid && createdIssueNumber.Int64 > 0 {
			if !sameAutomationGitHubIssueDedupSource(claim.Source, source) {
				return AutomationGitHubIssueDedupClaim{}, fmt.Errorf("%w: completed GitHub issue belongs to a different Automation source", ErrAutomationExternalReconciliation)
			}
			if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
				return AutomationGitHubIssueDedupClaim{}, err
			}
			committed = true
			claim.IssueNumber = int(createdIssueNumber.Int64)
			return claim, nil
		}
		if mutationState != "reserved" {
			if mutationState == "dispatched" && leaseExpiresAt.After(now.UTC()) {
				return AutomationGitHubIssueDedupClaim{}, ErrAutomationGitHubIssueDedupBusy
			}
			return AutomationGitHubIssueDedupClaim{}, fmt.Errorf("%w: prior GitHub issue creation outcome is uncertain", ErrAutomationExternalReconciliation)
		}
		result, err = conn.ExecContext(ctx, `UPDATE automation_github_issue_dedup_leases
			SET owner_token = ?, lease_expires_at = ?, projection_source_json = ?, updated_at = CURRENT_TIMESTAMP
			WHERE project_id = ? AND repository_full_name = ? AND title_fingerprint = ?
				AND mutation_state = 'reserved' AND created_issue_number IS NULL AND lease_expires_at <= ?`,
			ownerToken, expiresAt, string(projectionSourceJSON), projectID, repositoryFullName, titleFingerprint, now.UTC())
		if err != nil {
			return AutomationGitHubIssueDedupClaim{}, fmt.Errorf("reclaiming GitHub issue duplicate lease: %w", err)
		}
		acquired, err = result.RowsAffected()
		if err != nil {
			return AutomationGitHubIssueDedupClaim{}, err
		}
		if acquired == 1 {
			claim = AutomationGitHubIssueDedupClaim{OwnerToken: ownerToken, Source: source}
		}
	}
	if acquired != 1 {
		return AutomationGitHubIssueDedupClaim{}, ErrAutomationGitHubIssueDedupBusy
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return AutomationGitHubIssueDedupClaim{}, err
	}
	committed = true
	return claim, nil
}

func (r *AutomationRepo) MarkGitHubIssueDedupDispatched(ctx context.Context, projectID, repositoryFullName, titleFingerprint, ownerToken string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE automation_github_issue_dedup_leases
		SET mutation_state = 'dispatched', updated_at = CURRENT_TIMESTAMP
		WHERE project_id = ? AND repository_full_name = ? AND title_fingerprint = ? AND owner_token = ?
			AND mutation_state = 'reserved' AND created_issue_number IS NULL`,
		strings.TrimSpace(projectID), strings.ToLower(strings.TrimSpace(repositoryFullName)), strings.TrimSpace(titleFingerprint), strings.TrimSpace(ownerToken))
	if err != nil {
		return fmt.Errorf("marking GitHub issue duplicate lease dispatched: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return errors.New("GitHub issue duplicate lease is not reserved by this owner")
	}
	return nil
}

func (r *AutomationRepo) CompleteGitHubIssueDedupLease(ctx context.Context, projectID, repositoryFullName, titleFingerprint, ownerToken string, issueNumber int) error {
	if issueNumber <= 0 {
		return errors.New("created GitHub issue number is required")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE automation_github_issue_dedup_leases
		SET created_issue_number = ?, mutation_state = 'completed', updated_at = CURRENT_TIMESTAMP
		WHERE project_id = ? AND repository_full_name = ? AND title_fingerprint = ? AND owner_token = ?
			AND (mutation_state = 'dispatched' OR (mutation_state = 'completed' AND created_issue_number = ?))`,
		issueNumber, strings.TrimSpace(projectID), strings.ToLower(strings.TrimSpace(repositoryFullName)), strings.TrimSpace(titleFingerprint),
		strings.TrimSpace(ownerToken), issueNumber)
	if err != nil {
		return fmt.Errorf("completing GitHub issue duplicate lease: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return errors.New("GitHub issue duplicate lease is not dispatched by this owner")
	}
	return nil
}

func (r *AutomationRepo) ReleaseGitHubIssueDedupLease(ctx context.Context, projectID, repositoryFullName, titleFingerprint, ownerToken string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM automation_github_issue_dedup_leases
		WHERE project_id = ? AND repository_full_name = ? AND title_fingerprint = ? AND owner_token = ?
			AND mutation_state = 'reserved' AND created_issue_number IS NULL`,
		strings.TrimSpace(projectID), strings.ToLower(strings.TrimSpace(repositoryFullName)), strings.TrimSpace(titleFingerprint), strings.TrimSpace(ownerToken))
	if err != nil {
		return fmt.Errorf("releasing GitHub issue duplicate lease: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return errors.New("GitHub issue duplicate lease is not releasable by this owner")
	}
	return nil
}

func (r *AutomationRepo) ReserveExternalActivity(ctx context.Context, projectID string, binding models.AutomationBinding, activityKey, activityType, resourceType string) (string, error) {
	if projectID == "" || binding.AutomationID == "" || binding.VersionID == "" || binding.NodeID == "" || binding.InvocationID == "" || strings.TrimSpace(activityKey) == "" {
		return "", errors.New("complete invocation binding is required for an external mutation")
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	var activityID string
	err = conn.QueryRowContext(ctx, `INSERT INTO automation_activities
		(project_id, automation_id, version_id, node_id, invocation_id, activity_key, activity_type, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pending')
		ON CONFLICT(automation_id, version_id, activity_key) DO NOTHING RETURNING id`, projectID,
		binding.AutomationID, binding.VersionID, binding.NodeID, binding.InvocationID, strings.TrimSpace(activityKey), strings.TrimSpace(activityType)).Scan(&activityID)
	created := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("reserving automation external activity: %w", err)
	}
	if !created {
		if err := conn.QueryRowContext(ctx, `SELECT id FROM automation_activities
			WHERE project_id = ? AND automation_id = ? AND version_id = ? AND activity_key = ?`, projectID,
			binding.AutomationID, binding.VersionID, strings.TrimSpace(activityKey)).Scan(&activityID); err != nil {
			return "", err
		}
	}
	if err := syncAutomationLiveActivityState(ctx, conn, activityID); err != nil {
		return "", err
	}
	var resourceID string
	err = conn.QueryRowContext(ctx, `SELECT resource_id FROM automation_activity_resources
		WHERE activity_id = ? AND resource_type = ? ORDER BY id LIMIT 1`, activityID, resourceType).Scan(&resourceID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return "", err
	}
	committed = true
	if resourceID != "" {
		return resourceID, nil
	}
	if !created {
		automationobs.Event("automation.github.ambiguous_mutation",
			automationobs.String("project_id", projectID), automationobs.String("automation_id", binding.AutomationID),
			automationobs.String("version_id", binding.VersionID), automationobs.String("invocation_id", binding.InvocationID),
			automationobs.String("activity_id", activityID), automationobs.String("node_id", binding.NodeID),
			automationobs.String("resource_type", resourceType))
		return "", ErrAutomationExternalReconciliation
	}
	return "", nil
}

func (r *AutomationRepo) ReleaseExternalActivityReservation(ctx context.Context, projectID string, binding models.AutomationBinding, activityKey string) error {
	if projectID == "" || binding.AutomationID == "" || binding.VersionID == "" || binding.NodeID == "" || binding.InvocationID == "" || strings.TrimSpace(activityKey) == "" {
		return errors.New("complete invocation binding is required to release an external mutation reservation")
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM automation_activities
		WHERE project_id = ? AND automation_id = ? AND version_id = ? AND node_id = ? AND invocation_id = ?
		  AND activity_key = ? AND status = 'pending'
		  AND NOT EXISTS (SELECT 1 FROM automation_activity_resources WHERE activity_id = automation_activities.id)`,
		projectID, binding.AutomationID, binding.VersionID, binding.NodeID, binding.InvocationID, strings.TrimSpace(activityKey))
	if err != nil {
		return fmt.Errorf("releasing automation external activity reservation: %w", err)
	}
	return nil
}

func (r *AutomationRepo) ListAutomationPullRequests(ctx context.Context, projectID, automationID string, limit int) ([]models.TaskPullRequest, error) {
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT pr.id, pr.task_id, pr.pr_number, pr.pr_url, pr.pr_state, pr.published_head_sha,
		pr.issue_number, pr.issue_url, pr.created_at, pr.updated_at
		FROM task_pull_requests pr
		JOIN tasks t ON t.id = pr.task_id
		WHERE t.project_id = ? AND EXISTS (
			SELECT 1 FROM automation_activities a
			JOIN automation_activity_resources task_resource ON task_resource.activity_id = a.id
				AND task_resource.resource_type = 'task' AND task_resource.resource_id = pr.task_id
			JOIN automation_activity_resources pull_resource ON pull_resource.activity_id = a.id
				AND pull_resource.resource_type = 'pull_request'
			WHERE a.project_id = ? AND a.automation_id = ?
		)
		ORDER BY pr.updated_at, pr.id LIMIT ?`, projectID, projectID, automationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.TaskPullRequest
	for rows.Next() {
		var pull models.TaskPullRequest
		if err := rows.Scan(&pull.ID, &pull.TaskID, &pull.PRNumber, &pull.PRURL, &pull.PRState, &pull.PublishedHeadSHA,
			&pull.IssueNumber, &pull.IssueURL, &pull.CreatedAt, &pull.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, pull)
	}
	return result, rows.Err()
}

func (r *AutomationRepo) AutomationExternalState(ctx context.Context, projectID, automationID string, staleBefore time.Time) (models.AutomationExternalState, error) {
	var count int
	var oldest sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*), datetime(MIN(pr.updated_at))
		FROM task_pull_requests pr
		JOIN tasks t ON t.id = pr.task_id
		WHERE t.project_id = ? AND EXISTS (
			SELECT 1 FROM automation_activities a
			JOIN automation_activity_resources task_resource ON task_resource.activity_id = a.id
				AND task_resource.resource_type = 'task' AND task_resource.resource_id = pr.task_id
			JOIN automation_activity_resources pull_resource ON pull_resource.activity_id = a.id
				AND pull_resource.resource_type = 'pull_request'
			WHERE a.project_id = ? AND a.automation_id = ?
		)`, projectID, projectID, automationID).Scan(&count, &oldest)
	if err != nil {
		return models.AutomationExternalState{}, err
	}
	state := models.AutomationExternalState{TrackedResources: count}
	if oldest.Valid {
		updated := parseSQLiteTime(oldest.String)
		if updated.IsZero() {
			return models.AutomationExternalState{}, fmt.Errorf("invalid Automation external update time")
		}
		state.LastUpdatedAt = &updated
		state.Stale = updated.Before(staleBefore.UTC())
	}
	return state, nil
}

// ListAutomationsWithStaleExternalPullRequests returns (project_id, automation_id) pairs
// that track at least one GitHub pull request resource whose stored state has not been
// refreshed since staleBefore, so a background job can proactively refresh their state
// without requiring a manual "Refresh GitHub state" click.
func (r *AutomationRepo) ListAutomationsWithStaleExternalPullRequests(ctx context.Context, staleBefore time.Time, limit int) ([][2]string, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx, listAutomationsWithStaleExternalPullRequestsSQL, staleBefore.UTC().Format("2006-01-02 15:04:05"), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result [][2]string
	for rows.Next() {
		var value [2]string
		if err := rows.Scan(&value[0], &value[1]); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (r *AutomationRepo) BindingsForActivityResource(ctx context.Context, projectID, automationID, resourceType, resourceID string) (models.AutomationContext, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT a.automation_id, a.version_id, COALESCE(a.invocation_id, ''),
		a.node_id, COALESCE(a.work_item_id, '')
		FROM automation_activities a
		JOIN automation_activity_resources ar ON ar.activity_id = a.id
		WHERE a.project_id = ? AND a.automation_id = ? AND ar.resource_type = ? AND ar.resource_id = ?
			AND a.work_item_id IS NOT NULL
		ORDER BY a.version_id, a.node_id, a.work_item_id`, projectID, automationID, resourceType, resourceID)
	if err != nil {
		return models.AutomationContext{}, err
	}
	defer rows.Close()
	result := models.AutomationContext{ProjectID: projectID}
	for rows.Next() {
		var binding models.AutomationBinding
		if err := rows.Scan(&binding.AutomationID, &binding.VersionID, &binding.InvocationID, &binding.NodeID, &binding.WorkItemID); err != nil {
			return models.AutomationContext{}, err
		}
		result.Bindings = append(result.Bindings, binding)
	}
	return result, rows.Err()
}

func (r *AutomationRepo) FindActivityResource(ctx context.Context, projectID string, binding models.AutomationBinding, activityKey, resourceType string) (string, error) {
	var resourceID string
	err := r.db.QueryRowContext(ctx, `SELECT ar.resource_id FROM automation_activities a
		JOIN automation_activity_resources ar ON ar.activity_id = a.id
		WHERE a.project_id = ? AND a.automation_id = ? AND a.version_id = ? AND a.activity_key = ? AND ar.resource_type = ?
		ORDER BY ar.id LIMIT 1`, projectID, binding.AutomationID, binding.VersionID, activityKey, resourceType).Scan(&resourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return resourceID, err
}

func (r *AutomationRepo) BindingsForWorkItemKey(ctx context.Context, projectID, workItemKey string) (models.AutomationContext, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT wi.automation_id, wi.origin_version_id, COALESCE(wi.origin_invocation_id, ''),
		COALESCE((SELECT p.node_id FROM automation_work_item_positions p WHERE p.work_item_id = wi.id ORDER BY p.entered_at, p.node_id LIMIT 1),
		         (SELECT n.id FROM automation_nodes n WHERE n.automation_id = wi.automation_id AND n.version_id = wi.origin_version_id ORDER BY n.created_at, n.id LIMIT 1)),
		wi.id FROM automation_work_items wi WHERE wi.project_id = ? AND wi.work_item_key = ?
		ORDER BY wi.automation_id, wi.id`, projectID, workItemKey)
	if err != nil {
		return models.AutomationContext{}, err
	}
	defer rows.Close()
	result := models.AutomationContext{ProjectID: projectID}
	for rows.Next() {
		var binding models.AutomationBinding
		if err := rows.Scan(&binding.AutomationID, &binding.VersionID, &binding.InvocationID, &binding.NodeID, &binding.WorkItemID); err != nil {
			return models.AutomationContext{}, err
		}
		result.Bindings = append(result.Bindings, binding)
	}
	return result, rows.Err()
}

func (r *AutomationRepo) BindingsForExecutionResource(ctx context.Context, projectID, executionID, resourceType, resourceID string) (models.AutomationContext, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT a.automation_id, a.version_id,
		COALESCE(a.invocation_id, ''),
		COALESCE((SELECT p.node_id FROM automation_work_item_positions p
			WHERE p.work_item_id = a.work_item_id ORDER BY p.entered_at, p.node_id LIMIT 1), a.node_id),
		COALESCE(a.work_item_id, '')
		FROM automation_activities a
		JOIN automation_activity_resources execution_resource ON execution_resource.activity_id = a.id
			AND execution_resource.resource_type = 'execution' AND execution_resource.resource_id = ?
		JOIN automation_activity_resources causal_resource ON causal_resource.activity_id = a.id
			AND causal_resource.resource_type = ? AND causal_resource.resource_id = ?
		WHERE a.project_id = ? AND a.work_item_id IS NOT NULL
		ORDER BY a.automation_id, a.version_id, a.id`, executionID, resourceType, resourceID, projectID)
	if err != nil {
		return models.AutomationContext{}, err
	}
	defer rows.Close()
	result := models.AutomationContext{ProjectID: projectID}
	for rows.Next() {
		var binding models.AutomationBinding
		if err := rows.Scan(&binding.AutomationID, &binding.VersionID, &binding.InvocationID, &binding.NodeID, &binding.WorkItemID); err != nil {
			return models.AutomationContext{}, err
		}
		result.Bindings = append(result.Bindings, binding)
	}
	return result, rows.Err()
}

type AutomationExecutionProjectionRepair struct {
	ProjectID   string
	ExecutionID string
	Status      models.ExecutionStatus
	Error       string
}

func (r *AutomationRepo) ListExecutionProjectionRepairs(ctx context.Context, limit int) ([]AutomationExecutionProjectionRepair, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT t.project_id, e.id, e.status, e.error_message
		FROM executions e JOIN tasks t ON t.id = e.task_id
		JOIN automation_activity_resources ar ON ar.resource_type = 'execution' AND ar.resource_id = e.id
		JOIN automation_activities a ON a.id = ar.activity_id
		WHERE a.activity_type IN ('task_execution','thread_input_execution')
		AND (a.status <> e.status OR (e.status IN ('completed','failed','cancelled') AND a.completed_at IS NULL)
			OR (e.status IN ('completed','failed','cancelled') AND a.work_item_id IS NOT NULL AND EXISTS (
				SELECT 1 FROM automation_work_item_positions p WHERE p.work_item_id = a.work_item_id AND p.node_id = a.node_id)))
		ORDER BY e.started_at, e.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AutomationExecutionProjectionRepair
	for rows.Next() {
		var value AutomationExecutionProjectionRepair
		if err := rows.Scan(&value.ProjectID, &value.ExecutionID, &value.Status, &value.Error); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (r *AutomationRepo) RepairExecutionProjection(ctx context.Context, repair AutomationExecutionProjectionRepair) error {
	activityStatus := models.AutomationActivityRunning
	switch repair.Status {
	case models.ExecCompleted:
		activityStatus = models.AutomationActivityCompleted
	case models.ExecFailed:
		activityStatus = models.AutomationActivityFailed
	case models.ExecCancelled:
		activityStatus = models.AutomationActivityCancelled
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return err
	}
	rows, err := conn.QueryContext(ctx, `UPDATE automation_activities SET status = ?,
		completed_at = CASE WHEN ? IN ('completed','failed','cancelled') THEN COALESCE(completed_at, CURRENT_TIMESTAMP) ELSE NULL END,
		error_message = ? WHERE activity_type IN ('task_execution','thread_input_execution')
		AND id IN (SELECT activity_id FROM automation_activity_resources
		WHERE resource_type = 'execution' AND resource_id = ?) RETURNING id`, activityStatus, activityStatus,
		strings.TrimSpace(repair.Error), repair.ExecutionID)
	if err != nil {
		_ = conn.Close()
		return err
	}
	if err := syncAutomationLiveActivityStateRows(ctx, conn, rows); err != nil {
		_ = conn.Close()
		return err
	}
	if err := conn.Close(); err != nil {
		return err
	}
	return r.FinalizeExecutionProjection(ctx, repair.ProjectID, repair.ExecutionID, repair.Status)
}

func (r *AutomationRepo) ListRecoverablePreparedDispatches(ctx context.Context, limit int) ([]models.AutomationDispatch, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT d.id, d.invocation_id, d.task_id, COALESCE(d.execution_id, ''),
		d.status, d.attempts, d.claimed_by, d.claim_expires_at, d.next_attempt_at, d.last_error, d.created_at, d.updated_at
		FROM automation_dispatch_outbox d
		LEFT JOIN executions e ON e.id = d.execution_id AND e.dispatch_id = d.id
		JOIN tasks t ON t.id = d.task_id
		JOIN automation_task_run_reservations r ON r.dispatch_id = d.id AND r.task_id = d.task_id
		WHERE d.status = 'submitted' AND (
			(d.execution_id IS NULL AND t.status = 'pending') OR
			(d.execution_id IS NOT NULL AND e.status = 'running' AND t.status = 'running'))
		ORDER BY d.updated_at, d.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.AutomationDispatch
	for rows.Next() {
		var value models.AutomationDispatch
		if err := rows.Scan(&value.ID, &value.InvocationID, &value.TaskID, &value.ExecutionID, &value.Status,
			&value.Attempts, &value.ClaimedBy, &value.ClaimExpiresAt, &value.NextAttemptAt, &value.LastError,
			&value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (r *AutomationRepo) ListTerminalUnfinalizedDispatches(ctx context.Context, limit int) ([]models.AutomationDispatch, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT d.id, d.invocation_id, d.task_id, COALESCE(d.execution_id, ''),
		d.status, d.attempts, d.claimed_by, d.claim_expires_at, d.next_attempt_at, d.last_error, d.created_at, d.updated_at
		FROM automation_dispatch_outbox d JOIN executions e ON e.id = d.execution_id AND e.dispatch_id = d.id
		WHERE d.status IN ('processing','submitted') AND e.status IN ('completed','failed','cancelled')
		ORDER BY d.updated_at, d.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.AutomationDispatch
	for rows.Next() {
		var value models.AutomationDispatch
		if err := rows.Scan(&value.ID, &value.InvocationID, &value.TaskID, &value.ExecutionID, &value.Status,
			&value.Attempts, &value.ClaimedBy, &value.ClaimExpiresAt, &value.NextAttemptAt, &value.LastError,
			&value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (r *AutomationRepo) ListNodeRuntimeResources(ctx context.Context, projectID, automationID, versionID, nodeID string, limit int, cursorValue string) (models.AutomationNodeResourcePage, error) {
	limit = automationPageLimit(limit)
	kind := automationCursorKind("node_resources", automationID, versionID, nodeID)
	cursor, err := decodeAutomationCursor(kind, cursorValue)
	if err != nil {
		return models.AutomationNodeResourcePage{}, err
	}
	query := `SELECT a.node_id, a.id, ar.resource_type, ar.resource_id, ar.relation,
		CASE ar.resource_type
		 WHEN 'task' THEN COALESCE((SELECT title FROM tasks WHERE id = ar.resource_id AND project_id = a.project_id), '')
		 WHEN 'execution' THEN 'Execution'
		 WHEN 'alert' THEN COALESCE((SELECT title FROM alerts WHERE id = ar.resource_id AND project_id = a.project_id), '')
		 ELSE ar.resource_id END,
		a.status, a.started_at, ar.id
		FROM automation_activities a JOIN automation_activity_resources ar ON ar.activity_id = a.id
		WHERE a.project_id = ? AND a.automation_id = ? AND a.version_id = ? AND a.node_id = ?`
	args := []any{projectID, automationID, versionID, nodeID}
	if cursor != nil {
		parts := strings.Split(cursor.ID, ".")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return models.AutomationNodeResourcePage{}, ErrAutomationCursor
		}
		query += ` AND (datetime(a.started_at) < datetime(?) OR (datetime(a.started_at) = datetime(?) AND
			(a.id < ? OR (a.id = ? AND ar.id < ?))))`
		args = append(args, automationCursorSQLTime(cursor.Time), automationCursorSQLTime(cursor.Time), parts[0], parts[0], parts[1])
	}
	query += ` ORDER BY a.started_at DESC, a.id DESC, ar.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return models.AutomationNodeResourcePage{}, err
	}
	defer rows.Close()
	page := models.AutomationNodeResourcePage{}
	linkIDs := make([]string, 0, limit+1)
	for rows.Next() {
		var value models.AutomationNodeResource
		var linkID string
		if err := rows.Scan(&value.NodeID, &value.ActivityID, &value.ResourceType, &value.ResourceID,
			&value.Relation, &value.Name, &value.Status, &value.UpdatedAt, &linkID); err != nil {
			return models.AutomationNodeResourcePage{}, err
		}
		value.URL = automationRuntimeResourceURL(projectID, value.ResourceType, value.ResourceID)
		page.Items = append(page.Items, value)
		linkIDs = append(linkIDs, linkID)
	}
	if err := rows.Err(); err != nil {
		return models.AutomationNodeResourcePage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = encodeAutomationCursor(kind, page.Items[limit-1].UpdatedAt, page.Items[limit-1].ActivityID+"."+linkIDs[limit-1])
	}
	return page, nil
}

func automationRuntimeResourceURL(projectID, resourceType, resourceID string) string {
	switch resourceType {
	case "task":
		return "/tasks/" + url.PathEscape(resourceID)
	case "execution":
		return "/executions/" + url.PathEscape(resourceID)
	case "alert":
		return "/alerts?project_id=" + url.QueryEscape(projectID) + "&alert_id=" + url.QueryEscape(resourceID)
	case "goal":
		return "/tasks/" + url.PathEscape(resourceID) + "?project_id=" + url.QueryEscape(projectID) + "#task-goal-panel"
	case "workflow_execution":
		return "/workflows/executions/" + url.PathEscape(resourceID)
	case "github_issue", "pull_request", "review":
		parts := strings.Split(resourceID, ":")
		repositoryParts := []string(nil)
		if len(parts) >= 4 && parts[0] == "github" {
			repositoryParts = strings.Split(parts[1], "/")
		}
		if len(repositoryParts) == 2 {
			base := "https://github.com/" + url.PathEscape(repositoryParts[0]) + "/" + url.PathEscape(repositoryParts[1])
			switch resourceType {
			case "github_issue":
				return base + "/issues/" + url.PathEscape(parts[3])
			case "pull_request":
				return base + "/pull/" + url.PathEscape(parts[3])
			case "review":
				if len(parts) == 5 {
					return base + "/pull/" + url.PathEscape(parts[3]) + "#pullrequestreview-" + url.PathEscape(parts[4])
				}
			}
		}
	}
	return ""
}

func (r *AutomationRepo) BindThreadInput(ctx context.Context, inputID string, automationContext models.AutomationContext, bindingKey string) error {
	if inputID == "" || automationContext.ProjectID == "" || strings.TrimSpace(bindingKey) == "" || len(automationContext.Bindings) == 0 {
		return errors.New("thread input and automation bindings are required")
	}
	return NewThreadInputRepo(r.db).WithImmediateTx(ctx, func(exec SQLExecutor) error {
		return bindAutomationThreadInputWithExecutor(ctx, exec, inputID, automationContext, strings.TrimSpace(bindingKey))
	})
}

func (r *AutomationRepo) ContextForTask(ctx context.Context, projectID, taskID string) (models.AutomationContext, error) {
	return contextForTaskWithExecutor(ctx, r.db, projectID, taskID)
}

func recordGitHubIssueTaskProvenanceWithExecutor(ctx context.Context, exec SQLExecutor, projectID, automationID, taskID, issueResourceID, versionID, nodeID string) error {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(automationID) == "" || strings.TrimSpace(taskID) == "" || strings.TrimSpace(issueResourceID) == "" || strings.TrimSpace(versionID) == "" || strings.TrimSpace(nodeID) == "" {
		return errors.New("complete Automation GitHub issue task provenance is required")
	}
	var nodeKey string
	if err := exec.QueryRowContext(ctx, `SELECT node_key FROM automation_nodes
		WHERE project_id = ? AND automation_id = ? AND version_id = ? AND id = ? AND node_type = 'agent_task' AND role IN ('task','implementation')`,
		projectID, automationID, versionID, nodeID).Scan(&nodeKey); err != nil {
		return fmt.Errorf("loading Automation GitHub implementation node provenance: %w", err)
	}
	if strings.TrimSpace(nodeKey) == "" {
		return errors.New("Automation GitHub implementation node provenance is unavailable")
	}
	if _, err := exec.ExecContext(ctx, `INSERT INTO automation_github_issue_task_provenance
		(project_id, automation_id, task_id, issue_resource_id, implementation_node_key, created_from_version_id, created_from_node_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, task_id) DO NOTHING`, projectID, automationID, taskID, issueResourceID, nodeKey, versionID, nodeID); err != nil {
		return fmt.Errorf("recording Automation GitHub issue task provenance: %w", err)
	}
	var existingAutomationID, existingIssueResourceID, existingNodeKey string
	if err := exec.QueryRowContext(ctx, `SELECT automation_id, issue_resource_id, implementation_node_key
		FROM automation_github_issue_task_provenance WHERE project_id = ? AND task_id = ?`, projectID, taskID).
		Scan(&existingAutomationID, &existingIssueResourceID, &existingNodeKey); err != nil {
		return fmt.Errorf("loading recorded Automation GitHub issue task provenance: %w", err)
	}
	if existingAutomationID != automationID || existingIssueResourceID != issueResourceID || existingNodeKey != nodeKey {
		return errors.New("source GitHub issue has conflicting Automation task provenance")
	}
	return nil
}

// GitHubIssueTaskProvenance returns the graph-independent source issue record
// written when a GitHub inbox created the implementation task. It intentionally
// does not depend on replaceable graph-version rows, so legitimate tasks can
// publish PRs after compatible Automation edits while spoofed created_via values
// still fail closed.
func (r *AutomationRepo) GitHubIssueTaskProvenance(ctx context.Context, projectID, taskID string) (*AutomationGitHubIssueTaskProvenance, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT project_id, automation_id, task_id, issue_resource_id, implementation_node_key
		FROM automation_github_issue_task_provenance WHERE project_id = ? AND task_id = ?
		UNION
		SELECT activity.project_id, activity.automation_id, task_resource.resource_id, issue_resource.resource_id, node.node_key
		FROM automation_activities activity
		JOIN automation_nodes node ON node.id = activity.node_id AND node.version_id = activity.version_id
			AND node.automation_id = activity.automation_id AND node.project_id = activity.project_id
		JOIN automation_activity_resources task_resource ON task_resource.activity_id = activity.id
			AND task_resource.resource_type = 'task' AND task_resource.resource_id = ? AND task_resource.relation = 'child'
		JOIN automation_activity_resources issue_resource ON issue_resource.activity_id = activity.id
			AND issue_resource.resource_type = 'github_issue'
		WHERE activity.project_id = ? AND activity.activity_type = 'create_task' AND activity.work_item_id IS NOT NULL
			AND activity.activity_key = 'work-item:' || activity.work_item_id || ':implementation-task'
			AND node.node_type = 'agent_task' AND node.role IN ('task','implementation')
		ORDER BY automation_id, issue_resource_id, implementation_node_key`, projectID, taskID, taskID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out *AutomationGitHubIssueTaskProvenance
	for rows.Next() {
		var candidate AutomationGitHubIssueTaskProvenance
		if err := rows.Scan(&candidate.ProjectID, &candidate.AutomationID, &candidate.TaskID, &candidate.IssueResourceID, &candidate.ImplementationNodeKey); err != nil {
			return nil, err
		}
		if out != nil && (out.AutomationID != candidate.AutomationID || out.IssueResourceID != candidate.IssueResourceID || out.ImplementationNodeKey != candidate.ImplementationNodeKey) {
			return nil, errors.New("Automation task has conflicting GitHub source issue provenance")
		}
		out = &candidate
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GitHubIssueResourceForTask returns the canonical GitHub issue resource recorded
// when an Automation GitHub inbox created the implementation task. More than one
// source issue is ambiguous and therefore rejected.
func (r *AutomationRepo) GitHubIssueResourceForTask(ctx context.Context, projectID, taskID string) (string, error) {
	provenance, err := r.GitHubIssueTaskProvenance(ctx, projectID, taskID)
	if err != nil {
		return "", err
	}
	if provenance != nil {
		return provenance.IssueResourceID, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT issue_resource.resource_id
		FROM automation_activities activity
		JOIN automation_activity_resources task_resource ON task_resource.activity_id = activity.id
			AND task_resource.resource_type = 'task' AND task_resource.resource_id = ?
		JOIN automation_activity_resources issue_resource ON issue_resource.activity_id = activity.id
			AND issue_resource.resource_type = 'github_issue'
		WHERE activity.project_id = ? AND activity.activity_type = 'create_task'
		ORDER BY issue_resource.resource_id`, taskID, projectID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var resourceID string
	for rows.Next() {
		var candidate string
		if err := rows.Scan(&candidate); err != nil {
			return "", err
		}
		if resourceID != "" && resourceID != candidate {
			return "", errors.New("Automation task has conflicting GitHub source issues")
		}
		resourceID = candidate
	}
	return resourceID, rows.Err()
}

func contextForTaskWithExecutor(ctx context.Context, exec SQLExecutor, projectID, taskID string) (models.AutomationContext, error) {
	rows, err := exec.QueryContext(ctx, `WITH RECURSIVE task_lineage(id, parent_task_id, depth) AS (
		SELECT id, parent_task_id, 0 FROM tasks WHERE id = ? AND project_id = ?
		UNION ALL
		SELECT t.id, t.parent_task_id, l.depth + 1 FROM tasks t JOIN task_lineage l ON t.id = l.parent_task_id
		WHERE t.project_id = ? AND l.depth < 32
	), activity_bindings AS (
		SELECT DISTINCT a.automation_id, a.version_id, COALESCE(a.invocation_id, '') AS invocation_id,
			COALESCE((SELECT p.node_id FROM automation_work_item_positions p
				WHERE p.work_item_id = a.work_item_id ORDER BY p.entered_at, p.node_id LIMIT 1), a.node_id) AS node_id,
			COALESCE(a.work_item_id, '') AS work_item_id
		FROM automation_activities a JOIN automation_activity_resources ar ON ar.activity_id = a.id
		JOIN task_lineage l ON l.id = ar.resource_id
		WHERE a.project_id = ? AND ar.resource_type = 'task'
	), definition_bindings AS (
		SELECT dr.automation_id, dr.version_id, '' AS invocation_id, dr.node_id, '' AS work_item_id
		FROM automation_definition_resources dr
		JOIN automations a ON a.id = dr.automation_id AND a.project_id = dr.project_id
			AND a.published_version_id = dr.version_id
		WHERE dr.project_id = ? AND dr.resource_type = 'task' AND dr.resource_id = ?
			AND NOT EXISTS (SELECT 1 FROM activity_bindings ab
				WHERE ab.automation_id = dr.automation_id AND ab.version_id = dr.version_id)
	)
		SELECT automation_id, version_id, invocation_id, node_id, work_item_id FROM activity_bindings
		UNION ALL
		SELECT automation_id, version_id, invocation_id, node_id, work_item_id FROM definition_bindings
		ORDER BY automation_id, version_id, node_id`, taskID, projectID, projectID, projectID, projectID, taskID)
	if err != nil {
		return models.AutomationContext{}, err
	}
	defer rows.Close()
	result := models.AutomationContext{ProjectID: projectID}
	for rows.Next() {
		var binding models.AutomationBinding
		if err := rows.Scan(&binding.AutomationID, &binding.VersionID, &binding.InvocationID, &binding.NodeID, &binding.WorkItemID); err != nil {
			return models.AutomationContext{}, err
		}
		result.Bindings = append(result.Bindings, binding)
	}
	return result, rows.Err()
}

func (r *AutomationRepo) ContextForExecution(ctx context.Context, projectID, executionID string) (models.AutomationContext, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT a.automation_id, a.version_id, COALESCE(a.invocation_id, ''),
		COALESCE((SELECT p.node_id FROM automation_work_item_positions p
			WHERE p.work_item_id = a.work_item_id ORDER BY p.entered_at, p.node_id LIMIT 1), a.node_id),
		COALESCE(a.work_item_id, '')
		FROM automation_activities a
		JOIN automation_activity_resources ar ON ar.activity_id = a.id
		WHERE a.project_id = ? AND ar.resource_type = 'execution' AND ar.resource_id = ?
		ORDER BY a.automation_id, a.version_id, a.node_id, a.id`, projectID, executionID)
	if err != nil {
		return models.AutomationContext{}, err
	}
	defer rows.Close()
	result := models.AutomationContext{ProjectID: projectID}
	for rows.Next() {
		var binding models.AutomationBinding
		if err := rows.Scan(&binding.AutomationID, &binding.VersionID, &binding.InvocationID, &binding.NodeID, &binding.WorkItemID); err != nil {
			return models.AutomationContext{}, err
		}
		result.Bindings = append(result.Bindings, binding)
	}
	return result, rows.Err()
}

func (r *AutomationRepo) ContextForThreadInput(ctx context.Context, projectID, inputID string) (models.AutomationContext, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT automation_id, version_id, COALESCE(invocation_id, ''), node_id,
		COALESCE(work_item_id, '') FROM automation_thread_input_bindings
		WHERE project_id = ? AND thread_input_id = ? ORDER BY binding_key, id`, projectID, inputID)
	if err != nil {
		return models.AutomationContext{}, err
	}
	defer rows.Close()
	result := models.AutomationContext{ProjectID: projectID}
	for rows.Next() {
		var binding models.AutomationBinding
		if err := rows.Scan(&binding.AutomationID, &binding.VersionID, &binding.InvocationID, &binding.NodeID, &binding.WorkItemID); err != nil {
			return models.AutomationContext{}, err
		}
		result.Bindings = append(result.Bindings, binding)
	}
	return result, rows.Err()
}

func scanAutomationInvocation(row *sql.Row) (*models.AutomationInvocation, error) {
	var value models.AutomationInvocation
	err := row.Scan(&value.ID, &value.ProjectID, &value.AutomationID, &value.VersionID, &value.TriggerNodeID,
		&value.TriggerResourceType, &value.TriggerResourceID, &value.OccurrenceKey, &value.ScheduledFor,
		&value.Status, &value.SkippedReason, &value.StartedAt, &value.CompletedAt, &value.CreatedAt,
		&value.UpdatedAt, &value.ErrorMessage)
	return &value, err
}

func scanAutomationDispatch(row *sql.Row) (*models.AutomationDispatch, error) {
	var value models.AutomationDispatch
	err := row.Scan(&value.ID, &value.InvocationID, &value.TaskID, &value.ExecutionID, &value.Status,
		&value.Attempts, &value.ClaimedBy, &value.ClaimExpiresAt, &value.NextAttemptAt, &value.LastError,
		&value.CreatedAt, &value.UpdatedAt)
	return &value, err
}

func scanAutomationWorkItem(row *sql.Row) (*models.AutomationWorkItem, error) {
	var value models.AutomationWorkItem
	err := row.Scan(&value.ID, &value.ProjectID, &value.AutomationID, &value.OriginVersionID,
		&value.OriginInvocationID, &value.ParentWorkItemID, &value.WorkItemKey, &value.Kind, &value.Title,
		&value.Status, &value.CreatedAt, &value.UpdatedAt, &value.CompletedAt)
	return &value, err
}

func scanAutomationActivity(row *sql.Row) (*models.AutomationActivity, error) {
	var value models.AutomationActivity
	err := row.Scan(&value.ID, &value.ProjectID, &value.AutomationID, &value.VersionID, &value.NodeID,
		&value.InvocationID, &value.WorkItemID, &value.ActivityKey, &value.ActivityType, &value.Status,
		&value.StartedAt, &value.CompletedAt, &value.ErrorMessage)
	return &value, err
}

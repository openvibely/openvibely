package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

func automationNodeIDByKey(ctx context.Context, exec SQLExecutor, projectID, automationID, versionID, nodeKey string) (string, error) {
	var nodeID string
	if err := exec.QueryRowContext(ctx, `SELECT id FROM automation_nodes
		WHERE project_id = ? AND automation_id = ? AND version_id = ? AND node_key = ?`,
		projectID, automationID, versionID, nodeKey).Scan(&nodeID); err != nil {
		return "", err
	}
	return nodeID, nil
}

func recordAlertCreatedProjection(ctx context.Context, exec SQLExecutor, alert *models.Alert, automationContext models.AutomationContext) error {
	if alert == nil || automationContext.ProjectID != alert.ProjectID {
		return fmt.Errorf("alert automation project mismatch")
	}
	for _, sourceBinding := range automationContext.Bindings {
		var adapterKey string
		if err := exec.QueryRowContext(ctx, `SELECT adapter_key FROM automation_versions
			WHERE id = ? AND automation_id = ? AND project_id = ?`, sourceBinding.VersionID,
			sourceBinding.AutomationID, alert.ProjectID).Scan(&adapterKey); err != nil {
			return err
		}
		if adapterKey != "native_sdlc" {
			continue
		}
		notificationNode, err := automationNodeIDByKey(ctx, exec, alert.ProjectID, sourceBinding.AutomationID, sourceBinding.VersionID, "notification")
		if err != nil {
			return err
		}
		approvalNode, err := automationNodeIDByKey(ctx, exec, alert.ProjectID, sourceBinding.AutomationID, sourceBinding.VersionID, "approval")
		if err != nil {
			return err
		}
		binding := sourceBinding
		binding.NodeID = notificationNode
		resources := []models.AutomationActivityResource{{ResourceType: "alert", ResourceID: alert.ID}}
		if alert.SourceTaskID != nil && *alert.SourceTaskID != "" {
			resources = append(resources, models.AutomationActivityResource{ResourceType: "task", ResourceID: *alert.SourceTaskID})
		}
		if alert.ExecutionID != nil && *alert.ExecutionID != "" {
			resources = append(resources, models.AutomationActivityResource{ResourceType: "execution", ResourceID: *alert.ExecutionID})
		}
		item, _, err := recordProjectionEventWithExecutor(ctx, exec, AutomationProjectionEvent{
			Context: automationContext, Binding: binding,
			WorkItemKey: "alert:" + alert.ID, WorkItemKind: "suggestion", WorkItemTitle: alert.Title,
			WorkItemStatus: models.AutomationWorkItemWaiting,
			ActivityKey:    "alert:" + alert.ID + ":create", ActivityType: "create_notification", ActivityStatus: models.AutomationActivityCompleted,
			Resources: resources,
			EventKey:  "alert:" + alert.ID + ":created:notification", FromNodeID: sourceBinding.NodeID,
			ToNodeID: notificationNode, Transition: models.AutomationTransitionEntered,
		})
		if err != nil {
			return err
		}
		binding.WorkItemID = item.ID
		_, _, err = recordProjectionEventWithExecutor(ctx, exec, AutomationProjectionEvent{
			Context: automationContext, Binding: binding,
			ActivityKey: "alert:" + alert.ID + ":create", ActivityType: "create_notification", ActivityStatus: models.AutomationActivityCompleted,
			Resources: resources,
			EventKey:  "alert:" + alert.ID + ":created:waiting", FromNodeID: notificationNode,
			ToNodeID: approvalNode, Transition: models.AutomationTransitionWaiting,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func recordAlertDecisionProjection(ctx context.Context, exec SQLExecutor, projectID, alertID string, state models.AlertDecisionState) error {
	rows, err := exec.QueryContext(ctx, `SELECT DISTINCT wi.automation_id, wi.origin_version_id, wi.id
		FROM automation_work_items wi
		JOIN automation_activities a ON a.work_item_id = wi.id
		JOIN automation_activity_resources ar ON ar.activity_id = a.id
		WHERE wi.project_id = ? AND ar.resource_type = 'alert' AND ar.resource_id = ?`, projectID, alertID)
	if err != nil {
		return err
	}
	type target struct{ automationID, versionID, workItemID string }
	var targets []target
	for rows.Next() {
		var value target
		if err := rows.Scan(&value.automationID, &value.versionID, &value.workItemID); err != nil {
			_ = rows.Close()
			return err
		}
		targets = append(targets, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range targets {
		approvalNode, err := automationNodeIDByKey(ctx, exec, projectID, value.automationID, value.versionID, "approval")
		if err != nil {
			return err
		}
		targetKey := "inbox"
		transition := models.AutomationTransitionEntered
		activityStatus := models.AutomationActivityCompleted
		if state == models.AlertDecisionRejected || state == models.AlertDecisionDismissed {
			targetKey = "rejected"
			transition = models.AutomationTransitionCompleted
		}
		targetNode, err := automationNodeIDByKey(ctx, exec, projectID, value.automationID, value.versionID, targetKey)
		if err != nil {
			return err
		}
		binding := models.AutomationBinding{AutomationID: value.automationID, VersionID: value.versionID, NodeID: approvalNode, WorkItemID: value.workItemID}
		_, _, err = recordProjectionEventWithExecutor(ctx, exec, AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: projectID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			ActivityKey: "alert:" + alertID + ":decision:" + string(state), ActivityType: "human_decision", ActivityStatus: activityStatus,
			Resources: []models.AutomationActivityResource{{ResourceType: "alert", ResourceID: alertID}},
			EventKey:  "alert:" + alertID + ":decision:" + string(state), FromNodeID: approvalNode, ToNodeID: targetNode, Transition: transition,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func recordAlertClaimProjection(ctx context.Context, exec SQLExecutor, projectID, alertID, claimant string) error {
	targets, err := alertAutomationWorkItems(ctx, exec, projectID, alertID)
	if err != nil {
		return err
	}
	for _, value := range targets {
		if _, err := exec.ExecContext(ctx, `UPDATE automation_activities SET status = 'cancelled',
			completed_at = CURRENT_TIMESTAMP WHERE project_id = ? AND automation_id = ? AND version_id = ?
			AND work_item_id = ? AND activity_type = 'claim_notification' AND status = 'running'`,
			projectID, value.automationID, value.versionID, value.workItemID); err != nil {
			return err
		}
		inboxNode, err := automationNodeIDByKey(ctx, exec, projectID, value.automationID, value.versionID, "inbox")
		if err != nil {
			return err
		}
		binding := models.AutomationBinding{AutomationID: value.automationID, VersionID: value.versionID, NodeID: inboxNode, WorkItemID: value.workItemID}
		if _, _, err := recordProjectionEventWithExecutor(ctx, exec, AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: projectID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			ActivityKey: "alert:" + alertID + ":claim:" + claimant, ActivityType: "claim_notification", ActivityStatus: models.AutomationActivityRunning,
			Resources: []models.AutomationActivityResource{{ResourceType: "alert", ResourceID: alertID}},
		}); err != nil {
			return err
		}
	}
	return nil
}

func recordAlertClaimReleasedProjection(ctx context.Context, exec SQLExecutor, projectID, alertID, claimant string) error {
	targets, err := alertAutomationWorkItems(ctx, exec, projectID, alertID)
	if err != nil {
		return err
	}
	for _, value := range targets {
		if _, err := exec.ExecContext(ctx, `UPDATE automation_activities SET status = 'cancelled',
			completed_at = CURRENT_TIMESTAMP WHERE project_id = ? AND automation_id = ? AND version_id = ?
			AND work_item_id = ? AND activity_type = 'claim_notification' AND activity_key = ? AND status = 'running'`,
			projectID, value.automationID, value.versionID, value.workItemID, "alert:"+alertID+":claim:"+claimant); err != nil {
			return err
		}
	}
	return nil
}

func recordAlertProcessingProjection(ctx context.Context, exec SQLExecutor, projectID, alertID string, state models.AlertProcessingState, message string) error {
	targets, err := alertAutomationWorkItems(ctx, exec, projectID, alertID)
	if err != nil {
		return err
	}
	for _, value := range targets {
		claimStatus := models.AutomationActivityCompleted
		if state == models.AlertProcessingFailed {
			claimStatus = models.AutomationActivityFailed
		}
		if _, err := exec.ExecContext(ctx, `UPDATE automation_activities SET status = ?, completed_at = CURRENT_TIMESTAMP,
			error_message = CASE WHEN ? = 'failed' THEN ? ELSE error_message END
			WHERE project_id = ? AND automation_id = ? AND version_id = ? AND work_item_id = ?
			AND activity_type = 'claim_notification' AND status = 'running'`, claimStatus, claimStatus, message,
			projectID, value.automationID, value.versionID, value.workItemID); err != nil {
			return err
		}
		fromNode := value.fromNodeID
		if fromNode == "" {
			fromNode, err = automationNodeIDByKey(ctx, exec, projectID, value.automationID, value.versionID, "implementation")
			if err != nil {
				return err
			}
		}
		binding := models.AutomationBinding{AutomationID: value.automationID, VersionID: value.versionID, NodeID: fromNode, WorkItemID: value.workItemID}
		event := AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: projectID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			ActivityKey: "alert:" + alertID + ":processing:" + string(state), ActivityType: "process_notification",
			ActivityStatus: models.AutomationActivityCompleted,
			Resources:      []models.AutomationActivityResource{{ResourceType: "alert", ResourceID: alertID}},
			MetadataJSON:   `{"message_present":` + fmt.Sprintf("%t", message != "") + `}`,
		}
		// Processing completion means the inbox finished linking/handing off the
		// implementation task. It is not implementation completion, so the work
		// item remains at the implementation node until the real task execution
		// terminalizes. A processing failure is an actual failed projection.
		if state == models.AlertProcessingFailed {
			event.ActivityStatus = models.AutomationActivityFailed
			event.EventKey = "alert:" + alertID + ":processing:" + string(state)
			event.FromNodeID = fromNode
			event.ToNodeID = fromNode
			event.Transition = models.AutomationTransitionFailed
		}
		if _, _, err := recordProjectionEventWithExecutor(ctx, exec, event); err != nil {
			return err
		}
	}
	return nil
}

type alertAutomationTarget struct{ automationID, versionID, workItemID, fromNodeID string }

func alertAutomationWorkItems(ctx context.Context, exec SQLExecutor, projectID, alertID string) ([]alertAutomationTarget, error) {
	rows, err := exec.QueryContext(ctx, `SELECT DISTINCT wi.automation_id, wi.origin_version_id, wi.id,
		COALESCE((SELECT p.node_id FROM automation_work_item_positions p WHERE p.work_item_id = wi.id ORDER BY p.entered_at, p.node_id LIMIT 1), '')
		FROM automation_work_items wi
		JOIN automation_activities a ON a.work_item_id = wi.id
		JOIN automation_activity_resources ar ON ar.activity_id = a.id
		WHERE wi.project_id = ? AND ar.resource_type = 'alert' AND ar.resource_id = ?`, projectID, alertID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []alertAutomationTarget
	for rows.Next() {
		var value alertAutomationTarget
		if err := rows.Scan(&value.automationID, &value.versionID, &value.workItemID, &value.fromNodeID); err != nil {
			return nil, err
		}
		targets = append(targets, value)
	}
	return targets, rows.Err()
}

func recordAlertImplementationProjection(ctx context.Context, exec SQLExecutor, projectID, alertID, taskID string) error {
	rows, err := exec.QueryContext(ctx, `SELECT DISTINCT wi.automation_id, wi.origin_version_id, wi.id,
		COALESCE((SELECT p.node_id FROM automation_work_item_positions p WHERE p.work_item_id = wi.id ORDER BY p.entered_at, p.node_id LIMIT 1), '')
		FROM automation_work_items wi
		JOIN automation_activities a ON a.work_item_id = wi.id
		JOIN automation_activity_resources ar ON ar.activity_id = a.id
		WHERE wi.project_id = ? AND ar.resource_type = 'alert' AND ar.resource_id = ?`, projectID, alertID)
	if err != nil {
		return err
	}
	type target struct{ automationID, versionID, workItemID, fromNodeID string }
	var targets []target
	for rows.Next() {
		var value target
		if err := rows.Scan(&value.automationID, &value.versionID, &value.workItemID, &value.fromNodeID); err != nil {
			_ = rows.Close()
			return err
		}
		targets = append(targets, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range targets {
		implementationNode, err := automationNodeIDByKey(ctx, exec, projectID, value.automationID, value.versionID, "implementation")
		if err != nil {
			return err
		}
		binding := models.AutomationBinding{AutomationID: value.automationID, VersionID: value.versionID, NodeID: implementationNode, WorkItemID: value.workItemID}
		_, _, err = recordProjectionEventWithExecutor(ctx, exec, AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: projectID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			ActivityKey: "alert:" + alertID + ":implementation-task", ActivityType: "create_implementation_task", ActivityStatus: models.AutomationActivityCompleted,
			Resources: []models.AutomationActivityResource{{ResourceType: "alert", ResourceID: alertID}, {ResourceType: "task", ResourceID: taskID}},
			EventKey:  "alert:" + alertID + ":implementation:" + taskID, FromNodeID: value.fromNodeID, ToNodeID: implementationNode, Transition: models.AutomationTransitionEntered,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func alertAutomationBindingsForTask(ctx context.Context, exec SQLExecutor, projectID, alertID, taskID string) ([]models.AutomationBinding, error) {
	rows, err := exec.QueryContext(ctx, `SELECT DISTINCT a.automation_id, a.version_id, COALESCE(a.invocation_id, ''),
		a.node_id, COALESCE(a.work_item_id, '') FROM automation_activities a
		JOIN automation_activity_resources ar_alert ON ar_alert.activity_id = a.id AND ar_alert.resource_type = 'alert' AND ar_alert.resource_id = ?
		JOIN automation_activity_resources ar_task ON ar_task.activity_id = a.id AND ar_task.resource_type = 'task' AND ar_task.resource_id = ?
		WHERE a.project_id = ? ORDER BY a.automation_id, a.version_id, a.id`, alertID, taskID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bindings []models.AutomationBinding
	for rows.Next() {
		var binding models.AutomationBinding
		if err := rows.Scan(&binding.AutomationID, &binding.VersionID, &binding.InvocationID, &binding.NodeID, &binding.WorkItemID); err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

var _ = sql.ErrNoRows

package repository

import (
	"context"
	"database/sql"
	"errors"
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

func automationWorkItemNodeIDByRole(ctx context.Context, exec SQLExecutor, projectID, automationID, versionID, workItemID, role string) (string, error) {
	var nodeID string
	err := exec.QueryRowContext(ctx, `SELECT n.id FROM automation_work_item_positions p
		JOIN automation_nodes n ON n.id = p.node_id AND n.project_id = p.project_id
			AND n.automation_id = p.automation_id AND n.version_id = p.version_id
		WHERE p.project_id = ? AND p.automation_id = ? AND p.version_id = ? AND p.work_item_id = ? AND n.role = ?
		ORDER BY p.entered_at DESC, p.node_id LIMIT 1`, projectID, automationID, versionID, workItemID, role).Scan(&nodeID)
	return nodeID, err
}

func automationTargetNodeIDByRole(ctx context.Context, exec SQLExecutor, projectID, automationID, versionID, sourceNodeID, role string) (string, error) {
	var nodeID string
	err := exec.QueryRowContext(ctx, `SELECT target.id FROM automation_edges edge
		JOIN automation_nodes target ON target.id = edge.target_node_id AND target.project_id = edge.project_id
			AND target.automation_id = edge.automation_id AND target.version_id = edge.version_id
		WHERE edge.project_id = ? AND edge.automation_id = ? AND edge.version_id = ?
			AND edge.source_node_id = ? AND target.role = ?`, projectID, automationID, versionID, sourceNodeID, role).Scan(&nodeID)
	return nodeID, err
}

func alertHandoffNodeIDs(ctx context.Context, exec SQLExecutor, projectID, automationID, versionID, sourceNodeID string) (string, string, string, bool, error) {
	var notificationNode, approvalNode, inboxNode string
	err := exec.QueryRowContext(ctx, `SELECT notification.id, approval.id, inbox.id
		FROM automation_edges source_edge
		JOIN automation_nodes notification ON notification.id = source_edge.target_node_id
			AND notification.project_id = source_edge.project_id AND notification.automation_id = source_edge.automation_id
			AND notification.version_id = source_edge.version_id
		JOIN automation_edges approval_edge ON approval_edge.source_node_id = notification.id
			AND approval_edge.project_id = source_edge.project_id AND approval_edge.automation_id = source_edge.automation_id
			AND approval_edge.version_id = source_edge.version_id
		JOIN automation_nodes approval ON approval.id = approval_edge.target_node_id
			AND approval.project_id = source_edge.project_id AND approval.automation_id = source_edge.automation_id
			AND approval.version_id = source_edge.version_id
		JOIN automation_edges inbox_edge ON inbox_edge.source_node_id = approval.id
			AND inbox_edge.project_id = source_edge.project_id AND inbox_edge.automation_id = source_edge.automation_id
			AND inbox_edge.version_id = source_edge.version_id
		JOIN automation_nodes inbox ON inbox.id = inbox_edge.target_node_id
			AND inbox.project_id = source_edge.project_id AND inbox.automation_id = source_edge.automation_id
			AND inbox.version_id = source_edge.version_id
		WHERE source_edge.project_id = ? AND source_edge.automation_id = ? AND source_edge.version_id = ?
			AND source_edge.source_node_id = ? AND notification.node_type = 'action'
			AND notification.role = 'create_notification' AND approval.node_type = 'human_gate'
			AND approval.role = 'native_approval' AND inbox.role = 'native_inbox'`,
		projectID, automationID, versionID, sourceNodeID).Scan(&notificationNode, &approvalNode, &inboxNode)
	if err == sql.ErrNoRows {
		return "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", false, err
	}
	return notificationNode, approvalNode, inboxNode, true, nil
}

func applyAlertAutomationProvenance(ctx context.Context, exec SQLExecutor, alert *models.Alert, automationContext models.AutomationContext) error {
	entries := make([]any, 0, len(automationContext.Bindings))
	for _, binding := range automationContext.Bindings {
		notificationNode, approvalNode, inboxNode, found, err := alertHandoffNodeIDs(ctx, exec, alert.ProjectID, binding.AutomationID, binding.VersionID, binding.NodeID)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		entries = append(entries, map[string]any{
			"automation_id":        binding.AutomationID,
			"version_id":           binding.VersionID,
			"producer_node_id":     binding.NodeID,
			"notification_node_id": notificationNode,
			"approval_node_id":     approvalNode,
			"inbox_node_id":        inboxNode,
		})
	}
	if len(entries) == 0 {
		return nil
	}
	if alert.Metadata == nil {
		alert.Metadata = map[string]any{}
	}
	alert.Metadata[models.AlertAutomationProvenanceMetadataKey] = entries
	return nil
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
		var notificationNode, approvalNode string
		var err error
		if adapterKey == "native_sdlc" {
			notificationNode, err = automationTargetNodeIDByRole(ctx, exec, alert.ProjectID, sourceBinding.AutomationID, sourceBinding.VersionID, sourceBinding.NodeID, "create_notification")
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("create_notification is not authorized by the caller's Automation graph")
			}
			if err != nil {
				return err
			}
			approvalNode, err = automationTargetNodeIDByRole(ctx, exec, alert.ProjectID, sourceBinding.AutomationID, sourceBinding.VersionID, notificationNode, "native_approval")
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("create_notification is not authorized by the caller's Automation graph")
			}
			if err != nil {
				return err
			}
		} else if adapterKey == "custom" {
			var found bool
			notificationNode, approvalNode, _, found, err = alertHandoffNodeIDs(ctx, exec, alert.ProjectID, sourceBinding.AutomationID, sourceBinding.VersionID, sourceBinding.NodeID)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
		} else {
			continue
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
		var adapterKey string
		if err := exec.QueryRowContext(ctx, `SELECT adapter_key FROM automation_versions
			WHERE id = ? AND automation_id = ? AND project_id = ?`, value.versionID, value.automationID, projectID).Scan(&adapterKey); err != nil {
			return err
		}
		var approvalNode, targetNode string
		transition := models.AutomationTransitionEntered
		if adapterKey == "native_sdlc" {
			var err error
			approvalNode, err = automationNodeIDByKey(ctx, exec, projectID, value.automationID, value.versionID, "approval")
			if err != nil {
				return err
			}
		} else if adapterKey == "custom" {
			if err := exec.QueryRowContext(ctx, `SELECT n.id FROM automation_work_item_positions p
				JOIN automation_nodes n ON n.id = p.node_id AND n.project_id = p.project_id
					AND n.automation_id = p.automation_id AND n.version_id = p.version_id
				WHERE p.project_id = ? AND p.automation_id = ? AND p.version_id = ? AND p.work_item_id = ?
					AND n.node_type = 'human_gate' AND n.role = 'native_approval'`, projectID, value.automationID, value.versionID, value.workItemID).Scan(&approvalNode); err != nil {
				return err
			}
		} else {
			continue
		}

		branchState := "approved"
		if state == models.AlertDecisionRejected || state == models.AlertDecisionDismissed {
			branchState = "rejected"
		}
		var targetType models.AutomationNodeType
		branchErr := exec.QueryRowContext(ctx, `SELECT target.id, target.node_type FROM automation_edges edge
				JOIN automation_nodes target ON target.id = edge.target_node_id AND target.project_id = edge.project_id
					AND target.automation_id = edge.automation_id AND target.version_id = edge.version_id
				WHERE edge.project_id = ? AND edge.automation_id = ? AND edge.version_id = ?
					AND edge.source_node_id = ? AND json_extract(edge.condition_json, '$.state') = ?`,
			projectID, value.automationID, value.versionID, approvalNode, branchState).Scan(&targetNode, &targetType)
		if errors.Is(branchErr, sql.ErrNoRows) {
			targetNode = approvalNode
			transition = models.AutomationTransitionCompleted
		} else if branchErr != nil {
			return branchErr
		} else if targetType == models.AutomationNodeOutcome {
			transition = models.AutomationTransitionCompleted
		}
		binding := models.AutomationBinding{AutomationID: value.automationID, VersionID: value.versionID, NodeID: approvalNode, WorkItemID: value.workItemID}
		_, _, err = recordProjectionEventWithExecutor(ctx, exec, AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: projectID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			ActivityKey: "alert:" + alertID + ":decision:" + string(state), ActivityType: "human_decision", ActivityStatus: models.AutomationActivityCompleted,
			Resources: []models.AutomationActivityResource{{ResourceType: "alert", ResourceID: alertID}},
			EventKey:  "alert:" + alertID + ":decision:" + string(state), FromNodeID: approvalNode, ToNodeID: targetNode, Transition: transition,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func rebindAlertAutomationProjection(ctx context.Context, exec SQLExecutor, projectID, alertID string, bindings []models.AutomationBinding) error {
	var title string
	var decision models.AlertDecisionState
	var sourceTaskID, executionID sql.NullString
	if err := exec.QueryRowContext(ctx, `SELECT title, decision_state, source_task_id, execution_id
		FROM alerts WHERE project_id = ? AND id = ?`, projectID, alertID).Scan(&title, &decision, &sourceTaskID, &executionID); err != nil {
		return err
	}
	for _, inboxBinding := range bindings {
		rows, err := exec.QueryContext(ctx, `SELECT producer.id, action.id, gate.id, inbox.id
			FROM automation_artifact_mailbox_owners owner
			JOIN automation_nodes inbox ON inbox.project_id = owner.project_id AND inbox.automation_id = owner.automation_id
				AND inbox.version_id = ? AND inbox.id = ? AND inbox.role = 'native_inbox'
			JOIN automation_edges inbox_edge ON inbox_edge.target_node_id = inbox.id
				AND inbox_edge.project_id = inbox.project_id AND inbox_edge.automation_id = inbox.automation_id AND inbox_edge.version_id = inbox.version_id
			JOIN automation_nodes gate ON gate.id = inbox_edge.source_node_id AND gate.project_id = inbox.project_id
				AND gate.automation_id = inbox.automation_id AND gate.version_id = inbox.version_id AND gate.role = 'native_approval'
			JOIN automation_edges gate_edge ON gate_edge.target_node_id = gate.id
				AND gate_edge.project_id = gate.project_id AND gate_edge.automation_id = gate.automation_id AND gate_edge.version_id = gate.version_id
			JOIN automation_nodes action ON action.id = gate_edge.source_node_id AND action.project_id = gate.project_id
				AND action.automation_id = gate.automation_id AND action.version_id = gate.version_id AND action.role = 'create_notification'
			JOIN automation_edges producer_edge ON producer_edge.target_node_id = action.id
				AND producer_edge.project_id = action.project_id AND producer_edge.automation_id = action.automation_id AND producer_edge.version_id = action.version_id
			JOIN automation_nodes producer ON producer.id = producer_edge.source_node_id AND producer.project_id = action.project_id
				AND producer.automation_id = action.automation_id AND producer.version_id = action.version_id
			WHERE owner.project_id = ? AND owner.automation_id = ? AND owner.artifact_type = 'alert' AND owner.artifact_id = ?`,
			inboxBinding.VersionID, inboxBinding.NodeID, projectID, inboxBinding.AutomationID, alertID)
		if err != nil {
			return err
		}
		type path struct{ producerID, actionID, gateID, inboxID string }
		var paths []path
		for rows.Next() {
			var value path
			if err := rows.Scan(&value.producerID, &value.actionID, &value.gateID, &value.inboxID); err != nil {
				_ = rows.Close()
				return err
			}
			paths = append(paths, value)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, value := range paths {
			binding := inboxBinding
			binding.NodeID = value.actionID
			resources := []models.AutomationActivityResource{{ResourceType: "alert", ResourceID: alertID}}
			if sourceTaskID.Valid && sourceTaskID.String != "" {
				resources = append(resources, models.AutomationActivityResource{ResourceType: "task", ResourceID: sourceTaskID.String})
			}
			if executionID.Valid && executionID.String != "" {
				resources = append(resources, models.AutomationActivityResource{ResourceType: "execution", ResourceID: executionID.String})
			}
			item, _, err := recordProjectionEventWithExecutor(ctx, exec, AutomationProjectionEvent{
				Context: models.AutomationContext{ProjectID: projectID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
				WorkItemKey: "alert:" + alertID, WorkItemKind: "suggestion", WorkItemTitle: title, WorkItemStatus: models.AutomationWorkItemWaiting,
				ActivityKey: "alert:" + alertID + ":create", ActivityType: "create_notification", ActivityStatus: models.AutomationActivityCompleted,
				Resources: resources, EventKey: "alert:" + alertID + ":created:notification", FromNodeID: value.producerID,
				ToNodeID: value.actionID, Transition: models.AutomationTransitionEntered,
			})
			if err != nil {
				return err
			}
			binding.WorkItemID = item.ID
			if _, _, err := recordProjectionEventWithExecutor(ctx, exec, AutomationProjectionEvent{
				Context: models.AutomationContext{ProjectID: projectID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
				ActivityKey: "alert:" + alertID + ":create", ActivityType: "create_notification", ActivityStatus: models.AutomationActivityCompleted,
				Resources: resources, EventKey: "alert:" + alertID + ":created:waiting", FromNodeID: value.actionID,
				ToNodeID: value.gateID, Transition: models.AutomationTransitionWaiting,
			}); err != nil {
				return err
			}
			if decision == models.AlertDecisionApproved {
				binding.NodeID = value.gateID
				if _, _, err := recordProjectionEventWithExecutor(ctx, exec, AutomationProjectionEvent{
					Context: models.AutomationContext{ProjectID: projectID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
					ActivityKey: "alert:" + alertID + ":decision:" + string(decision), ActivityType: "human_decision", ActivityStatus: models.AutomationActivityCompleted,
					Resources: resources, EventKey: "alert:" + alertID + ":decision:" + string(decision), FromNodeID: value.gateID,
					ToNodeID: value.inboxID, Transition: models.AutomationTransitionEntered,
				}); err != nil {
					return err
				}
			}
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
		rows, err := exec.QueryContext(ctx, `UPDATE automation_activities SET status = 'cancelled',
			completed_at = CURRENT_TIMESTAMP WHERE project_id = ? AND automation_id = ? AND version_id = ?
			AND work_item_id = ? AND activity_type = 'claim_notification' AND status = 'running' RETURNING id`,
			projectID, value.automationID, value.versionID, value.workItemID)
		if err != nil {
			return err
		}
		if err := syncAutomationLiveActivityStateRows(ctx, exec, rows); err != nil {
			return err
		}
		inboxNode, err := automationWorkItemNodeIDByRole(ctx, exec, projectID, value.automationID, value.versionID, value.workItemID, "native_inbox")
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
		rows, err := exec.QueryContext(ctx, `UPDATE automation_activities SET status = 'cancelled',
			completed_at = CURRENT_TIMESTAMP WHERE project_id = ? AND automation_id = ? AND version_id = ?
			AND work_item_id = ? AND activity_type = 'claim_notification' AND activity_key = ? AND status = 'running' RETURNING id`,
			projectID, value.automationID, value.versionID, value.workItemID, "alert:"+alertID+":claim:"+claimant)
		if err != nil {
			return err
		}
		if err := syncAutomationLiveActivityStateRows(ctx, exec, rows); err != nil {
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
		rows, err := exec.QueryContext(ctx, `UPDATE automation_activities SET status = ?, completed_at = CURRENT_TIMESTAMP,
			error_message = CASE WHEN ? = 'failed' THEN ? ELSE error_message END
			WHERE project_id = ? AND automation_id = ? AND version_id = ? AND work_item_id = ?
			AND activity_type = 'claim_notification' AND status = 'running' RETURNING id`, claimStatus, claimStatus, message,
			projectID, value.automationID, value.versionID, value.workItemID)
		if err != nil {
			return err
		}
		if err := syncAutomationLiveActivityStateRows(ctx, exec, rows); err != nil {
			return err
		}
		fromNode := value.fromNodeID
		if fromNode == "" {
			fromNode, err = automationWorkItemNodeIDByRole(ctx, exec, projectID, value.automationID, value.versionID, value.workItemID, "implementation")
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
	existing, err := alertAutomationBindingsForTask(ctx, exec, projectID, alertID, taskID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
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
		inboxNode := value.fromNodeID
		if inboxNode == "" {
			inboxNode, err = automationWorkItemNodeIDByRole(ctx, exec, projectID, value.automationID, value.versionID, value.workItemID, "native_inbox")
			if err != nil {
				return err
			}
		}
		implementationNode, err := automationTargetNodeIDByRole(ctx, exec, projectID, value.automationID, value.versionID, inboxNode, "implementation")
		if err != nil {
			return err
		}
		binding := models.AutomationBinding{AutomationID: value.automationID, VersionID: value.versionID, NodeID: implementationNode, WorkItemID: value.workItemID}
		_, _, err = recordProjectionEventWithExecutor(ctx, exec, AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: projectID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			ActivityKey: "alert:" + alertID + ":implementation-task", ActivityType: "create_implementation_task", ActivityStatus: models.AutomationActivityCompleted,
			Resources: []models.AutomationActivityResource{{ResourceType: "alert", ResourceID: alertID}, {ResourceType: "task", ResourceID: taskID}},
			EventKey:  "alert:" + alertID + ":implementation:" + taskID, FromNodeID: inboxNode, ToNodeID: implementationNode, Transition: models.AutomationTransitionEntered,
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

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/openvibely/openvibely/internal/automationobs"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
)

var ErrAutomationDispatchInFlight = errors.New("automation has in-flight dispatch work")

func (r *AutomationRepo) ResumeAutomation(ctx context.Context, projectID, automationID string) ([]models.Task, error) {
	conn, finishImmediate, err := beginImmediateConn(ctx, r.db)
	if err != nil {
		return nil, err
	}
	defer finishImmediate()

	var current models.AutomationLifecycleState
	var versionID sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT lifecycle_state, published_version_id FROM automations
		WHERE project_id = ? AND id = ?`, projectID, automationID).Scan(&current, &versionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("automation not found")
		}
		return nil, err
	}
	if !versionID.Valid {
		return nil, errors.New("Automation cannot change active lifecycle state before Save")
	}
	if current == models.AutomationArchived {
		return nil, errors.New("archived automation cannot be resumed")
	}
	if current == models.AutomationActive {
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return nil, err
		}
		return nil, nil
	}

	var candidateJSON string
	hasCandidate := true
	if err := conn.QueryRowContext(ctx, `SELECT candidate_json FROM automation_graph_metadata
		WHERE project_id = ? AND automation_id = ? AND version_id = ?`, projectID, automationID, versionID.String).Scan(&candidateJSON); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		hasCandidate = false
	}
	var candidate models.AutomationDraftCandidate
	if hasCandidate {
		if err := json.Unmarshal([]byte(candidateJSON), &candidate); err != nil {
			return nil, err
		}
	}
	rows, err := conn.QueryContext(ctx, `SELECT o.schedule_id, n.node_key
		FROM automation_trigger_owners o
		JOIN automation_nodes n ON n.id = o.node_id AND n.version_id = o.version_id
			AND n.automation_id = o.automation_id AND n.project_id = o.project_id
		WHERE o.project_id = ? AND o.automation_id = ? AND o.version_id = ?`, projectID, automationID, versionID.String)
	if err != nil {
		return nil, err
	}
	var ownedSchedules []struct {
		id      string
		nodeKey string
	}
	for rows.Next() {
		var schedule struct {
			id      string
			nodeKey string
		}
		if err := rows.Scan(&schedule.id, &schedule.nodeKey); err != nil {
			rows.Close()
			return nil, err
		}
		ownedSchedules = append(ownedSchedules, schedule)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, schedule := range ownedSchedules {
		if _, err := conn.ExecContext(ctx, `UPDATE schedules SET enabled = 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, schedule.id); err != nil {
			return nil, err
		}
	}

	admittedTaskIDs := []string{}
	admittedTaskSet := map[string]bool{}
	nodesByKey := make(map[string]models.AutomationDraftNode, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		nodesByKey[node.Key] = node
	}
	scheduleDerivedChildren := make(map[string]bool)
	for _, edge := range candidate.Edges {
		source, sourceOK := nodesByKey[edge.From]
		target, targetOK := nodesByKey[edge.To]
		if sourceOK && targetOK && source.Type == models.AutomationNodeTrigger && target.Type == models.AutomationNodeAgentTask &&
			(target.Role == "task" || target.Role == "github_inbox") {
			scheduleDerivedChildren[target.Key] = true
		}
	}
	for _, node := range candidate.Nodes {
		category, _ := node.Config["category"].(string)
		configuredRunnable := node.Type == models.AutomationNodeAgentTask && node.Role == "task" && category == string(models.CategoryActive)
		scheduleDerived := node.Type == models.AutomationNodeAgentTask &&
			(node.Role == "task" || node.Role == "github_inbox") && scheduleDerivedChildren[node.Key]
		if !configuredRunnable && !scheduleDerived {
			continue
		}
		allowRoot := 0
		if configuredRunnable {
			allowRoot = 1
		}
		var taskID string
		err := conn.QueryRowContext(ctx, `SELECT t.id FROM automation_definition_resources resource
			JOIN automation_nodes n ON n.id = resource.node_id AND n.version_id = resource.version_id
			JOIN tasks t ON t.id = resource.resource_id AND t.project_id = resource.project_id
			WHERE resource.project_id = ? AND resource.automation_id = ? AND resource.version_id = ?
				AND resource.resource_type = 'task' AND n.node_key = ? AND t.category = 'backlog'
				AND t.status = 'pending' AND ((? = 1 AND t.parent_task_id IS NULL) OR EXISTS (
					SELECT 1 FROM automation_transitions transition
					JOIN automation_definition_resources parent_resource ON parent_resource.project_id = transition.project_id
						AND parent_resource.automation_id = transition.automation_id AND parent_resource.version_id = transition.version_id
						AND parent_resource.node_id = transition.from_node_id AND parent_resource.resource_type = 'task'
						AND parent_resource.resource_id = t.parent_task_id
					WHERE transition.project_id = resource.project_id AND transition.automation_id = resource.automation_id
						AND transition.version_id = resource.version_id AND transition.to_node_id = n.id AND transition.state = 'entered'
				))`, projectID, automationID, versionID.String, node.Key, allowRoot).Scan(&taskID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !admittedTaskSet[taskID] {
			admittedTaskSet[taskID] = true
			admittedTaskIDs = append(admittedTaskIDs, taskID)
		}
	}

	activityRows, err := conn.QueryContext(ctx, `SELECT admission.task_id
		FROM automation_paused_task_admissions admission
		JOIN tasks task ON task.id = admission.task_id AND task.project_id = admission.project_id
		WHERE admission.project_id = ? AND admission.automation_id = ? AND admission.version_id = ?
			AND task.category = 'backlog' AND task.status = 'pending'
			AND EXISTS (
				SELECT 1 FROM automation_activity_resources resource
				JOIN automation_activities activity ON activity.id = resource.activity_id
				WHERE resource.resource_type = 'task' AND resource.resource_id = admission.task_id AND resource.relation = 'child'
					AND activity.project_id = admission.project_id AND activity.automation_id = admission.automation_id
					AND activity.version_id = admission.version_id AND activity.activity_type = 'create_task')
		ORDER BY admission.created_at, admission.task_id`, projectID, automationID, versionID.String)
	if err != nil {
		return nil, err
	}
	var activityTaskIDs []string
	for activityRows.Next() {
		var taskID string
		if err := activityRows.Scan(&taskID); err != nil {
			activityRows.Close()
			return nil, err
		}
		activityTaskIDs = append(activityTaskIDs, taskID)
	}
	if err := activityRows.Close(); err != nil {
		return nil, err
	}
	for _, taskID := range activityTaskIDs {
		if !admittedTaskSet[taskID] {
			admittedTaskSet[taskID] = true
			admittedTaskIDs = append(admittedTaskIDs, taskID)
		}
	}
	if len(admittedTaskIDs) > 0 {
		args := make([]any, 0, len(admittedTaskIDs)+1)
		args = append(args, projectID)
		placeholders := make([]string, 0, len(admittedTaskIDs))
		for _, taskID := range admittedTaskIDs {
			placeholders = append(placeholders, "?")
			args = append(args, taskID)
		}
		orderedRows, err := conn.QueryContext(ctx, `SELECT id FROM tasks
			WHERE project_id = ? AND category = 'backlog' AND status = 'pending'
			  AND id IN (`+strings.Join(placeholders, ",")+`)
			ORDER BY display_order ASC, created_at ASC, id ASC`, args...)
		if err != nil {
			return nil, err
		}
		orderedTaskIDs := make([]string, 0, len(admittedTaskIDs))
		for orderedRows.Next() {
			var taskID string
			if err := orderedRows.Scan(&taskID); err != nil {
				orderedRows.Close()
				return nil, err
			}
			orderedTaskIDs = append(orderedTaskIDs, taskID)
		}
		if err := orderedRows.Err(); err != nil {
			orderedRows.Close()
			return nil, err
		}
		if err := orderedRows.Close(); err != nil {
			return nil, err
		}

		var nextOrder int
		if err := conn.QueryRowContext(ctx, activeBoardTailOrderQuery, projectID).Scan(&nextOrder); err != nil {
			return nil, err
		}
		admittedTaskIDs = admittedTaskIDs[:0]
		for _, taskID := range orderedTaskIDs {
			result, err := conn.ExecContext(ctx, `UPDATE tasks
				SET category = 'active', display_order = ?, completed_at = NULL, updated_at = CURRENT_TIMESTAMP
				WHERE id = ? AND project_id = ? AND category = 'backlog' AND status = 'pending'`, nextOrder, taskID, projectID)
			if err != nil {
				return nil, err
			}
			if affected, _ := result.RowsAffected(); affected == 1 {
				admittedTaskIDs = append(admittedTaskIDs, taskID)
				nextOrder++
			}
		}
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM automation_paused_task_admissions
		WHERE project_id = ? AND automation_id = ?`, projectID, automationID); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE automation_trigger_owners SET ownership_state = 'active', updated_at = CURRENT_TIMESTAMP
		WHERE project_id = ? AND automation_id = ?`, projectID, automationID); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE automations SET lifecycle_state = 'active', archived_at = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE project_id = ? AND id = ?`, projectID, automationID); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, err
	}
	finishImmediate()

	admittedTasks := make([]models.Task, 0, len(admittedTaskIDs))
	taskRepo := NewTaskRepo(r.db, nil)
	for _, taskID := range admittedTaskIDs {
		task, err := taskRepo.GetByID(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if task != nil {
			admittedTasks = append(admittedTasks, *task)
		}
	}
	automationobs.Event("automation.lifecycle.resumed",
		automationobs.String("project_id", projectID), automationobs.String("automation_id", automationID),
		automationobs.String("version_id", versionID.String), automationobs.String("state", string(models.AutomationActive)))
	r.PublishInvalidation(events.AutomationDefinitionUpdated, projectID, models.AutomationBinding{AutomationID: automationID, VersionID: versionID.String})
	return admittedTasks, nil
}

func (r *AutomationRepo) SetAutomationLifecycle(ctx context.Context, projectID, automationID string, state models.AutomationLifecycleState) error {
	if state == models.AutomationActive {
		_, err := r.ResumeAutomation(ctx, projectID, automationID)
		return err
	}
	if state != models.AutomationPaused && state != models.AutomationArchived {
		return errors.New("unsupported automation lifecycle state")
	}
	conn, finishImmediate, err := beginImmediateConn(ctx, r.db)
	if err != nil {
		return err
	}
	defer finishImmediate()
	var current models.AutomationLifecycleState
	var published sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT lifecycle_state, published_version_id FROM automations WHERE project_id = ? AND id = ?`, projectID, automationID).Scan(&current, &published); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("automation not found")
		}
		return err
	}
	if !published.Valid {
		return errors.New("Automation cannot change active lifecycle state before Save")
	}
	if current == models.AutomationArchived && state != models.AutomationArchived {
		return errors.New("archived automation cannot be resumed")
	}
	if _, err := conn.ExecContext(ctx, `UPDATE schedules SET enabled = 0, updated_at = CURRENT_TIMESTAMP
		WHERE id IN (SELECT schedule_id FROM automation_trigger_owners WHERE project_id = ? AND automation_id = ?)`, projectID, automationID); err != nil {
		return err
	}
	if state == models.AutomationPaused || state == models.AutomationArchived {
		cancellationMessage := "Automation lifecycle changed before dispatch"
		if state == models.AutomationPaused {
			cancellationMessage = "Automation was paused before dispatch"
		} else if state == models.AutomationArchived {
			cancellationMessage = "Automation was archived before dispatch"
		}
		if _, err := conn.ExecContext(ctx, `UPDATE automation_invocations SET status = 'cancelled', error_message = ?,
			completed_at = COALESCE(completed_at, CURRENT_TIMESTAMP), updated_at = CURRENT_TIMESTAMP
			WHERE id IN (
				SELECT d.invocation_id FROM automation_dispatch_outbox d
				JOIN automation_invocations i ON i.id = d.invocation_id
				WHERE i.project_id = ? AND i.automation_id = ?
				AND d.execution_id IS NULL AND d.status IN ('pending', 'processing', 'submitted')
			)`, cancellationMessage, projectID, automationID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM automation_task_run_reservations
			WHERE dispatch_id IN (
				SELECT d.id FROM automation_dispatch_outbox d
				JOIN automation_invocations i ON i.id = d.invocation_id
				WHERE i.project_id = ? AND i.automation_id = ?
				AND d.execution_id IS NULL AND d.status IN ('pending', 'processing', 'submitted')
			)`, projectID, automationID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE automation_dispatch_outbox SET status = 'failed', last_error = ?,
			claimed_by = '', claim_expires_at = NULL, updated_at = CURRENT_TIMESTAMP
			WHERE id IN (
				SELECT d.id FROM automation_dispatch_outbox d
				JOIN automation_invocations i ON i.id = d.invocation_id
				WHERE i.project_id = ? AND i.automation_id = ?
				AND d.execution_id IS NULL AND d.status IN ('pending', 'processing', 'submitted')
			)`, cancellationMessage, projectID, automationID); err != nil {
			return err
		}
	}
	if state == models.AutomationPaused {
		if _, err := conn.ExecContext(ctx, `INSERT INTO automation_paused_task_admissions
			(task_id, project_id, automation_id, version_id)
			SELECT DISTINCT task.id, task.project_id, activity.automation_id, activity.version_id
			FROM tasks task
			JOIN automation_activity_resources resource ON resource.resource_type = 'task'
				AND resource.resource_id = task.id AND resource.relation = 'child'
			JOIN automation_activities activity ON activity.id = resource.activity_id
			WHERE task.project_id = ? AND task.category = 'active' AND task.status = 'pending'
				AND activity.project_id = ? AND activity.automation_id = ? AND activity.version_id = ?
				AND activity.activity_type = 'create_task'
			ON CONFLICT(task_id) DO NOTHING`, projectID, projectID, automationID, published.String); err != nil {
			return err
		}
	} else if _, err := conn.ExecContext(ctx, `DELETE FROM automation_paused_task_admissions
		WHERE project_id = ? AND automation_id = ?`, projectID, automationID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE tasks SET category = 'backlog', updated_at = CURRENT_TIMESTAMP
		WHERE project_id = ? AND category IN ('active', 'scheduled') AND status = 'pending'
		  AND (id IN (SELECT resource_id FROM automation_definition_resources
			WHERE project_id = ? AND automation_id = ? AND version_id = ? AND resource_type = 'task')
		  OR id IN (SELECT resource.resource_id FROM automation_activity_resources resource
			JOIN automation_activities activity ON activity.id = resource.activity_id
			WHERE resource.resource_type = 'task' AND resource.relation = 'child'
				AND activity.project_id = ? AND activity.automation_id = ? AND activity.version_id = ?
				AND activity.activity_type = 'create_task'))`,
		projectID, projectID, automationID, published.String, projectID, automationID, published.String); err != nil {
		return err
	}
	if state == models.AutomationArchived {
		if _, err := conn.ExecContext(ctx, `UPDATE automation_trigger_owners SET ownership_state = 'archived', updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND automation_id = ?`, projectID, automationID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE automations SET lifecycle_state = ?, archived_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`, state, projectID, automationID); err != nil {
			return err
		}
	} else {
		if _, err := conn.ExecContext(ctx, `UPDATE automation_trigger_owners SET ownership_state = 'paused', updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND automation_id = ?`, projectID, automationID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE automations SET lifecycle_state = ?, archived_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`, state, projectID, automationID); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	eventName := "automation.lifecycle.paused"
	if state == models.AutomationArchived {
		eventName = "automation.lifecycle.archived"
	}
	automationobs.Event(eventName,
		automationobs.String("project_id", projectID), automationobs.String("automation_id", automationID),
		automationobs.String("version_id", published.String), automationobs.String("state", string(state)))
	r.PublishInvalidation(events.AutomationDefinitionUpdated, projectID, models.AutomationBinding{AutomationID: automationID, VersionID: published.String})
	return nil
}

func (r *AutomationRepo) DeleteAutomations(ctx context.Context, projectID string, automationIDs []string) error {
	versions, deletedTasks, err := r.deleteAutomations(ctx, projectID, automationIDs)
	if err != nil {
		return err
	}
	for _, id := range automationIDs {
		r.PublishInvalidation(events.AutomationDefinitionUpdated, projectID, models.AutomationBinding{AutomationID: id, VersionID: versions[id]})
	}
	r.publishDeletedAutomationTasks(projectID, deletedTasks)
	return nil
}

type deletedAutomationTask struct {
	id    string
	title string
}

func (r *AutomationRepo) deleteAutomations(ctx context.Context, projectID string, automationIDs []string) (map[string]string, []deletedAutomationTask, error) {
	if len(automationIDs) == 0 {
		return nil, nil, errors.New("at least one automation is required")
	}
	conn, finishImmediate, err := beginImmediateConn(ctx, r.db)
	if err != nil {
		return nil, nil, err
	}
	defer finishImmediate()
	placeholders := strings.TrimRight(strings.Repeat("?,", len(automationIDs)), ",")
	args := []any{projectID}
	for _, id := range automationIDs {
		args = append(args, id)
	}

	rows, err := conn.QueryContext(ctx, `SELECT id, COALESCE(published_version_id, '') FROM automations WHERE project_id = ? AND id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, nil, err
	}
	versions := make(map[string]string, len(automationIDs))
	for rows.Next() {
		var id, versionID string
		if err := rows.Scan(&id, &versionID); err != nil {
			rows.Close()
			return nil, nil, err
		}
		versions[id] = versionID
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	if len(versions) != len(automationIDs) {
		return nil, nil, errors.New("automation not found")
	}

	flightArgs := append(append([]any(nil), args...), args...)
	var inFlight int
	if err := conn.QueryRowContext(ctx, `SELECT CASE WHEN EXISTS (
		SELECT 1 FROM automation_invocations i LEFT JOIN automation_dispatch_outbox d ON d.invocation_id = i.id
		WHERE i.project_id = ? AND i.automation_id IN (`+placeholders+`) AND ((i.status IN ('claimed','dispatched','running') AND d.id IS NOT NULL) OR d.status IN ('pending','processing','submitted') OR EXISTS (SELECT 1 FROM executions e WHERE e.dispatch_id = d.id AND e.status = 'running'))
	) OR EXISTS (
		SELECT 1 FROM automation_activities a JOIN automation_activity_resources ar ON ar.activity_id = a.id AND ar.resource_type = 'execution' JOIN executions e ON e.id = ar.resource_id
		WHERE a.project_id = ? AND a.automation_id IN (`+placeholders+`) AND e.status = 'running'
	) THEN 1 ELSE 0 END`, flightArgs...).Scan(&inFlight); err != nil {
		return nil, nil, err
	}
	if inFlight > 0 {
		return nil, nil, ErrAutomationDispatchInFlight
	}

	taskRows, err := conn.QueryContext(ctx, `SELECT DISTINCT schedule.task_id
		FROM automation_trigger_owners owner
		JOIN schedules schedule ON schedule.id = owner.schedule_id
		JOIN tasks task ON task.id = schedule.task_id AND task.project_id = owner.project_id
		WHERE owner.project_id = ? AND owner.automation_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, nil, err
	}
	var triggerTaskIDs []string
	for taskRows.Next() {
		var taskID string
		if err := taskRows.Scan(&taskID); err != nil {
			taskRows.Close()
			return nil, nil, err
		}
		triggerTaskIDs = append(triggerTaskIDs, taskID)
	}
	if err := taskRows.Close(); err != nil {
		return nil, nil, err
	}

	if _, err := conn.ExecContext(ctx, `DELETE FROM schedules WHERE id IN (SELECT schedule_id FROM automation_trigger_owners WHERE project_id = ? AND automation_id IN (`+placeholders+`))`, args...); err != nil {
		return nil, nil, err
	}

	var deletedTasks []deletedAutomationTask
	if len(triggerTaskIDs) > 0 {
		taskPlaceholders := strings.TrimRight(strings.Repeat("?,", len(triggerTaskIDs)), ",")
		deleteArgs := []any{projectID}
		for _, taskID := range triggerTaskIDs {
			deleteArgs = append(deleteArgs, taskID)
		}
		deleteArgs = append(deleteArgs, projectID)
		for _, id := range automationIDs {
			deleteArgs = append(deleteArgs, id)
		}
		deletedRows, err := conn.QueryContext(ctx, `DELETE FROM tasks AS task
			WHERE task.project_id = ? AND task.id IN (`+taskPlaceholders+`)
			  AND NOT EXISTS (SELECT 1 FROM schedules schedule WHERE schedule.task_id = task.id)
			  AND NOT EXISTS (
				SELECT 1 FROM automation_definition_resources resource
				WHERE resource.project_id = ? AND resource.resource_type = 'task' AND resource.resource_id = task.id
				  AND resource.automation_id NOT IN (`+placeholders+`)
			  )
			RETURNING id, title`, deleteArgs...)
		if err != nil {
			return nil, nil, err
		}
		for deletedRows.Next() {
			var task deletedAutomationTask
			if err := deletedRows.Scan(&task.id, &task.title); err != nil {
				deletedRows.Close()
				return nil, nil, err
			}
			deletedTasks = append(deletedTasks, task)
		}
		if err := deletedRows.Close(); err != nil {
			return nil, nil, err
		}
	}

	if _, err := conn.ExecContext(ctx, `DELETE FROM automations WHERE project_id = ? AND id IN (`+placeholders+`)`, args...); err != nil {
		return nil, nil, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, nil, err
	}
	return versions, deletedTasks, nil
}

func (r *AutomationRepo) publishDeletedAutomationTasks(projectID string, tasks []deletedAutomationTask) {
	if r.broadcaster == nil {
		return
	}
	for _, task := range tasks {
		r.broadcaster.Publish(events.TaskEvent{Type: events.TaskBoardUpdated, TaskID: task.id, TaskName: task.title, ProjectID: projectID})
	}
}

// DeleteAutomation permanently removes one project-scoped Automation definition,
// its Automation-owned trigger schedules, and trigger tasks that have no surviving owner.
func (r *AutomationRepo) DeleteAutomation(ctx context.Context, projectID, automationID string) error {
	versions, deletedTasks, err := r.deleteAutomations(ctx, projectID, []string{automationID})
	if err != nil {
		return err
	}
	versionID := versions[automationID]
	automationobs.Event("automation.lifecycle.deleted",
		automationobs.String("project_id", projectID), automationobs.String("automation_id", automationID),
		automationobs.String("version_id", versionID), automationobs.String("state", "deleted"))
	r.PublishInvalidation(events.AutomationDefinitionUpdated, projectID, models.AutomationBinding{AutomationID: automationID, VersionID: versionID})
	r.publishDeletedAutomationTasks(projectID, deletedTasks)
	return nil
}

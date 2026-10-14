package sqlitefixtures

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"

	"github.com/openvibely/openvibely/internal/models"
)

func (f *fixtures) SeedAutomation(ctx context.Context, a models.Automation) error {
	_, err := f.db.ExecContext(ctx, `INSERT INTO automations (id,project_id,stable_key,name,description,automation_type,lifecycle_state,published_version_id,created_via) VALUES (?,?,?,?,?,?,?,?,?)`, a.ID, a.ProjectID, a.StableKey, a.Name, a.Description, a.AutomationType, a.LifecycleState, a.PublishedVersionID, a.CreatedVia)
	return err
}
func (f *fixtures) SeedAutomationVersion(ctx context.Context, v models.AutomationVersion) error {
	_, err := f.db.ExecContext(ctx, `INSERT INTO automation_versions (id,project_id,automation_id,version,state,source,adapter_key,published_at) VALUES (?,?,?,?,?,?,?,?)`, v.ID, v.ProjectID, v.AutomationID, v.Version, v.State, v.Source, v.AdapterKey, v.PublishedAt)
	return err
}
func (f *fixtures) AutomationVersions(ctx context.Context, automationID string) ([]models.AutomationVersion, error) {
	rows, err := f.db.QueryContext(ctx, `SELECT id,project_id,automation_id,version,state,source,adapter_key,schema_version,created_at,published_at FROM automation_versions WHERE automation_id = ? ORDER BY version,id`, automationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var versions []models.AutomationVersion
	for rows.Next() {
		var v models.AutomationVersion
		if err := rows.Scan(&v.ID, &v.ProjectID, &v.AutomationID, &v.Version, &v.State, &v.Source, &v.AdapterKey, &v.SchemaVersion, &v.CreatedAt, &v.PublishedAt); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}
func (f *fixtures) SetAutomationNodeConfig(ctx context.Context, id, config string) error {
	_, err := f.db.ExecContext(ctx, `UPDATE automation_nodes SET config_json = ? WHERE id = ?`, config, id)
	return err
}

func (f *fixtures) SetAutomationTemplateRevision(ctx context.Context, projectID, id string, revision int) error {
	_, err := f.db.ExecContext(ctx, `UPDATE automations SET template_revision=? WHERE id=? AND project_id=?`, revision, id, projectID)
	return err
}

func (f *fixtures) SeedAutomationInvocation(ctx context.Context, v models.AutomationInvocation) error {
	var createdAt, updatedAt any
	if !v.UpdatedAt.IsZero() {
		updatedAt = v.UpdatedAt.UTC()
	}
	if !v.CreatedAt.IsZero() {
		createdAt = v.CreatedAt.UTC().Format("2006-01-02 15:04:05")
	}
	_, err := f.db.ExecContext(ctx, `INSERT INTO automation_invocations (id,project_id,automation_id,version_id,trigger_node_id,trigger_resource_type,trigger_resource_id,occurrence_key,status,completed_at,started_at,error_message,skipped_reason,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,COALESCE(?,CURRENT_TIMESTAMP),?,?,COALESCE(?,CURRENT_TIMESTAMP),COALESCE(?,CURRENT_TIMESTAMP))`, v.ID, v.ProjectID, v.AutomationID, v.VersionID, v.TriggerNodeID, v.TriggerResourceType, v.TriggerResourceID, v.OccurrenceKey, v.Status, v.CompletedAt, v.StartedAt, v.ErrorMessage, v.SkippedReason, createdAt, updatedAt)
	return err
}

func (f *fixtures) SeedRetainedScheduleOwnership(ctx context.Context, n models.AutomationNode, scheduleID string) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO automation_nodes(id,project_id,automation_id,version_id,node_key,name,node_type,role) VALUES(?,?,?,?,?,?,?,?)`, n.ID, n.ProjectID, n.AutomationID, n.VersionID, n.NodeKey, n.Name, n.NodeType, n.Role); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO automation_definition_resources(project_id,automation_id,version_id,node_id,resource_type,resource_id,relation) VALUES(?,?,?,?,'schedule',?,'owned')`, n.ProjectID, n.AutomationID, n.VersionID, n.ID, scheduleID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO automation_trigger_owners(schedule_id,project_id,automation_id,version_id,node_id,ownership_state) VALUES(?,?,?,?,?,'active')`, scheduleID, n.ProjectID, n.AutomationID, n.VersionID, n.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (f *fixtures) SetAutomationUpdatedAt(ctx context.Context, id string, at time.Time) error {
	return f.setTime(ctx, `UPDATE automations SET updated_at=? WHERE id=?`, id, at)
}

func (f *fixtures) SeedAutomationNode(ctx context.Context, n models.AutomationNode) error {
	_, err := f.db.ExecContext(ctx, `INSERT INTO automation_nodes(id,project_id,automation_id,version_id,node_key,name,node_type,role,config_json,position_x,position_y) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, n.ID, n.ProjectID, n.AutomationID, n.VersionID, n.NodeKey, n.Name, n.NodeType, n.Role, n.ConfigJSON, n.PositionX, n.PositionY)
	return err
}

func (f *fixtures) RenewAutomationDispatchLease(ctx context.Context, id, owner string, expires time.Time) error {
	return sqlite.NewAutomationRepo(f.db).RenewDispatchLease(ctx, id, owner, expires)
}
func (f *fixtures) AutomationDispatchState(ctx context.Context, id string) (storefixture.AutomationDispatchState, error) {
	var state storefixture.AutomationDispatchState
	err := f.db.QueryRowContext(ctx, `SELECT status,(SELECT COUNT(*) FROM automation_task_run_reservations WHERE dispatch_id = ?),(SELECT COUNT(*) FROM executions WHERE dispatch_id = ?) FROM automation_dispatch_outbox WHERE id = ?`, id, id, id).Scan(&state.Status, &state.TaskReservations, &state.Executions)
	return state, err
}
func (f *fixtures) CompleteAutomationActivity(ctx context.Context, id string, at time.Time) error {
	_, err := f.db.ExecContext(ctx, `UPDATE automation_activities SET status='completed',completed_at=? WHERE id=?`, at.UTC().Format(time.RFC3339Nano), id)
	return err
}

func (f *fixtures) SeedAutomationIssueTaskOrigin(ctx context.Context, origin storefixture.AutomationIssueTaskOrigin) error {
	_, err := f.db.ExecContext(ctx, `INSERT INTO automation_github_issue_task_provenance (project_id,automation_id,task_id,issue_resource_id,implementation_node_key) VALUES (?,?,?,?,?)`, origin.ProjectID, origin.AutomationID, origin.TaskID, origin.IssueResourceID, origin.ImplementationNodeKey)
	return err
}

func (f *fixtures) FailLatestActiveAutomationInvocation(ctx context.Context, projectID, automationID string) (string, error) {
	var id string
	err := f.db.QueryRowContext(ctx, `UPDATE automation_invocations SET status = 'failed', completed_at = CURRENT_TIMESTAMP WHERE id = (SELECT id FROM automation_invocations WHERE project_id = ? AND automation_id = ? AND status IN ('claimed','dispatched','running') ORDER BY rowid DESC LIMIT 1) RETURNING id`, projectID, automationID).Scan(&id)
	return id, err
}

func (f *fixtures) SetAutomationInvocationCompletionTime(ctx context.Context, id string, at time.Time) error {
	_, err := f.db.ExecContext(ctx, `UPDATE automation_invocations SET completed_at = ?, updated_at = ? WHERE id = ?`, at.UTC().Format("2006-01-02 15:04:05"), at.UTC().Format("2006-01-02 15:04:05"), id)
	return err
}
func (f *fixtures) SetTaskPullRequestUpdatedAt(ctx context.Context, id string, at time.Time) error {
	_, err := f.db.ExecContext(ctx, `UPDATE task_pull_requests SET updated_at = ? WHERE task_id = ?`, at.UTC().Format("2006-01-02 15:04:05"), id)
	return err
}

func (f *fixtures) SeedAutomationActivity(ctx context.Context, a models.AutomationActivity, resources []models.AutomationActivityResource) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO automation_activities(id,project_id,automation_id,version_id,node_id,invocation_id,work_item_id,activity_key,activity_type,status,started_at,completed_at,error_message) VALUES(?,?,?,?,?,NULLIF(?, ''),NULLIF(?, ''),?,?,?,COALESCE(?,CURRENT_TIMESTAMP),?,?)`, a.ID, a.ProjectID, a.AutomationID, a.VersionID, a.NodeID, a.InvocationID, a.WorkItemID, a.ActivityKey, a.ActivityType, a.Status, optionalActivityStart(a.StartedAt), a.CompletedAt, a.ErrorMessage)
	if err != nil {
		return err
	}
	for _, r := range resources {
		if _, err = tx.ExecContext(ctx, `INSERT INTO automation_activity_resources(activity_id,resource_type,resource_id,relation) VALUES(?,?,?,?)`, a.ID, r.ResourceType, r.ResourceID, r.Relation); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (f *fixtures) SetAutomationInvocationStart(ctx context.Context, id string, at time.Time) error {
	_, err := f.db.ExecContext(ctx, `UPDATE automation_invocations SET created_at=?,started_at=? WHERE id=?`, at.UTC().Format("2006-01-02 15:04:05"), at.UTC().Format("2006-01-02 15:04:05"), id)
	return err
}
func (f *fixtures) SetAutomationActivityStart(ctx context.Context, id string, at time.Time) error {
	_, err := f.db.ExecContext(ctx, `UPDATE automation_activities SET started_at=? WHERE id=?`, at.UTC().Format("2006-01-02 15:04:05"), id)
	return err
}
func (f *fixtures) SetAutomationTransitionTime(ctx context.Context, automationID, eventKey string, at time.Time) error {
	_, err := f.db.ExecContext(ctx, `UPDATE automation_transitions SET occurred_at=? WHERE automation_id=? AND event_key=?`, at.UTC().Format("2006-01-02 15:04:05"), automationID, eventKey)
	return err
}
func (f *fixtures) SetAutomationWorkItemHistory(ctx context.Context, id string, createdAt time.Time, completed bool) error {
	status := "active"
	var completedAt *time.Time
	if completed {
		status = "completed"
		now := time.Now().UTC()
		completedAt = &now
	}
	_, err := f.db.ExecContext(ctx, `UPDATE automation_work_items SET created_at=?,status=?,completed_at=? WHERE id=?`, createdAt.UTC().Format("2006-01-02 15:04:05"), status, completedAt, id)
	return err
}

func (f *fixtures) SetAutomationHealthEvaluationTime(ctx context.Context, id string, at *time.Time) error {
	_, err := f.db.ExecContext(ctx, `UPDATE automations SET health_evaluated_at = ? WHERE id = ?`, at, id)
	return err
}
func (f *fixtures) SetAutomationPublishedVersion(ctx context.Context, id, versionID string) error {
	_, err := f.db.ExecContext(ctx, `UPDATE automations SET published_version_id = NULLIF(?, '') WHERE id = ?`, versionID, id)
	return err
}

func (f *fixtures) RemoveAutomationCandidate(ctx context.Context, projectID, automationID, versionID string) error {
	_, err := f.db.ExecContext(ctx, `DELETE FROM automation_graph_metadata WHERE project_id = ? AND automation_id = ? AND version_id = ?`, projectID, automationID, versionID)
	return err
}
func (f *fixtures) SetAutomationCandidate(ctx context.Context, id string, candidate models.AutomationDraftCandidate) error {
	data, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	_, err = f.db.ExecContext(ctx, `UPDATE automation_graph_metadata SET candidate_json = ? WHERE automation_id = ?`, string(data), id)
	return err
}

func (f *fixtures) AutomationState(ctx context.Context) (storefixture.AutomationStateCounts, error) {
	var state storefixture.AutomationStateCounts
	err := f.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM automations),(SELECT COUNT(*) FROM automation_versions),(SELECT COUNT(*) FROM automation_nodes),(SELECT COUNT(*) FROM automation_edges),(SELECT COUNT(*) FROM automation_definition_resources),(SELECT COUNT(*) FROM tasks),(SELECT COUNT(*) FROM schedules),(SELECT COUNT(*) FROM automation_invocations),(SELECT COUNT(*) FROM automation_work_items),(SELECT COUNT(*) FROM automation_activities),(SELECT COUNT(*) FROM automation_chat_confirmation_receipts),(SELECT COUNT(*) FROM automation_dispatch_outbox)`).Scan(&state.Automations, &state.Versions, &state.Nodes, &state.Edges, &state.Resources, &state.Tasks, &state.Schedules, &state.Invocations, &state.WorkItems, &state.Activities, &state.ChatConfirmations, &state.Dispatches)
	return state, err
}
func (f *fixtures) FailScheduleCreation(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fail_atomic_automation_schedule BEFORE INSERT ON schedules BEGIN SELECT RAISE(ABORT, 'injected schedule failure'); END`)
	return err
}
func (f *fixtures) FailScheduleUpdate(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fail_atomic_automation_schedule_update BEFORE UPDATE ON schedules BEGIN SELECT RAISE(ABORT, 'injected schedule update failure'); END`)
	return err
}

func (f *fixtures) SeedAutomationEdge(ctx context.Context, e models.AutomationEdge) error {
	_, err := f.db.ExecContext(ctx, `INSERT INTO automation_edges(project_id,automation_id,version_id,source_node_id,target_node_id,edge_key,label,condition_json,display_order) VALUES(?,?,?,?,?,?,?,?,?)`, e.ProjectID, e.AutomationID, e.VersionID, e.SourceNodeID, e.TargetNodeID, e.EdgeKey, e.Label, e.ConditionJSON, e.DisplayOrder)
	return err
}
func (f *fixtures) SeedAutomationResource(ctx context.Context, r models.AutomationDefinitionResource) error {
	_, err := f.db.ExecContext(ctx, `INSERT INTO automation_definition_resources(project_id,automation_id,version_id,node_id,resource_type,resource_id,relation) VALUES(?,?,?,?,?,?,?)`, r.ProjectID, r.AutomationID, r.VersionID, r.NodeID, r.ResourceType, r.ResourceID, r.Relation)
	return err
}
func (f *fixtures) SetAutomationHealth(ctx context.Context, id string, state models.AutomationHealthState, reason string) error {
	_, err := f.db.ExecContext(ctx, `UPDATE automations SET health_state=?,health_reason=? WHERE id=?`, state, reason, id)
	return err
}

func (f *fixtures) ProjectAutomationState(ctx context.Context, projectID string) (storefixture.ProjectAutomationState, error) {
	var state storefixture.ProjectAutomationState
	err := f.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM automations WHERE project_id = ?),(SELECT COUNT(*) FROM tasks WHERE project_id = ?),(SELECT COUNT(*) FROM schedules s JOIN tasks t ON t.id = s.task_id WHERE t.project_id = ?)`, projectID, projectID, projectID).Scan(&state.Automations, &state.Tasks, &state.Schedules)
	return state, err
}

func (f *fixtures) SetAutomationLifecycleSnapshot(ctx context.Context, project, id string, state models.AutomationLifecycleState) error {
	_, err := f.db.ExecContext(ctx, `UPDATE automations SET lifecycle_state = ? WHERE id = ? AND project_id = ?`, state, id, project)
	return err
}
func (f *fixtures) SetTaskCreationOrigin(ctx context.Context, id, origin string) error {
	_, err := f.db.ExecContext(ctx, `UPDATE tasks SET created_via = ? WHERE id = ?`, origin, id)
	return err
}
func (f *fixtures) CountTasksByCreationOrigin(ctx context.Context, project, origin string) (int, error) {
	var n int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE created_via = ? AND (? = '' OR project_id = ?)`, origin, project, project).Scan(&n)
	return n, err
}
func (f *fixtures) CountAutomationTriggerOwners(ctx context.Context, project, automation, node string) (int, error) {
	var n int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_trigger_owners WHERE project_id = ? AND automation_id = ? AND node_id = ?`, project, automation, node).Scan(&n)
	return n, err
}

func (f *fixtures) CountAutomationInvocations(ctx context.Context, projectID, automationID, triggerID string) (int, error) {
	var count int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_invocations WHERE project_id=? AND automation_id=? AND (?='' OR trigger_resource_id=?)`, projectID, automationID, triggerID, triggerID).Scan(&count)
	return count, err
}

func (f *fixtures) AutomationExecutionOwnership(ctx context.Context, activityID, executionID string) (storefixture.AutomationExecutionOwnership, error) {
	var state storefixture.AutomationExecutionOwnership
	err := f.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM automation_activities WHERE id = ?), (SELECT COUNT(*) FROM automation_activity_resources WHERE activity_id = ? AND resource_type = 'execution' AND resource_id = ?)`, activityID, activityID, executionID).Scan(&state.Activities, &state.ExecutionResources)
	return state, err
}
func (f *fixtures) CountDispatchExecutions(ctx context.Context, dispatchID string) (int, error) {
	var count int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM executions WHERE dispatch_id = ?`, dispatchID).Scan(&count)
	return count, err
}

func (f *fixtures) RemoveAutomationTaskBinding(ctx context.Context, versionID, taskID string) error {
	_, err := f.db.ExecContext(ctx, `DELETE FROM automation_definition_resources WHERE version_id = ? AND resource_type = 'task' AND resource_id = ?`, versionID, taskID)
	return err
}

func (f *fixtures) SeedAutomationWorkItemPosition(ctx context.Context, w models.AutomationWorkItem, p models.AutomationWorkItemPosition) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO automation_work_items(id,project_id,automation_id,origin_version_id,work_item_key,kind,title,status) VALUES(?,?,?,?,?,?,?,?)`, w.ID, w.ProjectID, w.AutomationID, w.OriginVersionID, w.WorkItemKey, w.Kind, w.Title, w.Status); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO automation_work_item_positions(work_item_id,project_id,automation_id,version_id,node_id,state) VALUES(?,?,?,?,?,?)`, p.WorkItemID, p.ProjectID, p.AutomationID, p.VersionID, p.NodeID, p.State); err != nil {
		return err
	}
	return tx.Commit()
}
func (f *fixtures) SetAutomationPositionState(ctx context.Context, workItemID, nodeID string, state models.AutomationPositionState) error {
	_, err := f.db.ExecContext(ctx, `UPDATE automation_work_item_positions SET state=? WHERE work_item_id=? AND node_id=?`, state, workItemID, nodeID)
	return err
}

func (f *fixtures) CountAutomationActivities(ctx context.Context, automationID, kind string, status models.AutomationActivityStatus) (int, error) {
	var n int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_activities WHERE automation_id=? AND activity_type=? AND status=?`, automationID, kind, status).Scan(&n)
	return n, err
}
func (f *fixtures) CountAutomationWorkItems(ctx context.Context, automationID, key string) (int, error) {
	var n int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_work_items WHERE automation_id=? AND work_item_key=?`, automationID, key).Scan(&n)
	return n, err
}
func (f *fixtures) CountAutomationTransitions(ctx context.Context, automationID, edgeKey string) (int, error) {
	var n int
	var err error
	if edgeKey == "" {
		err = f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_transitions WHERE automation_id=?`, automationID).Scan(&n)
	} else {
		err = f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_transitions tr JOIN automation_edges e ON e.id=tr.edge_id WHERE tr.automation_id=? AND e.edge_key=?`, automationID, edgeKey).Scan(&n)
	}
	return n, err
}
func (f *fixtures) CountAutomationResourceLinks(ctx context.Context, kind, id string) (int, error) {
	var n int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_activity_resources WHERE resource_type=? AND resource_id=?`, kind, id).Scan(&n)
	return n, err
}
func (f *fixtures) AutomationContextForWorkItem(ctx context.Context, projectID, key string) (models.AutomationContext, error) {
	return sqlite.NewAutomationRepo(f.db).BindingsForWorkItemKey(ctx, projectID, key)
}

func (f *fixtures) EditPendingAutomationTask(ctx context.Context, id, agentID string, prompt *string) error {
	var updated string
	return f.db.QueryRowContext(ctx, `UPDATE tasks SET agent_id=?, prompt=COALESCE(?,prompt) WHERE id=? AND status='pending' RETURNING id`, agentID, prompt, id).Scan(&updated)
}
func (f *fixtures) ExpireAutomationDispatch(ctx context.Context, id string, at time.Time) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE automation_dispatch_outbox SET claim_expires_at=? WHERE id=?`, at, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE automation_task_run_reservations SET lease_expires_at=? WHERE dispatch_id=?`, at, id); err != nil {
		return err
	}
	return tx.Commit()
}
func (f *fixtures) InspectAutomationDispatch(ctx context.Context, id string) (storefixture.AutomationDispatchOutcome, error) {
	var s storefixture.AutomationDispatchOutcome
	err := f.db.QueryRowContext(ctx, `SELECT d.status,COALESCE(d.execution_id,''),t.status,COALESCE(e.status,''),i.status,
 COALESCE((SELECT status FROM automation_activities WHERE invocation_id=i.id AND activity_key='dispatch:'||d.id||':execute'),''),
 COALESCE(e.error_message,''),COALESCE(e.prompt_sent,''),COALESCE(e.agent_config_id,''),
 (SELECT COUNT(*) FROM automation_task_run_reservations WHERE dispatch_id=d.id),
 (SELECT COUNT(*) FROM executions WHERE dispatch_id=d.id),
 (SELECT COUNT(*) FROM automation_activities WHERE activity_key='dispatch:'||d.id||':execute'),
 (SELECT COUNT(DISTINCT a.id) FROM automation_activities a JOIN automation_activity_resources ar ON ar.activity_id=a.id WHERE ar.resource_type='execution' AND ar.resource_id=e.id),
 e.completed_at IS NOT NULL,i.completed_at IS NOT NULL,d.next_attempt_at
 FROM automation_dispatch_outbox d JOIN tasks t ON t.id=d.task_id JOIN automation_invocations i ON i.id=d.invocation_id LEFT JOIN executions e ON e.id=d.execution_id WHERE d.id=?`, id).Scan(&s.Status, &s.ExecutionID, &s.TaskStatus, &s.ExecutionStatus, &s.InvocationStatus, &s.ActivityStatus, &s.ExecutionError, &s.PromptSent, &s.AgentConfigID, &s.Reservations, &s.Executions, &s.Activities, &s.ExecutionActivities, &s.ExecutionCompleted, &s.InvocationCompleted, &s.NextAttemptAt)
	return s, err
}

func (f *fixtures) SeedAutomationTriggerOwnership(ctx context.Context, n models.AutomationNode, scheduleID, taskID string) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range []struct{ kind, id string }{{"schedule", scheduleID}, {"task", taskID}} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO automation_definition_resources(project_id,automation_id,version_id,node_id,resource_type,resource_id,relation) VALUES(?,?,?,?,?,?,'owned')`, n.ProjectID, n.AutomationID, n.VersionID, n.ID, r.kind, r.id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO automation_trigger_owners(schedule_id,project_id,automation_id,version_id,node_id,ownership_state) VALUES(?,?,?,?,?,'active')`, scheduleID, n.ProjectID, n.AutomationID, n.VersionID, n.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func (f *fixtures) AutomationDispatchOrder(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := f.db.QueryContext(ctx, `SELECT id FROM automation_dispatch_outbox WHERE id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`) ORDER BY created_at,id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}
func (f *fixtures) CountTaskAutomationReservations(ctx context.Context, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	var n int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_task_run_reservations WHERE task_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...).Scan(&n)
	return n, err
}

func optionalActivityStart(at time.Time) any {
	if at.IsZero() {
		return nil
	}
	return at.UTC().Format("2006-01-02 15:04:05")
}

func (f *fixtures) AutomationDefinitionResources(ctx context.Context, match storefixture.AutomationResourceMatch) ([]models.AutomationDefinitionResource, error) {
	q := `SELECT r.project_id,r.automation_id,r.version_id,r.node_id,n.node_key,r.resource_type,r.resource_id,r.relation FROM automation_definition_resources r JOIN automation_nodes n ON n.id=r.node_id AND n.version_id=r.version_id WHERE 1=1`
	var args []any
	for _, v := range []struct{ column, value string }{{"r.project_id", match.ProjectID}, {"r.automation_id", match.AutomationID}, {"r.version_id", match.VersionID}, {"n.node_key", match.NodeKey}, {"r.resource_type", match.ResourceType}} {
		if v.value != "" {
			q += " AND " + v.column + "=?"
			args = append(args, v.value)
		}
	}
	rows, err := f.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.AutomationDefinitionResource
	for rows.Next() {
		var r models.AutomationDefinitionResource
		if err := rows.Scan(&r.ProjectID, &r.AutomationID, &r.VersionID, &r.NodeID, &r.NodeKey, &r.ResourceType, &r.ResourceID, &r.Relation); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
func (f *fixtures) AutomationOwnedScheduleIDs(ctx context.Context, id string) ([]string, error) {
	rows, err := f.db.QueryContext(ctx, `SELECT schedule_id FROM automation_trigger_owners WHERE automation_id=? ORDER BY schedule_id`, id)
	if err != nil {
		return nil, err
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
func (f *fixtures) ClearAutomationTemplateRevision(ctx context.Context, id string) error {
	_, err := f.db.ExecContext(ctx, `UPDATE automations SET template_revision=NULL WHERE id=?`, id)
	return err
}

func (f *fixtures) GitHubIssueClaims(ctx context.Context, project, repo, fingerprint string) ([]storefixture.GitHubIssueClaim, error) {
	q := `SELECT owner_token,projection_source_json,mutation_state,created_issue_number FROM automation_github_issue_dedup_leases WHERE project_id=? AND repository_full_name=?`
	args := []any{project, repo}
	if fingerprint != "" {
		q += ` AND title_fingerprint=?`
		args = append(args, fingerprint)
	}
	rows, err := f.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []storefixture.GitHubIssueClaim
	for rows.Next() {
		var claim storefixture.GitHubIssueClaim
		var number sql.NullInt64
		if err := rows.Scan(&claim.OwnerToken, &claim.ProjectionSource, &claim.MutationState, &number); err != nil {
			return nil, err
		}
		if number.Valid {
			n := int(number.Int64)
			claim.IssueNumber = &n
		}
		result = append(result, claim)
	}
	return result, rows.Err()
}
func (f *fixtures) ExpireGitHubIssueClaim(ctx context.Context, project, repo, fingerprint string, at time.Time) error {
	_, err := f.db.ExecContext(ctx, `UPDATE automation_github_issue_dedup_leases SET lease_expires_at=? WHERE project_id=? AND repository_full_name=? AND title_fingerprint=?`, at, project, repo, fingerprint)
	return err
}
func (f *fixtures) SetIssueProjectionFailure(ctx context.Context, failure *storefixture.IssueProjectionFailure) error {
	if _, err := f.db.ExecContext(ctx, `DROP TRIGGER IF EXISTS fail_fixture_issue_projection`); err != nil {
		return err
	}
	if failure == nil {
		return nil
	}
	target, condition, message := "automation_work_items", "", "injected issue projection failure"
	if failure.AutomationID != "" {
		condition = " WHEN NEW.automation_id='" + strings.ReplaceAll(failure.AutomationID, "'", "''") + "'"
		message = "injected second binding projection failure"
	}
	if failure.AssignmentEventKey != "" {
		target = "automation_transitions"
		condition = " WHEN NEW.event_key='" + strings.ReplaceAll(failure.AssignmentEventKey, "'", "''") + "'"
	}
	_, err := f.db.ExecContext(ctx, "CREATE TRIGGER fail_fixture_issue_projection BEFORE INSERT ON "+target+condition+" BEGIN SELECT RAISE(FAIL, '"+message+"'); END")
	return err
}

func (f *fixtures) CountTaskThreadInputs(ctx context.Context, id string) (int, error) {
	var n int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM thread_inputs WHERE task_id=?`, id).Scan(&n)
	return n, err
}
func (f *fixtures) SeedReservedAutomationTask(ctx context.Context, project, task, schedule string) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO automations(id,project_id,stable_key,name) VALUES('status-count-auto',?,'status-count-auto','Status count automation')`, []any{project}},
		{`INSERT INTO automation_versions(id,project_id,automation_id,version,adapter_key) VALUES('status-count-version',?,'status-count-auto',1,'test')`, []any{project}},
		{`INSERT INTO automation_nodes(id,project_id,automation_id,version_id,node_key,name,node_type,role) VALUES('status-count-node',?,'status-count-auto','status-count-version','trigger','Trigger','trigger','trigger')`, []any{project}},
		{`INSERT INTO automation_invocations(id,project_id,automation_id,version_id,trigger_node_id,trigger_resource_type,trigger_resource_id,occurrence_key) VALUES('status-count-invocation',?,'status-count-auto','status-count-version','status-count-node','schedule',?,'status-count-occurrence')`, []any{project, schedule}},
		{`INSERT INTO automation_dispatch_outbox(id,invocation_id,task_id) VALUES('status-count-dispatch','status-count-invocation',?)`, []any{task}},
		{`INSERT INTO automation_task_run_reservations(task_id,dispatch_id,project_id) VALUES(?,'status-count-dispatch',?)`, []any{task, project}},
	}
	for _, s := range statements {
		if _, err = tx.ExecContext(ctx, s.query, s.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

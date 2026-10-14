package sqlitefixtures

import (
	"context"
	"encoding/json"
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
	_, err := f.db.ExecContext(ctx, `INSERT INTO automation_invocations (id,project_id,automation_id,version_id,trigger_node_id,trigger_resource_type,trigger_resource_id,occurrence_key,status,completed_at,started_at,error_message) VALUES (?,?,?,?,?,?,?,?,?,?,COALESCE(?,CURRENT_TIMESTAMP),?)`, v.ID, v.ProjectID, v.AutomationID, v.VersionID, v.TriggerNodeID, v.TriggerResourceType, v.TriggerResourceID, v.OccurrenceKey, v.Status, v.CompletedAt, v.StartedAt, v.ErrorMessage)
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
	err := f.db.QueryRowContext(ctx, `SELECT status,(SELECT COUNT(*) FROM automation_task_run_reservations WHERE dispatch_id = ?) FROM automation_dispatch_outbox WHERE id = ?`, id, id).Scan(&state.Status, &state.TaskReservations)
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
	_, err = tx.ExecContext(ctx, `INSERT INTO automation_activities(id,project_id,automation_id,version_id,node_id,invocation_id,activity_key,activity_type,status) VALUES(?,?,?,?,?,?,?,?,?)`, a.ID, a.ProjectID, a.AutomationID, a.VersionID, a.NodeID, a.InvocationID, a.ActivityKey, a.ActivityType, a.Status)
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
	_, err := f.db.ExecContext(ctx, `UPDATE automations SET published_version_id = ? WHERE id = ?`, versionID, id)
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
	err := f.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM automations),(SELECT COUNT(*) FROM automation_versions),(SELECT COUNT(*) FROM automation_nodes),(SELECT COUNT(*) FROM automation_edges),(SELECT COUNT(*) FROM automation_definition_resources),(SELECT COUNT(*) FROM tasks),(SELECT COUNT(*) FROM schedules)`).Scan(&state.Automations, &state.Versions, &state.Nodes, &state.Edges, &state.Resources, &state.Tasks, &state.Schedules)
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

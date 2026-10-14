package sqlitefixtures

import (
	"context"
	"database/sql/driver"
	"errors"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
)

// SeedDiscardedIssueDiscovery preserves legacy orphan relationships atomically.
// Enforcement is restored on the same connection, even when seeding fails.
func (f *fixtures) SeedDiscardedIssueDiscovery(ctx context.Context, s storefixture.DiscardedIssueDiscovery) (err error) {
	conn, err := f.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		_, restore := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
		if restore != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		err = errors.Join(err, restore)
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	operations := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO automation_work_items(id,project_id,automation_id,origin_version_id,work_item_key,kind,title,status) VALUES(?,?,?,?,?,'github_issue','Old title','waiting')`, []any{s.WorkItemID, s.ProjectID, s.AutomationID, s.VersionID, s.ResourceID}},
		{`INSERT INTO automation_activities(id,project_id,automation_id,version_id,node_id,work_item_id,activity_key,activity_type,status) VALUES(?,?,?,?,?,?,'stale-discovery','discover_assigned_issue','completed')`, []any{s.ActivityID, s.ProjectID, s.AutomationID, s.VersionID, s.NodeID, s.WorkItemID}},
		{`INSERT INTO automation_activity_resources(activity_id,resource_type,resource_id,relation) VALUES(?,'github_issue',?,'subject')`, []any{s.ActivityID, s.ResourceID}},
		{`INSERT INTO automation_work_item_positions(work_item_id,project_id,automation_id,version_id,node_id,state) VALUES(?,?,?,?,?,'waiting')`, []any{s.WorkItemID, s.ProjectID, s.AutomationID, s.VersionID, s.NodeID}},
		{`INSERT INTO thread_inputs(id,scope,project_id,task_id,input_mode,input_status,content,queue_position,source) VALUES(?,'task_thread',?,?,'queued','pending','stale queued follow-up',1,'test')`, []any{s.InputID, s.ProjectID, s.TaskID}},
		{`INSERT INTO automation_thread_input_bindings(id,thread_input_id,project_id,automation_id,version_id,node_id,work_item_id,binding_key) VALUES(?,?,?,?,?,?,?,'stale:0')`, []any{s.BindingID, s.InputID, s.ProjectID, s.AutomationID, s.VersionID, s.NodeID, s.WorkItemID}},
		{`INSERT INTO automation_work_items(id,project_id,automation_id,origin_version_id,parent_work_item_id,work_item_key,kind,title,status) VALUES(?,?,?,?,?,'stale-child-key','work','Stale child','waiting')`, []any{s.ChildID, s.ProjectID, s.AutomationID, s.VersionID, s.WorkItemID}},
		{`INSERT INTO automation_transitions(project_id,automation_id,version_id,work_item_id,activity_id,to_node_id,event_key,state) VALUES(?,?,?,?,?,?,?,'waiting')`, []any{s.ProjectID, s.AutomationID, s.VersionID, s.WorkItemID, s.ActivityID, s.NodeID, s.EventKey}},
	}
	for _, op := range operations {
		if _, err = tx.ExecContext(ctx, op.query, op.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (f *fixtures) CountIssueMailboxOwners(ctx context.Context) (int, error) {
	var count int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_artifact_mailbox_owners WHERE artifact_type='github_issue'`).Scan(&count)
	return count, err
}
func (f *fixtures) CountAutomationInputBinding(ctx context.Context, id string) (int, error) {
	var count int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_thread_input_bindings WHERE id=?`, id).Scan(&count)
	return count, err
}
func (f *fixtures) IssueDiscovery(ctx context.Context, project, automation, resource string) (storefixture.IssueDiscoveryState, error) {
	var s storefixture.IssueDiscoveryState
	err := f.db.QueryRowContext(ctx, `SELECT w.id,w.title,w.origin_version_id,COALESCE(w.origin_invocation_id,''),w.status,p.state,COALESCE(a.invocation_id,'')
 FROM automation_work_items w JOIN automation_work_item_positions p ON p.work_item_id=w.id JOIN automation_nodes n ON n.id=p.node_id
 JOIN automation_activities a ON a.work_item_id=w.id AND a.activity_type='discover_assigned_issue'
 JOIN automation_activity_resources r ON r.activity_id=a.id AND r.resource_type='github_issue' AND r.resource_id=?
 WHERE w.project_id=? AND w.automation_id=? AND w.work_item_key=? AND n.role='github_inbox'`, resource, project, automation, resource).Scan(&s.WorkItemID, &s.Title, &s.OriginVersionID, &s.OriginInvocationID, &s.Status, &s.Position, &s.DiscoveryInvocationID)
	return s, err
}

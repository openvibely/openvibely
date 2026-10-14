package sqlitefixtures

import (
	"context"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
)

func (f *fixtures) CountMatchingActivities(ctx context.Context, m storefixture.ActivityMatch) (int, error) {
	conditions := []string{"1=1"}
	args := []any{}
	if m.ProjectID != "" {
		conditions = append(conditions, "a.project_id=?")
		args = append(args, m.ProjectID)
	}
	if m.AutomationID != "" {
		conditions = append(conditions, "a.automation_id=?")
		args = append(args, m.AutomationID)
	}
	if m.VersionID != "" {
		conditions = append(conditions, "a.version_id=?")
		args = append(args, m.VersionID)
	}
	if m.ID != "" {
		conditions = append(conditions, "a.id=?")
		args = append(args, m.ID)
	}
	if m.Key != "" {
		conditions = append(conditions, "a.activity_key=?")
		args = append(args, m.Key)
	}
	if m.Type != "" {
		conditions = append(conditions, "a.activity_type=?")
		args = append(args, m.Type)
	}
	if m.Status != "" {
		conditions = append(conditions, "a.status=?")
		args = append(args, m.Status)
	}
	if m.WorkItemID != "" {
		conditions = append(conditions, "a.work_item_id=?")
		args = append(args, m.WorkItemID)
	}
	if m.HasWorkItem {
		conditions = append(conditions, "a.work_item_id IS NOT NULL")
	}
	var n int
	err := f.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM automation_activities a WHERE "+strings.Join(conditions, " AND "), args...).Scan(&n)
	return n, err
}
func (f *fixtures) CountMatchingWorkItems(ctx context.Context, m storefixture.WorkItemMatch) (int, error) {
	conditions := []string{"1=1"}
	args := []any{}
	if m.ProjectID != "" {
		conditions = append(conditions, "w.project_id=?")
		args = append(args, m.ProjectID)
	}
	if m.AutomationID != "" {
		conditions = append(conditions, "w.automation_id=?")
		args = append(args, m.AutomationID)
	}
	if m.VersionID != "" {
		conditions = append(conditions, "w.origin_version_id=?")
		args = append(args, m.VersionID)
	}
	if m.ID != "" {
		conditions = append(conditions, "w.id=?")
		args = append(args, m.ID)
	}
	if m.Key != "" {
		conditions = append(conditions, "w.work_item_key=?")
		args = append(args, m.Key)
	}
	if m.Kind != "" {
		conditions = append(conditions, "w.kind=?")
		args = append(args, m.Kind)
	}
	if m.Status != "" {
		conditions = append(conditions, "w.status=?")
		args = append(args, m.Status)
	}
	var n int
	err := f.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM automation_work_items w WHERE "+strings.Join(conditions, " AND "), args...).Scan(&n)
	return n, err
}
func (f *fixtures) CountMatchingPositions(ctx context.Context, m storefixture.PositionMatch) (int, error) {
	conditions := []string{"1=1"}
	args := []any{}
	if m.ProjectID != "" {
		conditions = append(conditions, "p.project_id=?")
		args = append(args, m.ProjectID)
	}
	if m.AutomationID != "" {
		conditions = append(conditions, "p.automation_id=?")
		args = append(args, m.AutomationID)
	}
	if m.VersionID != "" {
		conditions = append(conditions, "p.version_id=?")
		args = append(args, m.VersionID)
	}
	if m.WorkItemID != "" {
		conditions = append(conditions, "p.work_item_id=?")
		args = append(args, m.WorkItemID)
	}
	if m.NodeID != "" {
		conditions = append(conditions, "p.node_id=?")
		args = append(args, m.NodeID)
	}
	if m.NodeKey != "" {
		conditions = append(conditions, "n.node_key=?")
		args = append(args, m.NodeKey)
	}
	if m.State != "" {
		conditions = append(conditions, "p.state=?")
		args = append(args, m.State)
	}
	if m.WorkItemKey != "" {
		conditions = append(conditions, "w.work_item_key=?")
		args = append(args, m.WorkItemKey)
	}
	var n int
	err := f.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM automation_work_item_positions p LEFT JOIN automation_nodes n ON n.id=p.node_id LEFT JOIN automation_work_items w ON w.id=p.work_item_id WHERE "+strings.Join(conditions, " AND "), args...).Scan(&n)
	return n, err
}
func (f *fixtures) CountMatchingTransitions(ctx context.Context, m storefixture.TransitionMatch) (int, error) {
	conditions := []string{"1=1"}
	args := []any{}
	if m.ProjectID != "" {
		conditions = append(conditions, "t.project_id=?")
		args = append(args, m.ProjectID)
	}
	if m.AutomationID != "" {
		conditions = append(conditions, "t.automation_id=?")
		args = append(args, m.AutomationID)
	}
	if m.VersionID != "" {
		conditions = append(conditions, "t.version_id=?")
		args = append(args, m.VersionID)
	}
	if m.WorkItemID != "" {
		conditions = append(conditions, "t.work_item_id=?")
		args = append(args, m.WorkItemID)
	}
	if m.EdgeKey != "" {
		conditions = append(conditions, "e.edge_key=?")
		args = append(args, m.EdgeKey)
	}
	if m.State != "" {
		conditions = append(conditions, "t.state=?")
		args = append(args, m.State)
	}
	var n int
	err := f.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM automation_transitions t LEFT JOIN automation_edges e ON e.id=t.edge_id WHERE "+strings.Join(conditions, " AND "), args...).Scan(&n)
	return n, err
}

func (f *fixtures) AutomationActivityStatus(ctx context.Context, key string) (models.AutomationActivityStatus, error) {
	var state models.AutomationActivityStatus
	err := f.db.QueryRowContext(ctx, "SELECT status FROM automation_activities WHERE activity_key=?", key).Scan(&state)
	return state, err
}
func (f *fixtures) SeedAutomationPosition(ctx context.Context, p models.AutomationWorkItemPosition) error {
	_, err := f.db.ExecContext(ctx, `INSERT OR IGNORE INTO automation_work_item_positions(work_item_id,project_id,automation_id,version_id,node_id,state,entered_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, p.WorkItemID, p.ProjectID, p.AutomationID, p.VersionID, p.NodeID, p.State, p.EnteredAt, p.UpdatedAt)
	return err
}
func (f *fixtures) SeedAutomationWorkItem(ctx context.Context, w models.AutomationWorkItem) error {
	_, err := f.db.ExecContext(ctx, `INSERT INTO automation_work_items(id,project_id,automation_id,origin_version_id,work_item_key,kind,title,status) VALUES(?,?,?,?,?,?,?,?)`, w.ID, w.ProjectID, w.AutomationID, w.OriginVersionID, w.WorkItemKey, w.Kind, w.Title, w.Status)
	return err
}

// SetLiveAutomationActivity chooses the authoritative activity for a task (if supplied) or its work item.
func (f *fixtures) SetLiveAutomationActivity(ctx context.Context, id, taskID string) error {
	_, err := f.db.ExecContext(ctx, `INSERT INTO automation_live_activity_states(project_id,automation_id,version_id,node_id,state_key,activity_id,invocation_id,work_item_id,activity_status,completed_at,activity_rowid) SELECT project_id,automation_id,version_id,node_id,CASE WHEN ?='' THEN 'work:'||work_item_id ELSE 'task:'||? END,id,invocation_id,work_item_id,status,completed_at,rowid FROM automation_activities WHERE id=?`, taskID, taskID, id)
	return err
}

func (f *fixtures) AutomationActivityResources(ctx context.Context, m storefixture.ActivityMatch) ([]models.AutomationActivityResource, error) {
	conditions := []string{"1=1"}
	args := []any{}
	if m.ProjectID != "" {
		conditions = append(conditions, "a.project_id=?")
		args = append(args, m.ProjectID)
	}
	if m.AutomationID != "" {
		conditions = append(conditions, "a.automation_id=?")
		args = append(args, m.AutomationID)
	}
	if m.VersionID != "" {
		conditions = append(conditions, "a.version_id=?")
		args = append(args, m.VersionID)
	}
	if m.Key != "" {
		conditions = append(conditions, "a.activity_key=?")
		args = append(args, m.Key)
	}
	if m.ID != "" {
		conditions = append(conditions, "a.id=?")
		args = append(args, m.ID)
	}
	if m.Type != "" {
		conditions = append(conditions, "a.activity_type=?")
		args = append(args, m.Type)
	}
	if m.Status != "" {
		conditions = append(conditions, "a.status=?")
		args = append(args, m.Status)
	}
	if m.WorkItemID != "" {
		conditions = append(conditions, "a.work_item_id=?")
		args = append(args, m.WorkItemID)
	}
	if m.HasWorkItem {
		conditions = append(conditions, "a.work_item_id IS NOT NULL")
	}
	rows, err := f.db.QueryContext(ctx, `SELECT r.id,r.activity_id,r.resource_type,r.resource_id,r.relation,r.created_at FROM automation_activity_resources r JOIN automation_activities a ON a.id=r.activity_id WHERE `+strings.Join(conditions, " AND "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.AutomationActivityResource
	for rows.Next() {
		var r models.AutomationActivityResource
		if err := rows.Scan(&r.ID, &r.ActivityID, &r.ResourceType, &r.ResourceID, &r.Relation, &r.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

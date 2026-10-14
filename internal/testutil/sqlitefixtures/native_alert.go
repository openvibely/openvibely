package sqlitefixtures

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"strings"
)

func (f *fixtures) NativeAlertProjection(ctx context.Context, project, automation, version, alert string) (storefixture.NativeAlertProjection, error) {
	var p storefixture.NativeAlertProjection
	err := f.db.QueryRowContext(ctx, `SELECT id,kind FROM automation_work_items WHERE project_id=? AND automation_id=? AND origin_version_id=? AND work_item_key=?`, project, automation, version, "alert:"+alert).Scan(&p.WorkItemID, &p.Kind)
	if err != nil {
		return p, err
	}
	err = f.db.QueryRowContext(ctx, `SELECT id,node_id,status FROM automation_activities WHERE project_id=? AND automation_id=? AND version_id=? AND work_item_id=? AND activity_key=? AND activity_type='create_notification'`, project, automation, version, p.WorkItemID, "alert:"+alert+":create").Scan(&p.ActivityID, &p.ActivityNodeID, &p.ActivityStatus)
	if err != nil {
		return p, err
	}
	err = f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_activity_resources WHERE activity_id=? AND resource_type='alert' AND resource_id=?`, p.ActivityID, alert).Scan(&p.ResourceCount)
	if err != nil {
		return p, err
	}
	err = f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_activities WHERE project_id=? AND automation_id=? AND version_id=? AND activity_key=?`, project, automation, version, "alert:"+alert+":create").Scan(&p.ActivityCount)
	if err != nil {
		return p, err
	}
	rows, err := f.db.QueryContext(ctx, `SELECT event_key,COALESCE(activity_id,''),state,COALESCE(from_node_id,''),to_node_id FROM automation_transitions WHERE project_id=? AND automation_id=? AND version_id=? AND work_item_id=? AND event_key IN (?,?,?) ORDER BY event_key`, project, automation, version, p.WorkItemID, "alert:"+alert+":created:notification", "alert:"+alert+":created:waiting", "alert:"+alert+":decision:approved")
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var tr models.AutomationTransition
		if err := rows.Scan(&tr.EventKey, &tr.ActivityID, &tr.State, &tr.FromNodeID, &tr.ToNodeID); err != nil {
			return p, err
		}
		p.Transitions = append(p.Transitions, tr)
	}
	return p, rows.Err()
}
func (f *fixtures) NativeMailboxKeys(ctx context.Context, project, automation, alert string) (storefixture.NativeMailboxKeys, error) {
	var k storefixture.NativeMailboxKeys
	err := f.db.QueryRowContext(ctx, `SELECT producer_node_key,action_node_key,gate_node_key,mailbox_node_key FROM automation_artifact_mailbox_owners WHERE project_id=? AND automation_id=? AND artifact_type='alert' AND artifact_id=?`, project, automation, alert).Scan(&k.Producer, &k.Action, &k.Gate, &k.Mailbox)
	return k, err
}
func (f *fixtures) CountTransitionEvents(ctx context.Context, project, automation string, keys []string) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	args := []any{project, automation}
	for _, k := range keys {
		args = append(args, k)
	}
	var n int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_transitions WHERE project_id=? AND automation_id=? AND event_key IN (`+strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")+`)`, args...).Scan(&n)
	return n, err
}

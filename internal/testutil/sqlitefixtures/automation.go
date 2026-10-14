package sqlitefixtures

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
)

func (f *fixtures) SeedAutomation(ctx context.Context, a models.Automation) error {
	_, err := f.db.ExecContext(ctx, `INSERT INTO automations (id,project_id,stable_key,name,description,automation_type,lifecycle_state,published_version_id) VALUES (?,?,?,?,?,?,?,?)`, a.ID, a.ProjectID, a.StableKey, a.Name, a.Description, a.AutomationType, a.LifecycleState, a.PublishedVersionID)
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

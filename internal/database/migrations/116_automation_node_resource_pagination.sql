-- +goose Up
CREATE INDEX idx_automation_activities_node_resources
ON automation_activities(project_id, automation_id, version_id, node_id, started_at DESC, id DESC);

CREATE INDEX idx_automation_activity_resources_activity_page
ON automation_activity_resources(activity_id, id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_automation_activity_resources_activity_page;
DROP INDEX IF EXISTS idx_automation_activities_node_resources;

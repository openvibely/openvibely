-- +goose Up
CREATE INDEX idx_task_pull_requests_updated_at_task_id
ON task_pull_requests(updated_at, task_id);

CREATE INDEX idx_automation_activity_resources_type_resource_activity
ON automation_activity_resources(resource_type, resource_id, activity_id);

CREATE INDEX idx_automation_activity_resources_activity_type
ON automation_activity_resources(activity_id, resource_type);

-- +goose Down
DROP INDEX IF EXISTS idx_automation_activity_resources_activity_type;
DROP INDEX IF EXISTS idx_automation_activity_resources_type_resource_activity;
DROP INDEX IF EXISTS idx_task_pull_requests_updated_at_task_id;

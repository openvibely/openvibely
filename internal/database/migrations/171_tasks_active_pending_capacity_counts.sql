-- +goose Up
CREATE INDEX idx_tasks_active_pending_capacity_counts ON tasks(category, status, project_id);

-- +goose Down
DROP INDEX IF EXISTS idx_tasks_active_pending_capacity_counts;

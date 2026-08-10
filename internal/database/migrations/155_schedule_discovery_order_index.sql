-- +goose Up
CREATE INDEX IF NOT EXISTS idx_schedules_discovery_order
ON schedules((next_run IS NULL), next_run ASC, created_at DESC, id ASC, task_id);

-- +goose Down
DROP INDEX IF EXISTS idx_schedules_discovery_order;

-- +goose Up
-- The discovery page always excludes internal chat rows and orders visible tasks
-- by recency with a stable ID tiebreaker. Keep that access path separate from
-- title-filtered CASE ordering and the compact projection itself.
CREATE INDEX IF NOT EXISTS idx_tasks_discovery_order
ON tasks(project_id, updated_at DESC, id ASC)
WHERE category != 'chat';

-- +goose Down
DROP INDEX IF EXISTS idx_tasks_discovery_order;

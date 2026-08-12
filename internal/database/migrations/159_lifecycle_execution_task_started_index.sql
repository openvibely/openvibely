-- +goose Up
-- Task lifecycle activity lists filter by task and display newest rows first.
-- Include id as a deterministic tie-breaker so SQLite can satisfy the full
-- ORDER BY without building a temporary sort.
CREATE INDEX idx_lifecycle_executions_task_started
    ON lifecycle_executions(task_id, started_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_lifecycle_executions_task_started;

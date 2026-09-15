-- +goose Up
CREATE INDEX IF NOT EXISTS idx_executions_task_terminal_at
    ON executions(task_id, COALESCE(completed_at, started_at) DESC)
    WHERE status IN ('completed','failed','cancelled');

-- +goose Down
DROP INDEX IF EXISTS idx_executions_task_terminal_at;

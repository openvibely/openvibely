-- +goose Up
-- Deleting a task cascades through lifecycle_executions. SQLite checks the
-- self-referencing parent_execution_id foreign key for every deleted lifecycle
-- row, so this lookup must be indexed to avoid repeated full-table scans.
CREATE INDEX idx_lifecycle_executions_parent_execution_id
    ON lifecycle_executions(parent_execution_id)
    WHERE parent_execution_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_lifecycle_executions_parent_execution_id;

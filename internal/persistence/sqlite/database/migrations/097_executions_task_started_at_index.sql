-- +goose Up
-- Composite index on (task_id, started_at) so ORDER BY started_at queries scoped to a
-- single task_id can use the index for sorting rather than materialising a temporary B-tree.
-- This benefits ListByTask, ListByTaskChronological, GetLatestNonEmptyDiffOutput, and the
-- thread-window pagination queries that all filter by task_id and sort by started_at.
CREATE INDEX IF NOT EXISTS idx_executions_task_started_at ON executions(task_id, started_at);

-- +goose Down
DROP INDEX IF EXISTS idx_executions_task_started_at;

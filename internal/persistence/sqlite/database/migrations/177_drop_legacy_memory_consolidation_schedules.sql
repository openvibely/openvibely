-- +goose Up
-- Older databases can retain this pre-agent memory scheduler after migration 83.
-- Its last_run_id foreign key references memory_consolidation_runs, which migration
-- 83 removed, causing every project deletion with a legacy schedule row to fail.
DROP TABLE IF EXISTS memory_consolidation_schedules;

-- +goose Down
-- Legacy memory schedules are intentionally not restored. Memory consolidation is
-- represented by ordinary tasks and schedules managed by the Memory Curator.
SELECT 1;

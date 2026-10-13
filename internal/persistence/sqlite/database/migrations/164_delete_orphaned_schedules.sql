-- +goose Up
-- Historical databases can contain schedules whose task rows were deleted while
-- SQLite foreign-key enforcement was unavailable. Remove those rows so the
-- scheduler does not retry permanently orphaned due schedules.
DELETE FROM schedules
WHERE NOT EXISTS (
    SELECT 1 FROM tasks WHERE tasks.id = schedules.task_id
);

-- +goose Down
-- Irreversible data cleanup.

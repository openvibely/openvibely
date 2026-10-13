-- +goose Up
ALTER TABLE tasks ADD COLUMN completed_at DATETIME;

-- Backfill: tasks already in the completed category get completed_at = updated_at
-- (best available approximation for existing rows)
UPDATE tasks SET completed_at = updated_at WHERE category = 'completed';

-- +goose Down
ALTER TABLE tasks DROP COLUMN completed_at;

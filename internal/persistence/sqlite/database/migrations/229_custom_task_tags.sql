-- +goose Up
-- Replace only the constrained column, preserving tasks, references and indexes.
ALTER TABLE tasks ADD COLUMN custom_tag TEXT NOT NULL DEFAULT '';
UPDATE tasks SET custom_tag = tag;
ALTER TABLE tasks DROP COLUMN tag;
ALTER TABLE tasks RENAME COLUMN custom_tag TO tag;

-- +goose Down
-- Keep custom values when rolling back; reinstating the enum would lose user data.
SELECT 1;

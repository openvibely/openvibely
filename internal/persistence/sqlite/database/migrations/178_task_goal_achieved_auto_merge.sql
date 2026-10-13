-- +goose Up
ALTER TABLE tasks ADD COLUMN auto_merge_on_goal_achieved INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE tasks DROP COLUMN auto_merge_on_goal_achieved;

-- +goose Up
ALTER TABLE tasks ADD COLUMN author_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN author_name TEXT NOT NULL DEFAULT '';
ALTER TABLE executions ADD COLUMN author_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE executions ADD COLUMN author_name TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_inputs ADD COLUMN author_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_inputs ADD COLUMN author_name TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE thread_inputs DROP COLUMN author_name;
ALTER TABLE thread_inputs DROP COLUMN author_user_id;
ALTER TABLE executions DROP COLUMN author_name;
ALTER TABLE executions DROP COLUMN author_user_id;
ALTER TABLE tasks DROP COLUMN author_name;
ALTER TABLE tasks DROP COLUMN author_user_id;

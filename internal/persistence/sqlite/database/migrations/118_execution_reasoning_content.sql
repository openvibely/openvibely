-- +goose Up
ALTER TABLE executions ADD COLUMN reasoning_content TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE executions DROP COLUMN reasoning_content;

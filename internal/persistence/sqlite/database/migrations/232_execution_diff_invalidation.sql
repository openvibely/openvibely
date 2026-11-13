-- +goose Up
ALTER TABLE executions ADD COLUMN diff_output_invalidated INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE executions DROP COLUMN diff_output_invalidated;

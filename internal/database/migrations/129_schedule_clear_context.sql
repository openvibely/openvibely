-- +goose Up
ALTER TABLE schedules ADD COLUMN clear_context_on_start INTEGER NOT NULL DEFAULT 0;
ALTER TABLE executions ADD COLUMN starts_new_context INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE executions DROP COLUMN starts_new_context;
ALTER TABLE schedules DROP COLUMN clear_context_on_start;

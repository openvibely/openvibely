-- +goose Up
ALTER TABLE thread_inputs ADD COLUMN origin_agent TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE thread_inputs DROP COLUMN origin_agent;

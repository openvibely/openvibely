-- +goose Up
ALTER TABLE thread_inputs ADD COLUMN edit_hold_token TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE thread_inputs DROP COLUMN edit_hold_token;

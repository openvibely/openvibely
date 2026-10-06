-- +goose Up
ALTER TABLE thread_inputs ADD COLUMN edit_hold INTEGER NOT NULL DEFAULT 0 CHECK (edit_hold IN (0, 1));
ALTER TABLE thread_inputs ADD COLUMN edit_hold_until TEXT;
ALTER TABLE thread_inputs ADD COLUMN edit_hold_token TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE thread_inputs DROP COLUMN edit_hold_token;
ALTER TABLE thread_inputs DROP COLUMN edit_hold_until;
ALTER TABLE thread_inputs DROP COLUMN edit_hold;

-- +goose Up
ALTER TABLE thread_inputs ADD COLUMN edit_hold_until TEXT;
UPDATE thread_inputs SET edit_hold_until = datetime('now', '+120 seconds') WHERE edit_hold = 1;

-- +goose Down
ALTER TABLE thread_inputs DROP COLUMN edit_hold_until;

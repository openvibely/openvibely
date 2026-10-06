-- +goose Up
ALTER TABLE thread_inputs ADD COLUMN edit_hold INTEGER NOT NULL DEFAULT 0 CHECK (edit_hold IN (0, 1));

-- +goose Down
ALTER TABLE thread_inputs DROP COLUMN edit_hold;

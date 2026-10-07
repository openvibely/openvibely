-- +goose Up
ALTER TABLE thread_inputs ADD COLUMN model_selection_snapshot INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE thread_inputs DROP COLUMN model_selection_snapshot;

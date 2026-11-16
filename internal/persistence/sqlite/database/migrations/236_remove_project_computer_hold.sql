-- +goose Up
ALTER TABLE project_computers DROP COLUMN held;

-- +goose Down
ALTER TABLE project_computers ADD COLUMN held INTEGER NOT NULL DEFAULT 0;

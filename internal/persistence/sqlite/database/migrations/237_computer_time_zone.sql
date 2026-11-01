-- +goose Up
ALTER TABLE project_computers ADD COLUMN time_zone TEXT NOT NULL DEFAULT 'UTC';
-- +goose Down
ALTER TABLE project_computers DROP COLUMN time_zone;

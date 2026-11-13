-- +goose Up
ALTER TABLE project_computers ADD COLUMN idle_minutes INTEGER NOT NULL DEFAULT 30 CHECK(idle_minutes BETWEEN 0 AND 10080);
-- +goose Down
ALTER TABLE project_computers DROP COLUMN idle_minutes;

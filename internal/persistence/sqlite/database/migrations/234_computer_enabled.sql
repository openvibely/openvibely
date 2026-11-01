-- +goose Up
ALTER TABLE project_computers ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1));
-- +goose Down
ALTER TABLE project_computers DROP COLUMN enabled;

-- +goose Up
ALTER TABLE project_computers ADD COLUMN takeover_pending INTEGER NOT NULL DEFAULT 0;
ALTER TABLE task_computer_sessions ADD COLUMN takeover_pending INTEGER NOT NULL DEFAULT 0;
-- +goose Down
ALTER TABLE task_computer_sessions DROP COLUMN takeover_pending;
ALTER TABLE project_computers DROP COLUMN takeover_pending;

-- +goose Up
ALTER TABLE project_computers DROP COLUMN held;
ALTER TABLE project_computers DROP COLUMN takeover_pending;
ALTER TABLE task_computer_sessions DROP COLUMN takeover_pending;
ALTER TABLE task_computer_sessions DROP COLUMN resume_after_takeover;

-- +goose Down
ALTER TABLE project_computers ADD COLUMN held INTEGER NOT NULL DEFAULT 0;
ALTER TABLE project_computers ADD COLUMN takeover_pending INTEGER NOT NULL DEFAULT 0;
ALTER TABLE task_computer_sessions ADD COLUMN takeover_pending INTEGER NOT NULL DEFAULT 0;
ALTER TABLE task_computer_sessions ADD COLUMN resume_after_takeover INTEGER NOT NULL DEFAULT 0;

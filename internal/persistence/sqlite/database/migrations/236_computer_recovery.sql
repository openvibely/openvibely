-- +goose Up
ALTER TABLE task_computer_sessions ADD COLUMN resume_after_takeover INTEGER NOT NULL DEFAULT 0;
CREATE TABLE computer_chrome_profile_imports (
 grant_id TEXT NOT NULL REFERENCES computer_chrome_grants(id) ON DELETE CASCADE,
 task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
 profile TEXT NOT NULL,
 imported INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(grant_id,task_id,profile)
);
-- +goose Down
DROP TABLE computer_chrome_profile_imports;
ALTER TABLE task_computer_sessions DROP COLUMN resume_after_takeover;

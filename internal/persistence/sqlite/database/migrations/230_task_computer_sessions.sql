-- +goose Up
CREATE TABLE task_computer_sessions (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL REFERENCES project_computers(project_id) ON DELETE CASCADE,
 handoff_kind TEXT NOT NULL DEFAULT '',
 handoff_url TEXT NOT NULL DEFAULT '',
 held INTEGER NOT NULL DEFAULT 0,
 owner TEXT NOT NULL DEFAULT '',
 lease_until INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX task_computer_sessions_project ON task_computer_sessions(project_id);
-- +goose Down
DROP TABLE task_computer_sessions;

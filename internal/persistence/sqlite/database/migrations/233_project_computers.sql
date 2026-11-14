-- +goose Up
CREATE TABLE project_computers (
 project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
 provider TEXT NOT NULL CHECK(provider IN ('local','hosted')),
 cleanup_required INTEGER NOT NULL DEFAULT 0 CHECK(cleanup_required IN (0,1)),
 held INTEGER NOT NULL DEFAULT 0,
 owner TEXT NOT NULL DEFAULT '',
 lease_until INTEGER NOT NULL DEFAULT 0,
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
 time_zone TEXT NOT NULL DEFAULT 'UTC',
 takeover_pending INTEGER NOT NULL DEFAULT 0,
 idle_minutes INTEGER NOT NULL DEFAULT 30 CHECK(idle_minutes BETWEEN 0 AND 10080)
);

CREATE TABLE task_computer_sessions (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL REFERENCES project_computers(project_id) ON DELETE CASCADE,
 handoff_kind TEXT NOT NULL DEFAULT '',
 handoff_url TEXT NOT NULL DEFAULT '',
 held INTEGER NOT NULL DEFAULT 0,
 owner TEXT NOT NULL DEFAULT '',
 lease_until INTEGER NOT NULL DEFAULT 0,
 handoff_reason TEXT NOT NULL DEFAULT '',
 handoff_expected_text TEXT NOT NULL DEFAULT '',
 handoff_alert_id TEXT NOT NULL DEFAULT '',
 tool_target TEXT NOT NULL DEFAULT 'worktree' CHECK(tool_target IN ('worktree','computer')),
 resume_after_takeover INTEGER NOT NULL DEFAULT 0,
 takeover_pending INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX task_computer_sessions_project ON task_computer_sessions(project_id);

CREATE TABLE computer_chrome_grants (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES project_computers(project_id) ON DELETE CASCADE,
 -- For project grants this is provenance, not ownership.
 task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL,
 scope TEXT NOT NULL CHECK(scope IN ('task','project')),
 selections TEXT NOT NULL,
 expires_at INTEGER NOT NULL,
 revoked INTEGER NOT NULL DEFAULT 0,
 one_time INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX computer_chrome_grants_project ON computer_chrome_grants(project_id);
-- Task grants end with their task; project grants survive and lose only provenance.
-- +goose StatementBegin
CREATE TRIGGER computer_chrome_task_grant_cleanup BEFORE DELETE ON tasks
BEGIN
 DELETE FROM computer_chrome_grants WHERE task_id=OLD.id AND scope='task';
END;
-- +goose StatementEnd
CREATE TABLE computer_chrome_installations (
 grant_id TEXT NOT NULL REFERENCES computer_chrome_grants(id) ON DELETE CASCADE,
 task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
 PRIMARY KEY(grant_id,task_id)
);

CREATE TABLE computer_workflows (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 name TEXT NOT NULL,
 instructions TEXT NOT NULL
);
CREATE INDEX computer_workflows_project ON computer_workflows(project_id);

CREATE TABLE computer_chrome_profile_imports (
 grant_id TEXT NOT NULL REFERENCES computer_chrome_grants(id) ON DELETE CASCADE,
 task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
 profile TEXT NOT NULL,
 imported INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(grant_id,task_id,profile)
);

-- +goose Down
DROP TRIGGER computer_chrome_task_grant_cleanup;
DROP TABLE computer_chrome_profile_imports;
DROP TABLE computer_workflows;
DROP TABLE computer_chrome_installations;
DROP TABLE computer_chrome_grants;
DROP TABLE task_computer_sessions;
DROP TABLE project_computers;

-- +goose Up
CREATE TABLE computer_chrome_grants (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES project_computers(project_id) ON DELETE CASCADE,
 task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
 scope TEXT NOT NULL CHECK(scope IN ('task','project')),
 selections TEXT NOT NULL,
 expires_at INTEGER NOT NULL,
 revoked INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX computer_chrome_grants_project ON computer_chrome_grants(project_id);
CREATE TABLE computer_chrome_installations (
 grant_id TEXT NOT NULL REFERENCES computer_chrome_grants(id) ON DELETE CASCADE,
 task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
 PRIMARY KEY(grant_id,task_id)
);
-- +goose Down
DROP TABLE computer_chrome_installations;
DROP TABLE computer_chrome_grants;

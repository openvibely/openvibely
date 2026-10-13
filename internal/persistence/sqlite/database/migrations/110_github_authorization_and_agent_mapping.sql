-- +goose Up
CREATE TABLE github_authorized_actors (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    github_user_id INTEGER,
    github_login TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    permission TEXT NOT NULL DEFAULT 'triage',
    added_at DATETIME NOT NULL DEFAULT (datetime('now')),
    added_by TEXT NOT NULL DEFAULT 'web',
    CHECK (trim(github_login) != ''),
    CHECK (permission IN ('triage', 'approve', 'admin'))
);

CREATE UNIQUE INDEX idx_github_authorized_actors_login
    ON github_authorized_actors(lower(github_login));
CREATE UNIQUE INDEX idx_github_authorized_actors_user_id
    ON github_authorized_actors(github_user_id)
    WHERE github_user_id IS NOT NULL;

CREATE TABLE github_project_inboxes (
    project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    github_user_id INTEGER,
    github_login TEXT NOT NULL,
    agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
    enabled BOOLEAN NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    CHECK (trim(github_login) != '')
);

CREATE INDEX idx_github_project_inboxes_login
    ON github_project_inboxes(lower(github_login));
CREATE INDEX idx_github_project_inboxes_agent
    ON github_project_inboxes(agent_id);
CREATE INDEX idx_github_project_inboxes_enabled
    ON github_project_inboxes(project_id, enabled);

-- +goose Down
DROP INDEX IF EXISTS idx_github_project_inboxes_enabled;
DROP INDEX IF EXISTS idx_github_project_inboxes_agent;
DROP INDEX IF EXISTS idx_github_project_inboxes_login;
DROP TABLE IF EXISTS github_project_inboxes;

DROP INDEX IF EXISTS idx_github_authorized_actors_user_id;
DROP INDEX IF EXISTS idx_github_authorized_actors_login;
DROP TABLE IF EXISTS github_authorized_actors;

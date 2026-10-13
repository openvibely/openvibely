-- +goose Up
CREATE TABLE IF NOT EXISTS email_sender_projects (
    email_address TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    updated_at TIMESTAMP NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_email_sender_projects_project ON email_sender_projects(project_id);

-- +goose Down
DROP INDEX IF EXISTS idx_email_sender_projects_project;
DROP TABLE IF EXISTS email_sender_projects;

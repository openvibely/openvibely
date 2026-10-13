-- +goose Up
CREATE TABLE IF NOT EXISTS discord_user_projects (
    discord_user_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    updated_at TIMESTAMP NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_discord_user_projects_project ON discord_user_projects(project_id);

-- +goose Down
DROP INDEX IF EXISTS idx_discord_user_projects_project;
DROP TABLE IF EXISTS discord_user_projects;

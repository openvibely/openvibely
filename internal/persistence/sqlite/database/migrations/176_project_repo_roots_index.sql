-- +goose Up
CREATE INDEX IF NOT EXISTS idx_projects_repo_roots ON projects(repo_path, id);

-- +goose Down
DROP INDEX IF EXISTS idx_projects_repo_roots;

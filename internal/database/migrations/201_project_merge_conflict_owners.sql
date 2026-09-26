-- +goose Up
CREATE TABLE project_merge_conflict_owners (
    project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE IF EXISTS project_merge_conflict_owners;

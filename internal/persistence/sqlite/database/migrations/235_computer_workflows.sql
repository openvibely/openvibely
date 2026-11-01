-- +goose Up
CREATE TABLE computer_workflows (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 name TEXT NOT NULL,
 instructions TEXT NOT NULL
);
CREATE INDEX computer_workflows_project ON computer_workflows(project_id);
-- +goose Down
DROP TABLE computer_workflows;

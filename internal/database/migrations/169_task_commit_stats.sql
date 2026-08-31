-- +goose Up
-- +goose StatementBegin
CREATE TABLE task_commit_stats (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    execution_id TEXT REFERENCES executions(id) ON DELETE SET NULL,
    commit_sha TEXT NOT NULL,
    short_sha TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    author TEXT NOT NULL DEFAULT '',
    produced_at DATETIME NOT NULL,
    insertions INTEGER NOT NULL DEFAULT 0,
    deletions INTEGER NOT NULL DEFAULT 0,
    files_changed INTEGER NOT NULL DEFAULT 0,
    changed_files_json TEXT NOT NULL DEFAULT '[]',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX idx_task_commit_stats_task_commit ON task_commit_stats(task_id, commit_sha);
CREATE INDEX idx_task_commit_stats_project_produced ON task_commit_stats(project_id, produced_at);
CREATE INDEX idx_task_commit_stats_execution_id ON task_commit_stats(execution_id) WHERE execution_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_task_commit_stats_execution_id;
DROP INDEX IF EXISTS idx_task_commit_stats_project_produced;
DROP INDEX IF EXISTS idx_task_commit_stats_task_commit;
DROP TABLE IF EXISTS task_commit_stats;
-- +goose StatementEnd

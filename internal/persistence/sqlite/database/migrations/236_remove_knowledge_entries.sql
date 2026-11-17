-- +goose Up
-- Dropping the table also removes its indexes and permanently deletes its rows.
DROP TABLE knowledge_entries;

-- +goose Down
-- Rollback restores the schema only; extracted knowledge cannot be recovered.
CREATE TABLE knowledge_entries (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    topic TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    source_ref TEXT NOT NULL DEFAULT '',
    tags TEXT NOT NULL DEFAULT '[]',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_knowledge_project ON knowledge_entries(project_id);
CREATE INDEX idx_knowledge_topic ON knowledge_entries(topic);

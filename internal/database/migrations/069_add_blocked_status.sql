-- +goose Up
-- Add 'blocked' to allowed task status values.
-- SQLite requires table recreation to alter CHECK constraints.

PRAGMA foreign_keys=OFF;

CREATE TABLE tasks_new (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT 'active'
                 CHECK (category IN ('active', 'freezer', 'completed', 'backlog', 'scheduled', 'chat')),
    priority INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'queued', 'running', 'completed', 'failed', 'cancelled', 'blocked')),
    prompt TEXT NOT NULL DEFAULT '',
    agent_id TEXT REFERENCES agent_configs(id),
    tag TEXT NOT NULL DEFAULT ''
                 CHECK (tag IN ('', 'feature', 'bug')),
    display_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    parent_task_id TEXT REFERENCES tasks_new(id) ON DELETE SET NULL,
    chain_config TEXT NOT NULL DEFAULT '{}',
    execution_cost_cents INTEGER NOT NULL DEFAULT 0,
    execution_duration_ms INTEGER NOT NULL DEFAULT 0,
    worktree_path TEXT NOT NULL DEFAULT '',
    worktree_branch TEXT NOT NULL DEFAULT '',
    auto_merge INTEGER NOT NULL DEFAULT 0,
    merge_target_branch TEXT NOT NULL DEFAULT '',
    merge_status TEXT NOT NULL DEFAULT ''
        CHECK (merge_status IN ('', 'pending', 'merged', 'failed', 'conflict')),
    created_via TEXT NOT NULL DEFAULT '',
    telegram_chat_id INTEGER NOT NULL DEFAULT 0,
    agent_definition_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
    base_branch TEXT NOT NULL DEFAULT '',
    base_commit_sha TEXT NOT NULL DEFAULT '',
    lineage_depth INTEGER NOT NULL DEFAULT 0
);

INSERT INTO tasks_new SELECT * FROM tasks;

DROP TABLE tasks;
ALTER TABLE tasks_new RENAME TO tasks;

CREATE INDEX idx_tasks_project_id ON tasks(project_id);
CREATE INDEX idx_tasks_category ON tasks(category);
CREATE INDEX idx_tasks_status ON tasks(status);
CREATE UNIQUE INDEX idx_tasks_project_title ON tasks(project_id, title);
CREATE INDEX idx_tasks_display_order ON tasks(project_id, category, display_order);
CREATE INDEX idx_tasks_parent_task_id ON tasks(parent_task_id);

PRAGMA foreign_keys=ON;

-- +goose Down
-- Remove 'blocked' status: revert any blocked tasks to pending, then recreate table.
UPDATE tasks SET status = 'pending' WHERE status = 'blocked';

PRAGMA foreign_keys=OFF;

CREATE TABLE tasks_old (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT 'active'
                 CHECK (category IN ('active', 'freezer', 'completed', 'backlog', 'scheduled', 'chat')),
    priority INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'queued', 'running', 'completed', 'failed', 'cancelled')),
    prompt TEXT NOT NULL DEFAULT '',
    agent_id TEXT REFERENCES agent_configs(id),
    tag TEXT NOT NULL DEFAULT ''
                 CHECK (tag IN ('', 'feature', 'bug')),
    display_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    parent_task_id TEXT REFERENCES tasks_old(id) ON DELETE SET NULL,
    chain_config TEXT NOT NULL DEFAULT '{}',
    execution_cost_cents INTEGER NOT NULL DEFAULT 0,
    execution_duration_ms INTEGER NOT NULL DEFAULT 0,
    worktree_path TEXT NOT NULL DEFAULT '',
    worktree_branch TEXT NOT NULL DEFAULT '',
    auto_merge INTEGER NOT NULL DEFAULT 0,
    merge_target_branch TEXT NOT NULL DEFAULT '',
    merge_status TEXT NOT NULL DEFAULT ''
        CHECK (merge_status IN ('', 'pending', 'merged', 'failed', 'conflict')),
    created_via TEXT NOT NULL DEFAULT '',
    telegram_chat_id INTEGER NOT NULL DEFAULT 0,
    agent_definition_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
    base_branch TEXT NOT NULL DEFAULT '',
    base_commit_sha TEXT NOT NULL DEFAULT '',
    lineage_depth INTEGER NOT NULL DEFAULT 0
);

INSERT INTO tasks_old SELECT * FROM tasks;

DROP TABLE tasks;
ALTER TABLE tasks_old RENAME TO tasks;

CREATE INDEX idx_tasks_project_id ON tasks(project_id);
CREATE INDEX idx_tasks_category ON tasks(category);
CREATE INDEX idx_tasks_status ON tasks(status);
CREATE UNIQUE INDEX idx_tasks_project_title ON tasks(project_id, title);
CREATE INDEX idx_tasks_display_order ON tasks(project_id, category, display_order);
CREATE INDEX idx_tasks_parent_task_id ON tasks(parent_task_id);

PRAGMA foreign_keys=ON;

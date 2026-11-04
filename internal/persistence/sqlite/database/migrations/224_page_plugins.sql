-- +goose Up
-- Installed page-plugin packages are global and immutable per (plugin_id, version).
CREATE TABLE page_plugin_packages (
    id            TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    plugin_id     TEXT NOT NULL,
    version       TEXT NOT NULL,
    name          TEXT NOT NULL,
    content_hash  TEXT NOT NULL,
    manifest_json TEXT NOT NULL,
    install_path  TEXT NOT NULL,
    installed_by  TEXT NOT NULL DEFAULT '',
    installed_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE (plugin_id, version)
);

-- A page-plugin instance belongs to exactly one project. Config and state are per instance.
CREATE TABLE page_plugin_instances (
    id              TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    package_id      TEXT NOT NULL REFERENCES page_plugin_packages(id) ON DELETE RESTRICT,
    page_id         TEXT NOT NULL,
    title           TEXT NOT NULL,
    enabled         INTEGER NOT NULL DEFAULT 0,
    config_json     TEXT NOT NULL DEFAULT '{}',
    state_json      TEXT NOT NULL DEFAULT '{}',
    approved_hash   TEXT NOT NULL DEFAULT '',
    approved_by     TEXT NOT NULL DEFAULT '',
    approved_at     DATETIME,
    created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE (project_id, package_id, page_id)
);
CREATE INDEX idx_page_plugin_instances_project ON page_plugin_instances(project_id, enabled);

-- Side-chat conversation backing task per instance; removed with the instance.
CREATE TABLE page_plugin_conversations (
    instance_id TEXT PRIMARY KEY REFERENCES page_plugin_instances(id) ON DELETE CASCADE,
    task_id     TEXT NOT NULL UNIQUE REFERENCES tasks(id) ON DELETE CASCADE,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- Per-turn page context snapshot and provenance for side-chat executions.
CREATE TABLE page_plugin_turn_contexts (
    execution_id  TEXT PRIMARY KEY REFERENCES executions(id) ON DELETE CASCADE,
    instance_id   TEXT NOT NULL REFERENCES page_plugin_instances(id) ON DELETE CASCADE,
    snapshot_json TEXT NOT NULL,
    created_at    DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- +goose Down
DROP TABLE page_plugin_turn_contexts;
DROP TABLE page_plugin_conversations;
DROP INDEX idx_page_plugin_instances_project;
DROP TABLE page_plugin_instances;
DROP TABLE page_plugin_packages;

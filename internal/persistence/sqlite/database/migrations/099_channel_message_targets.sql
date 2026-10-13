-- +goose Up
CREATE TABLE IF NOT EXISTS channel_targets (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    target_id TEXT NOT NULL,
    thread_id TEXT NOT NULL DEFAULT '',
    is_home BOOLEAN NOT NULL DEFAULT FALSE,
    default_subject TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, platform, target_id, thread_id),
    FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_channel_targets_project_platform
ON channel_targets(project_id, platform);

CREATE TABLE IF NOT EXISTS channel_message_sends (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    target_id TEXT NOT NULL,
    thread_id TEXT NOT NULL DEFAULT '',
    requested_by_surface TEXT NOT NULL DEFAULT '',
    requested_by_user TEXT NOT NULL DEFAULT '',
    message_preview TEXT NOT NULL DEFAULT '',
    success BOOLEAN NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_channel_message_sends_project_created
ON channel_message_sends(project_id, created_at);

-- +goose Down
DROP INDEX IF EXISTS idx_channel_message_sends_project_created;
DROP TABLE IF EXISTS channel_message_sends;
DROP INDEX IF EXISTS idx_channel_targets_project_platform;
DROP TABLE IF EXISTS channel_targets;

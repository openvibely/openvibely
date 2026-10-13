-- +goose Up
CREATE TABLE IF NOT EXISTS channel_targets_new (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    target_kind TEXT NOT NULL DEFAULT 'channel',
    name TEXT NOT NULL DEFAULT '',
    target_id TEXT NOT NULL,
    thread_id TEXT NOT NULL DEFAULT '',
    is_home BOOLEAN NOT NULL DEFAULT FALSE,
    default_subject TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, platform, target_kind, target_id, thread_id),
    FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE
);

INSERT INTO channel_targets_new (id, project_id, platform, target_kind, name, target_id, thread_id, is_home, default_subject, created_at, updated_at)
SELECT
    id, project_id, platform,
    CASE platform
        WHEN 'telegram' THEN 'chat'
        WHEN 'email' THEN 'email'
        ELSE 'channel'
    END AS target_kind,
    name, target_id, thread_id, is_home, default_subject, created_at, updated_at
FROM channel_targets;

DROP TABLE channel_targets;
ALTER TABLE channel_targets_new RENAME TO channel_targets;

CREATE INDEX IF NOT EXISTS idx_channel_targets_project_platform
ON channel_targets(project_id, platform);
CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_targets_project_platform_name_nonempty
ON channel_targets(project_id, platform, name)
WHERE name <> '';

ALTER TABLE channel_message_sends ADD COLUMN target_kind TEXT NOT NULL DEFAULT '';

-- +goose Down
CREATE TABLE IF NOT EXISTS channel_targets_old (
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

INSERT INTO channel_targets_old (id, project_id, platform, name, target_id, thread_id, is_home, default_subject, created_at, updated_at)
SELECT id, project_id, platform, name, target_id, thread_id, is_home, default_subject, created_at, updated_at
FROM channel_targets;

DROP TABLE channel_targets;
ALTER TABLE channel_targets_old RENAME TO channel_targets;

CREATE INDEX IF NOT EXISTS idx_channel_targets_project_platform
ON channel_targets(project_id, platform);
CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_targets_project_platform_name_nonempty
ON channel_targets(project_id, platform, name)
WHERE name <> '';

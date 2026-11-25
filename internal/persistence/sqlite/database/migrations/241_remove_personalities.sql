-- +goose Up
DROP TABLE custom_personalities;
DELETE FROM app_settings WHERE key = 'personality';

-- +goose Down
-- Rollback restores an empty schema; deleted personalities and selection cannot be recovered.
CREATE TABLE custom_personalities (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    name TEXT NOT NULL,
    key TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    system_prompt TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

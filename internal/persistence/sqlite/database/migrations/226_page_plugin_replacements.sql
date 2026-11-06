-- +goose Up
-- Temporary recovery information, removed after update commit or rollback.
CREATE TABLE page_plugin_replacements (
 instance_id TEXT PRIMARY KEY REFERENCES page_plugin_instances(id) ON DELETE CASCADE,
 package_id TEXT NOT NULL REFERENCES page_plugin_packages(id) ON DELETE RESTRICT,
 approved_hash TEXT NOT NULL,
 approved_by TEXT NOT NULL,
 approved_at DATETIME,
 was_enabled INTEGER NOT NULL
);
-- +goose Down
DROP TABLE page_plugin_replacements;

-- +goose Up
ALTER TABLE account_usage_snapshots
ADD COLUMN oauth_config_revision INTEGER NOT NULL DEFAULT -1;

-- +goose Down
ALTER TABLE account_usage_snapshots DROP COLUMN oauth_config_revision;

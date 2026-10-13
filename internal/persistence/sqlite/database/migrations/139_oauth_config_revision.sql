-- +goose Up
ALTER TABLE agent_configs ADD COLUMN oauth_config_revision INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE agent_configs DROP COLUMN oauth_config_revision;

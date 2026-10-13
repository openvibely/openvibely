-- +goose Up
ALTER TABLE agent_configs ADD COLUMN oauth_needs_reauth INTEGER NOT NULL DEFAULT 0 CHECK (oauth_needs_reauth IN (0, 1));

-- +goose Down
ALTER TABLE agent_configs DROP COLUMN oauth_needs_reauth;

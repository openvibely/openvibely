-- +goose Up
ALTER TABLE agent_configs ADD COLUMN custom_auth_config_json TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_configs ADD COLUMN custom_auth_state_json TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agent_configs DROP COLUMN custom_auth_state_json;
ALTER TABLE agent_configs DROP COLUMN custom_auth_config_json;

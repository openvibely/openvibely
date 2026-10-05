-- +goose Up
ALTER TABLE agent_configs ADD COLUMN provider_context_window INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_configs ADD COLUMN provider_max_output_tokens INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE agent_configs DROP COLUMN provider_max_output_tokens;
ALTER TABLE agent_configs DROP COLUMN provider_context_window;

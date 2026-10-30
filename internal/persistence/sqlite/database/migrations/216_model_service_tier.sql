-- +goose Up
ALTER TABLE agent_configs ADD COLUMN service_tier TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_inputs ADD COLUMN service_tier TEXT NOT NULL DEFAULT '';
CREATE TABLE task_model_service_tiers (
 task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
 agent_config_id TEXT NOT NULL REFERENCES agent_configs(id) ON DELETE CASCADE,
 model TEXT NOT NULL,
 service_tier TEXT NOT NULL DEFAULT '',
 PRIMARY KEY (task_id, agent_config_id)
);
-- +goose Down
DROP TABLE task_model_service_tiers;
ALTER TABLE thread_inputs DROP COLUMN service_tier;
ALTER TABLE agent_configs DROP COLUMN service_tier;

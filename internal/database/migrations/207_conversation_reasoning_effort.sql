-- +goose Up
CREATE TABLE task_model_efforts (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_config_id TEXT NOT NULL REFERENCES agent_configs(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    effort TEXT NOT NULL,
    PRIMARY KEY (task_id, agent_config_id)
);
ALTER TABLE thread_inputs ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE thread_inputs DROP COLUMN reasoning_effort;
DROP TABLE task_model_efforts;

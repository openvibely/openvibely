-- +goose Up
-- NULL means no task override; an empty string explicitly selects model defaults.
CREATE TABLE task_model_overrides (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_config_id TEXT NOT NULL REFERENCES agent_configs(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    effort TEXT,
    service_tier TEXT,
    PRIMARY KEY (task_id, agent_config_id, model)
);
INSERT INTO task_model_overrides(task_id, agent_config_id, model, effort, service_tier)
SELECT task_id, agent_config_id, model, MAX(effort), MAX(service_tier)
FROM (
    SELECT task_id, agent_config_id, model, effort, NULL AS service_tier FROM task_model_efforts
    UNION ALL
    SELECT task_id, agent_config_id, model, NULL AS effort, service_tier FROM task_model_service_tiers
)
GROUP BY task_id, agent_config_id, model;
DROP TABLE task_model_efforts;
DROP TABLE task_model_service_tiers;

-- +goose Down
CREATE TABLE task_model_efforts (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_config_id TEXT NOT NULL REFERENCES agent_configs(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    effort TEXT NOT NULL,
    PRIMARY KEY (task_id, agent_config_id)
);
CREATE TABLE task_model_service_tiers (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_config_id TEXT NOT NULL REFERENCES agent_configs(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    service_tier TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (task_id, agent_config_id)
);
INSERT INTO task_model_efforts
SELECT task_id, agent_config_id, model, effort FROM (
    SELECT o.*, ROW_NUMBER() OVER (PARTITION BY task_id, agent_config_id
        ORDER BY (o.model = a.model) DESC, o.model) AS rank
    FROM task_model_overrides o JOIN agent_configs a ON a.id = o.agent_config_id
    WHERE effort IS NOT NULL
) WHERE rank = 1;
INSERT INTO task_model_service_tiers
SELECT task_id, agent_config_id, model, service_tier FROM (
    SELECT o.*, ROW_NUMBER() OVER (PARTITION BY task_id, agent_config_id
        ORDER BY (o.model = a.model) DESC, o.model) AS rank
    FROM task_model_overrides o JOIN agent_configs a ON a.id = o.agent_config_id
    WHERE o.service_tier IS NOT NULL
) WHERE rank = 1;
DROP TABLE task_model_overrides;

-- +goose Up

CREATE TABLE webhook_endpoints (
    id                  TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id          TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    enabled             INTEGER NOT NULL DEFAULT 1,
    path_token          TEXT NOT NULL UNIQUE,
    secret              TEXT NOT NULL DEFAULT '',
    system_instructions TEXT NOT NULL DEFAULT '',
    title_template      TEXT NOT NULL DEFAULT '',
    prompt_template     TEXT NOT NULL DEFAULT '',
    default_priority    INTEGER NOT NULL DEFAULT 2,
    created_at          DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at          DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_webhook_endpoints_project ON webhook_endpoints(project_id);
CREATE UNIQUE INDEX idx_webhook_endpoints_path_token ON webhook_endpoints(path_token);

CREATE TABLE webhook_endpoint_agents (
    webhook_endpoint_id TEXT NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
    agent_definition_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    position            INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (webhook_endpoint_id, agent_definition_id)
);

CREATE INDEX idx_webhook_endpoint_agents_endpoint ON webhook_endpoint_agents(webhook_endpoint_id);

CREATE TABLE task_agent_assignments (
    task_id             TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_definition_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    position            INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (task_id, agent_definition_id)
);

CREATE INDEX idx_task_agent_assignments_task ON task_agent_assignments(task_id);

-- +goose Down

DROP TABLE IF EXISTS task_agent_assignments;
DROP TABLE IF EXISTS webhook_endpoint_agents;
DROP TABLE IF EXISTS webhook_endpoints;

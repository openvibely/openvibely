-- +goose Up
CREATE TABLE automations (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    stable_key TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    automation_type TEXT NOT NULL DEFAULT 'custom',
    lifecycle_state TEXT NOT NULL DEFAULT 'draft' CHECK (lifecycle_state IN ('draft','active','paused','archived')),
    health_state TEXT NOT NULL DEFAULT 'unknown' CHECK (health_state IN ('unknown','healthy','degraded','unhealthy')),
    health_reason TEXT NOT NULL DEFAULT '',
    health_evaluated_at DATETIME,
    published_version_id TEXT,
    created_via TEXT NOT NULL DEFAULT 'web',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at DATETIME,
    UNIQUE(project_id, stable_key),
    UNIQUE(id, project_id)
);

CREATE TABLE automation_versions (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version INTEGER NOT NULL,
    state TEXT NOT NULL DEFAULT 'draft' CHECK (state IN ('draft','published')),
    source TEXT NOT NULL DEFAULT 'bootstrap' CHECK (source IN ('manual','template','bootstrap')),
    adapter_key TEXT NOT NULL,
    schema_version INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at DATETIME,
    FOREIGN KEY (automation_id, project_id) REFERENCES automations(id, project_id) ON DELETE CASCADE,
    UNIQUE(automation_id, version),
    UNIQUE(id, automation_id, project_id)
);

CREATE TABLE automation_nodes (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    node_key TEXT NOT NULL,
    name TEXT NOT NULL,
    node_type TEXT NOT NULL CHECK (node_type IN ('trigger','agent_task','human_gate','action','condition','outcome')),
    role TEXT NOT NULL,
    config_json TEXT NOT NULL DEFAULT '{}',
    position_x REAL NOT NULL DEFAULT 0,
    position_y REAL NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (version_id, automation_id, project_id) REFERENCES automation_versions(id, automation_id, project_id) ON DELETE CASCADE,
    UNIQUE(version_id, node_key),
    UNIQUE(id, version_id, automation_id, project_id)
);

CREATE TABLE automation_edges (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    source_node_id TEXT NOT NULL,
    target_node_id TEXT NOT NULL,
    edge_key TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    condition_json TEXT NOT NULL DEFAULT '{}',
    display_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (source_node_id, version_id, automation_id, project_id) REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (target_node_id, version_id, automation_id, project_id) REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    UNIQUE(version_id, edge_key),
    UNIQUE(id, version_id, automation_id, project_id),
    CHECK (source_node_id <> target_node_id)
);

CREATE TABLE automation_definition_resources (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    resource_type TEXT NOT NULL CHECK (resource_type IN ('schedule','task','workflow','agent','skill','channel','source_file')),
    resource_id TEXT NOT NULL,
    relation TEXT NOT NULL DEFAULT 'owned',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (node_id, version_id, automation_id, project_id) REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    UNIQUE(version_id, node_id, resource_type, resource_id, relation)
);

CREATE TABLE automation_trigger_owners (
    schedule_id TEXT PRIMARY KEY REFERENCES schedules(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    ownership_state TEXT NOT NULL DEFAULT 'active' CHECK (ownership_state IN ('active','paused','archived')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (node_id, version_id, automation_id, project_id) REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE
);

CREATE INDEX idx_automations_project_lifecycle ON automations(project_id, lifecycle_state, updated_at DESC);
CREATE INDEX idx_automation_versions_parent ON automation_versions(automation_id, state, version DESC);
CREATE INDEX idx_automation_definition_resources_reverse ON automation_definition_resources(project_id, resource_type, resource_id);
CREATE INDEX idx_automation_trigger_owners_parent ON automation_trigger_owners(automation_id, ownership_state, version_id);

-- +goose Down
DROP TABLE IF EXISTS automation_trigger_owners;
DROP TABLE IF EXISTS automation_definition_resources;
DROP TABLE IF EXISTS automation_edges;
DROP TABLE IF EXISTS automation_nodes;
DROP TABLE IF EXISTS automation_versions;
DROP TABLE IF EXISTS automations;

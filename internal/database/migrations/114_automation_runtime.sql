-- +goose Up
ALTER TABLE executions ADD COLUMN dispatch_id TEXT;
CREATE UNIQUE INDEX idx_executions_dispatch_id ON executions(dispatch_id) WHERE dispatch_id IS NOT NULL;

CREATE TABLE automation_invocations (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    trigger_node_id TEXT NOT NULL,
    trigger_resource_type TEXT NOT NULL,
    trigger_resource_id TEXT NOT NULL,
    occurrence_key TEXT NOT NULL,
    scheduled_for DATETIME,
    status TEXT NOT NULL DEFAULT 'claimed' CHECK (status IN ('claimed','dispatched','running','completed','failed','cancelled','skipped')),
    skipped_reason TEXT NOT NULL DEFAULT '',
    started_at DATETIME,
    completed_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    error_message TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (trigger_node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    CHECK ((status = 'skipped' AND skipped_reason <> '') OR (status <> 'skipped' AND skipped_reason = '')),
    UNIQUE(automation_id, trigger_resource_type, trigger_resource_id, occurrence_key),
    UNIQUE(id, automation_id, project_id),
    UNIQUE(id, automation_id, version_id, project_id)
);

CREATE TABLE automation_dispatch_outbox (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    invocation_id TEXT NOT NULL UNIQUE REFERENCES automation_invocations(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    execution_id TEXT UNIQUE REFERENCES executions(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','submitted','completed','failed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    claimed_by TEXT NOT NULL DEFAULT '',
    claim_expires_at DATETIME,
    next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_error TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((status = 'processing' AND claimed_by <> '' AND claim_expires_at IS NOT NULL) OR
           (status <> 'processing' AND claimed_by = '' AND claim_expires_at IS NULL))
);

CREATE TABLE automation_task_run_reservations (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    dispatch_id TEXT NOT NULL UNIQUE REFERENCES automation_dispatch_outbox(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'reserved' CHECK (state IN ('reserved','claimed')),
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_expires_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((state = 'reserved' AND lease_owner = '' AND lease_expires_at IS NULL) OR
           (state = 'claimed' AND lease_owner <> '' AND lease_expires_at IS NOT NULL))
);

CREATE TABLE automation_work_items (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    origin_version_id TEXT NOT NULL,
    origin_invocation_id TEXT,
    parent_work_item_id TEXT,
    work_item_key TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'work',
    title TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','waiting','blocked','failed','completed','cancelled')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    FOREIGN KEY (origin_version_id, automation_id, project_id)
      REFERENCES automation_versions(id, automation_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (origin_invocation_id, automation_id, origin_version_id, project_id)
      REFERENCES automation_invocations(id, automation_id, version_id, project_id),
    FOREIGN KEY (parent_work_item_id, automation_id, project_id)
      REFERENCES automation_work_items(id, automation_id, project_id),
    UNIQUE(automation_id, work_item_key),
    UNIQUE(id, automation_id, project_id),
    UNIQUE(id, automation_id, origin_version_id, project_id)
);

CREATE TABLE automation_work_item_positions (
    work_item_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('active','waiting','blocked','failed')),
    entered_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (work_item_id, automation_id, version_id, project_id)
      REFERENCES automation_work_items(id, automation_id, origin_version_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    PRIMARY KEY (work_item_id, node_id)
);

CREATE TABLE automation_thread_input_bindings (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    thread_input_id TEXT NOT NULL REFERENCES thread_inputs(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    invocation_id TEXT,
    work_item_id TEXT,
    binding_key TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (invocation_id, automation_id, project_id)
      REFERENCES automation_invocations(id, automation_id, project_id),
    FOREIGN KEY (work_item_id, automation_id, version_id, project_id)
      REFERENCES automation_work_items(id, automation_id, origin_version_id, project_id),
    CHECK (invocation_id IS NOT NULL OR work_item_id IS NOT NULL),
    UNIQUE(thread_input_id, binding_key)
);

CREATE TABLE automation_activities (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    invocation_id TEXT,
    work_item_id TEXT,
    activity_key TEXT NOT NULL,
    activity_type TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','running','waiting','completed','failed','cancelled')),
    started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    error_message TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (invocation_id, automation_id, project_id)
      REFERENCES automation_invocations(id, automation_id, project_id),
    FOREIGN KEY (work_item_id, automation_id, version_id, project_id)
      REFERENCES automation_work_items(id, automation_id, origin_version_id, project_id),
    CHECK (invocation_id IS NOT NULL OR work_item_id IS NOT NULL),
    UNIQUE(automation_id, version_id, activity_key),
    UNIQUE(id, automation_id, project_id),
    UNIQUE(id, version_id, automation_id, project_id)
);

CREATE TABLE automation_activity_resources (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    activity_id TEXT NOT NULL REFERENCES automation_activities(id) ON DELETE CASCADE,
    resource_type TEXT NOT NULL CHECK (resource_type IN ('task','execution','alert','goal','github_issue','pull_request','review','workflow_execution')),
    resource_id TEXT NOT NULL,
    relation TEXT NOT NULL DEFAULT 'subject',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(activity_id, resource_type, resource_id, relation)
);

CREATE TABLE automation_transitions (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    work_item_id TEXT NOT NULL,
    invocation_id TEXT,
    activity_id TEXT,
    from_node_id TEXT,
    to_node_id TEXT NOT NULL,
    edge_id TEXT,
    event_key TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('entered','waiting','completed','failed','blocked','cancelled')),
    metadata_json TEXT NOT NULL DEFAULT '{}',
    occurred_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (work_item_id, automation_id, version_id, project_id)
      REFERENCES automation_work_items(id, automation_id, origin_version_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (invocation_id, automation_id, project_id)
      REFERENCES automation_invocations(id, automation_id, project_id),
    FOREIGN KEY (activity_id, version_id, automation_id, project_id)
      REFERENCES automation_activities(id, version_id, automation_id, project_id),
    FOREIGN KEY (from_node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id),
    FOREIGN KEY (to_node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (edge_id, version_id, automation_id, project_id)
      REFERENCES automation_edges(id, version_id, automation_id, project_id),
    UNIQUE(automation_id, version_id, event_key)
);

CREATE INDEX idx_automation_invocations_history ON automation_invocations(automation_id, COALESCE(scheduled_for, started_at, created_at) DESC, id);
CREATE INDEX idx_automation_invocations_status ON automation_invocations(project_id, status, updated_at);
CREATE INDEX idx_automation_dispatch_pending ON automation_dispatch_outbox(status, next_attempt_at, claim_expires_at);
CREATE INDEX idx_automation_task_reservation_lease ON automation_task_run_reservations(state, lease_expires_at);
CREATE INDEX idx_automation_work_items_live ON automation_work_items(automation_id, status, updated_at DESC, id);
CREATE INDEX idx_automation_positions_node ON automation_work_item_positions(automation_id, version_id, node_id, state);
CREATE INDEX idx_automation_input_bindings_input ON automation_thread_input_bindings(thread_input_id, automation_id);
CREATE INDEX idx_automation_activities_invocation ON automation_activities(invocation_id, started_at, id);
CREATE INDEX idx_automation_activities_work_item ON automation_activities(work_item_id, started_at, id);
CREATE INDEX idx_automation_activity_resources_reverse ON automation_activity_resources(resource_type, resource_id);
CREATE INDEX idx_automation_transitions_work_item ON automation_transitions(work_item_id, occurred_at, id);
CREATE INDEX idx_automation_transitions_node ON automation_transitions(automation_id, version_id, to_node_id, occurred_at DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_automation_transitions_node;
DROP INDEX IF EXISTS idx_automation_transitions_work_item;
DROP INDEX IF EXISTS idx_automation_activity_resources_reverse;
DROP INDEX IF EXISTS idx_automation_activities_work_item;
DROP INDEX IF EXISTS idx_automation_activities_invocation;
DROP INDEX IF EXISTS idx_automation_input_bindings_input;
DROP INDEX IF EXISTS idx_automation_positions_node;
DROP INDEX IF EXISTS idx_automation_work_items_live;
DROP INDEX IF EXISTS idx_automation_task_reservation_lease;
DROP INDEX IF EXISTS idx_automation_dispatch_pending;
DROP INDEX IF EXISTS idx_automation_invocations_status;
DROP INDEX IF EXISTS idx_automation_invocations_history;
DROP TABLE IF EXISTS automation_transitions;
DROP TABLE IF EXISTS automation_activity_resources;
DROP TABLE IF EXISTS automation_activities;
DROP TABLE IF EXISTS automation_thread_input_bindings;
DROP TABLE IF EXISTS automation_work_item_positions;
DROP TABLE IF EXISTS automation_work_items;
DROP TABLE IF EXISTS automation_task_run_reservations;
DROP TABLE IF EXISTS automation_dispatch_outbox;
DROP TABLE IF EXISTS automation_invocations;
DROP INDEX IF EXISTS idx_executions_dispatch_id;
ALTER TABLE executions DROP COLUMN dispatch_id;

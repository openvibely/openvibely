-- +goose Up
-- +goose NO TRANSACTION
PRAGMA foreign_keys = OFF;

CREATE TABLE alerts_new (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    scope TEXT NOT NULL DEFAULT 'project' CHECK(scope IN ('project')),
    task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL,
    execution_id TEXT REFERENCES executions(id) ON DELETE SET NULL,
    source_task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL,
    type TEXT NOT NULL DEFAULT 'custom',
    severity TEXT NOT NULL DEFAULT 'info' CHECK(severity IN ('info', 'warning', 'error')),
    title TEXT NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT 'operational',
    metadata_json TEXT NOT NULL DEFAULT '{}',
    idempotency_key TEXT,
    decision_state TEXT NOT NULL DEFAULT 'not_required'
        CHECK(decision_state IN ('not_required', 'pending', 'approved', 'rejected', 'dismissed')),
    decided_at DATETIME,
    processing_state TEXT NOT NULL DEFAULT 'not_applicable'
        CHECK(processing_state IN ('not_applicable', 'unclaimed', 'claimed', 'implementation_task_linked', 'completed', 'failed')),
    claimant TEXT,
    claimed_at DATETIME,
    claim_expires_at DATETIME,
    implementation_task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL,
    processing_error TEXT NOT NULL DEFAULT '',
    is_read INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO alerts_new (
    id, project_id, scope, task_id, execution_id, source_task_id, type, severity,
    title, message, body, source, metadata_json, decision_state, processing_state,
    is_read, created_at, updated_at
)
SELECT
    id, project_id, 'project', task_id, execution_id, task_id, type, severity,
    title, message, message, 'operational', '{}', 'not_required', 'not_applicable',
    is_read, created_at, created_at
FROM alerts;

DROP TABLE alerts;
ALTER TABLE alerts_new RENAME TO alerts;
CREATE INDEX idx_alerts_project_id ON alerts(project_id);
CREATE INDEX idx_alerts_is_read ON alerts(project_id, is_read);
CREATE INDEX idx_alerts_created_at ON alerts(created_at, id);
CREATE INDEX idx_alerts_project_lifecycle ON alerts(project_id, decision_state, processing_state, created_at, id);
CREATE INDEX idx_alerts_source ON alerts(project_id, source, created_at, id);
CREATE UNIQUE INDEX idx_alerts_project_idempotency
    ON alerts(project_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL AND idempotency_key <> '';
CREATE UNIQUE INDEX idx_alerts_implementation_task
    ON alerts(implementation_task_id)
    WHERE implementation_task_id IS NOT NULL;

PRAGMA foreign_keys = ON;

-- +goose Down
-- +goose NO TRANSACTION
PRAGMA foreign_keys = OFF;

CREATE TABLE alerts_old (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL,
    execution_id TEXT REFERENCES executions(id) ON DELETE SET NULL,
    type TEXT NOT NULL DEFAULT 'task_failed' CHECK(type IN ('task_failed', 'task_needs_followup', 'custom')),
    severity TEXT NOT NULL DEFAULT 'error' CHECK(severity IN ('info', 'warning', 'error')),
    title TEXT NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    is_read INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO alerts_old (id, project_id, task_id, execution_id, type, severity, title, message, is_read, created_at)
SELECT id, project_id, task_id, execution_id,
       CASE WHEN type IN ('task_failed', 'task_needs_followup', 'custom') THEN type ELSE 'custom' END,
       severity, title, message, is_read, created_at
FROM alerts;
DROP TABLE alerts;
ALTER TABLE alerts_old RENAME TO alerts;
CREATE INDEX idx_alerts_project_id ON alerts(project_id);
CREATE INDEX idx_alerts_is_read ON alerts(project_id, is_read);
CREATE INDEX idx_alerts_created_at ON alerts(created_at);
CREATE INDEX idx_alerts_project_is_read ON alerts(project_id, is_read);

PRAGMA foreign_keys = ON;

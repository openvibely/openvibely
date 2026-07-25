-- +goose Up
DROP INDEX IF EXISTS idx_automation_publication_steps_status;
DROP INDEX IF EXISTS idx_automation_publication_attempts_parent;
DROP TABLE IF EXISTS automation_publication_steps;
DROP TABLE IF EXISTS automation_publication_attempts;

-- +goose Down
CREATE TABLE automation_publication_attempts (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    plan_revision TEXT NOT NULL,
    status TEXT NOT NULL,
    error_message TEXT NOT NULL DEFAULT '',
    claim_owner TEXT NOT NULL DEFAULT '',
    claim_expires_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME
);
CREATE TABLE automation_publication_steps (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    attempt_id TEXT NOT NULL REFERENCES automation_publication_attempts(id) ON DELETE CASCADE,
    step_key TEXT NOT NULL,
    operation TEXT NOT NULL,
    target_key TEXT NOT NULL,
    display_order INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL,
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_automation_publication_attempts_parent
  ON automation_publication_attempts(project_id, automation_id, created_at DESC);
CREATE INDEX idx_automation_publication_steps_status
  ON automation_publication_steps(attempt_id, status, updated_at);

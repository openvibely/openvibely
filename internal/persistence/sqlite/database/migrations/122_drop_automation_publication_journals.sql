-- +goose Up
-- Failed publication attempts could create disabled Scheduler rows before the
-- final graph transaction established trigger ownership. Consume the exact
-- create-step journal before dropping it, while protecting any schedule that
-- became part of the current published graph.
DELETE FROM schedules
WHERE id IN (
    SELECT step.resource_id
    FROM automation_publication_steps step
    JOIN automation_publication_attempts attempt ON attempt.id = step.attempt_id
    WHERE attempt.status <> 'completed'
      AND step.operation = 'create'
      AND step.resource_type = 'schedule'
      AND trim(step.resource_id) <> ''
)
AND NOT EXISTS (
    SELECT 1
    FROM automation_trigger_owners owner
    JOIN automations automation
      ON automation.id = owner.automation_id
     AND automation.project_id = owner.project_id
    WHERE owner.schedule_id = schedules.id
      AND automation.published_version_id = owner.version_id
)
AND NOT EXISTS (
    SELECT 1
    FROM automation_definition_resources resource
    JOIN automations automation
      ON automation.id = resource.automation_id
     AND automation.project_id = resource.project_id
    WHERE resource.resource_type = 'schedule'
      AND resource.resource_id = schedules.id
      AND automation.published_version_id = resource.version_id
);

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

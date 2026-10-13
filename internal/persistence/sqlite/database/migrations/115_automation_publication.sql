-- +goose Up
CREATE TABLE automation_draft_metadata (
    version_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    candidate_json TEXT NOT NULL DEFAULT '{}',
    assumptions_json TEXT NOT NULL DEFAULT '[]',
    warnings_json TEXT NOT NULL DEFAULT '[]',
    validation_json TEXT NOT NULL DEFAULT '[]',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (version_id, automation_id, project_id)
      REFERENCES automation_versions(id, automation_id, project_id) ON DELETE CASCADE,
    CHECK (json_valid(candidate_json)),
    CHECK (json_valid(assumptions_json)),
    CHECK (json_valid(warnings_json)),
    CHECK (json_valid(validation_json))
);

CREATE TABLE automation_publication_attempts (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    plan_revision TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('publishing','completed','failed')),
    error_message TEXT NOT NULL DEFAULT '',
    claim_owner TEXT NOT NULL DEFAULT '',
    claim_expires_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    FOREIGN KEY (version_id, automation_id, project_id)
      REFERENCES automation_versions(id, automation_id, project_id) ON DELETE CASCADE,
    UNIQUE(version_id, plan_revision),
    UNIQUE(id, project_id, automation_id, version_id),
    CHECK ((claim_owner = '' AND claim_expires_at IS NULL) OR (claim_owner <> '' AND claim_expires_at IS NOT NULL))
);

CREATE TABLE automation_publication_steps (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    attempt_id TEXT NOT NULL REFERENCES automation_publication_attempts(id) ON DELETE CASCADE,
    step_key TEXT NOT NULL,
    operation TEXT NOT NULL,
    target_key TEXT NOT NULL,
    display_order INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL CHECK (status IN ('pending','running','completed','ambiguous','failed')),
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(attempt_id, step_key),
    UNIQUE(attempt_id, operation, target_key)
);

CREATE TABLE automation_chat_confirmation_receipts (
    token_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    plan_revision TEXT NOT NULL,
    principal_id TEXT NOT NULL,
    thread_id TEXT NOT NULL,
    plan_message_id TEXT NOT NULL,
    expires_at DATETIME NOT NULL,
    consumed_attempt_id TEXT REFERENCES automation_publication_attempts(id),
    confirming_user_input_id TEXT UNIQUE,
    confirmation_method TEXT NOT NULL DEFAULT '' CHECK (confirmation_method IN ('','button','command')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    consumed_at DATETIME,
    FOREIGN KEY (version_id, automation_id, project_id)
      REFERENCES automation_versions(id, automation_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (consumed_attempt_id, project_id, automation_id, version_id)
      REFERENCES automation_publication_attempts(id, project_id, automation_id, version_id),
    CHECK ((consumed_at IS NULL AND consumed_attempt_id IS NULL AND confirming_user_input_id IS NULL AND confirmation_method = '') OR
           (consumed_at IS NOT NULL AND consumed_attempt_id IS NOT NULL AND confirming_user_input_id IS NOT NULL AND confirmation_method <> ''))
);

CREATE TABLE automation_chat_confirmation_inputs (
    input_id TEXT PRIMARY KEY REFERENCES executions(id) ON DELETE CASCADE,
    token_id TEXT NOT NULL REFERENCES automation_chat_confirmation_receipts(token_id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    principal_id TEXT NOT NULL,
    thread_id TEXT NOT NULL,
    confirmation_method TEXT NOT NULL CHECK (confirmation_method IN ('command')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(token_id, input_id)
);

CREATE INDEX idx_automation_drafts_parent
  ON automation_draft_metadata(project_id, automation_id, updated_at DESC);
CREATE INDEX idx_automation_publication_attempts_parent
  ON automation_publication_attempts(project_id, automation_id, created_at DESC);
CREATE INDEX idx_automation_publication_steps_status
  ON automation_publication_steps(attempt_id, status, updated_at);
CREATE INDEX idx_automation_confirmation_scope
  ON automation_chat_confirmation_receipts(project_id, principal_id, thread_id, expires_at);
CREATE INDEX idx_automation_confirmation_inputs_scope
  ON automation_chat_confirmation_inputs(project_id, principal_id, thread_id, created_at);

-- +goose Down
DROP INDEX IF EXISTS idx_automation_confirmation_inputs_scope;
DROP INDEX IF EXISTS idx_automation_confirmation_scope;
DROP INDEX IF EXISTS idx_automation_publication_steps_status;
DROP INDEX IF EXISTS idx_automation_publication_attempts_parent;
DROP INDEX IF EXISTS idx_automation_drafts_parent;
DROP TABLE IF EXISTS automation_chat_confirmation_inputs;
DROP TABLE IF EXISTS automation_chat_confirmation_receipts;
DROP TABLE IF EXISTS automation_publication_steps;
DROP TABLE IF EXISTS automation_publication_attempts;
DROP TABLE IF EXISTS automation_draft_metadata;

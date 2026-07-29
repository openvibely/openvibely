-- +goose Up
-- +goose NO TRANSACTION
PRAGMA foreign_keys = OFF;

DROP INDEX IF EXISTS idx_automation_confirmation_scope;
DROP INDEX IF EXISTS idx_automation_confirmation_inputs_scope;
ALTER TABLE automation_chat_confirmation_inputs RENAME TO automation_chat_confirmation_inputs_old;
ALTER TABLE automation_chat_confirmation_receipts RENAME TO automation_chat_confirmation_receipts_old;

CREATE TABLE automation_chat_confirmation_receipts (
    token_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    plan_revision TEXT NOT NULL,
    principal_id TEXT NOT NULL,
    thread_id TEXT NOT NULL,
    plan_message_id TEXT NOT NULL,
    automation_name TEXT NOT NULL,
    source TEXT NOT NULL,
    candidate_json TEXT NOT NULL,
    expires_at DATETIME NOT NULL,
    consumed_attempt_id TEXT REFERENCES automation_publication_attempts(id),
    confirming_user_input_id TEXT UNIQUE,
    confirmation_method TEXT NOT NULL DEFAULT '' CHECK (confirmation_method IN ('','button','command')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    consumed_at DATETIME,
    FOREIGN KEY (consumed_attempt_id, project_id, automation_id, version_id)
      REFERENCES automation_publication_attempts(id, project_id, automation_id, version_id),
    CHECK ((consumed_at IS NULL AND consumed_attempt_id IS NULL AND confirming_user_input_id IS NULL AND confirmation_method = '') OR
           (consumed_at IS NOT NULL AND consumed_attempt_id IS NOT NULL AND confirming_user_input_id IS NOT NULL AND confirmation_method <> ''))
);

INSERT INTO automation_chat_confirmation_receipts
    (token_id, project_id, automation_id, version_id, plan_revision, principal_id, thread_id, plan_message_id,
     automation_name, source, candidate_json, expires_at, consumed_attempt_id, confirming_user_input_id,
     confirmation_method, created_at, consumed_at)
SELECT r.token_id, r.project_id, r.automation_id, r.version_id, r.plan_revision, r.principal_id, r.thread_id,
       r.plan_message_id, COALESCE(a.name, 'Automation'), COALESCE(v.source, 'manual'),
       COALESCE(m.candidate_json, '{}'), r.expires_at, r.consumed_attempt_id, r.confirming_user_input_id,
       r.confirmation_method, r.created_at, r.consumed_at
FROM automation_chat_confirmation_receipts_old r
LEFT JOIN automations a ON a.id = r.automation_id AND a.project_id = r.project_id
LEFT JOIN automation_versions v ON v.id = r.version_id AND v.automation_id = r.automation_id AND v.project_id = r.project_id
LEFT JOIN automation_draft_metadata m ON m.version_id = r.version_id AND m.automation_id = r.automation_id AND m.project_id = r.project_id;

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

INSERT INTO automation_chat_confirmation_inputs
    (input_id, token_id, project_id, automation_id, version_id, principal_id, thread_id, confirmation_method, created_at)
SELECT input_id, token_id, project_id, automation_id, version_id, principal_id, thread_id, confirmation_method, created_at
FROM automation_chat_confirmation_inputs_old;

DROP TABLE automation_chat_confirmation_inputs_old;
DROP TABLE automation_chat_confirmation_receipts_old;
CREATE INDEX idx_automation_confirmation_scope
  ON automation_chat_confirmation_receipts(project_id, principal_id, thread_id, expires_at);
CREATE INDEX idx_automation_confirmation_inputs_scope
  ON automation_chat_confirmation_inputs(project_id, principal_id, thread_id, created_at);

PRAGMA foreign_keys = ON;

-- +goose Down
-- +goose NO TRANSACTION
PRAGMA foreign_keys = OFF;

DROP INDEX IF EXISTS idx_automation_confirmation_scope;
DROP INDEX IF EXISTS idx_automation_confirmation_inputs_scope;
ALTER TABLE automation_chat_confirmation_inputs RENAME TO automation_chat_confirmation_inputs_new;
ALTER TABLE automation_chat_confirmation_receipts RENAME TO automation_chat_confirmation_receipts_new;

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

INSERT INTO automation_chat_confirmation_receipts
    (token_id, project_id, automation_id, version_id, plan_revision, principal_id, thread_id, plan_message_id,
     expires_at, consumed_attempt_id, confirming_user_input_id, confirmation_method, created_at, consumed_at)
SELECT r.token_id, r.project_id, r.automation_id, r.version_id, r.plan_revision, r.principal_id, r.thread_id,
       r.plan_message_id, r.expires_at, r.consumed_attempt_id, r.confirming_user_input_id,
       r.confirmation_method, r.created_at, r.consumed_at
FROM automation_chat_confirmation_receipts_new r
JOIN automation_versions v ON v.id = r.version_id AND v.automation_id = r.automation_id AND v.project_id = r.project_id;

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

INSERT INTO automation_chat_confirmation_inputs
    (input_id, token_id, project_id, automation_id, version_id, principal_id, thread_id, confirmation_method, created_at)
SELECT i.input_id, i.token_id, i.project_id, i.automation_id, i.version_id, i.principal_id, i.thread_id,
       i.confirmation_method, i.created_at
FROM automation_chat_confirmation_inputs_new i
JOIN automation_chat_confirmation_receipts r ON r.token_id = i.token_id;

DROP TABLE automation_chat_confirmation_inputs_new;
DROP TABLE automation_chat_confirmation_receipts_new;
CREATE INDEX idx_automation_confirmation_scope
  ON automation_chat_confirmation_receipts(project_id, principal_id, thread_id, expires_at);
CREATE INDEX idx_automation_confirmation_inputs_scope
  ON automation_chat_confirmation_inputs(project_id, principal_id, thread_id, created_at);

PRAGMA foreign_keys = ON;

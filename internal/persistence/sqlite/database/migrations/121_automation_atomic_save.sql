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
    principal_id TEXT NOT NULL,
    thread_id TEXT NOT NULL,
    plan_message_id TEXT NOT NULL,
    automation_name TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('manual','template')),
    candidate_json TEXT NOT NULL CHECK (json_valid(candidate_json)),
    expires_at DATETIME NOT NULL,
    confirming_user_input_id TEXT UNIQUE,
    confirmation_method TEXT NOT NULL DEFAULT '' CHECK (confirmation_method IN ('','command')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    consumed_at DATETIME,
    CHECK ((consumed_at IS NULL AND confirming_user_input_id IS NULL AND confirmation_method = '') OR
           (consumed_at IS NOT NULL AND confirming_user_input_id IS NOT NULL AND confirmation_method = 'command'))
);

INSERT INTO automation_chat_confirmation_receipts
    (token_id, project_id, principal_id, thread_id, plan_message_id, automation_name, source,
     candidate_json, expires_at, confirming_user_input_id, confirmation_method, created_at, consumed_at)
SELECT token_id, project_id, principal_id, thread_id, plan_message_id, automation_name, source,
       candidate_json, expires_at, confirming_user_input_id,
       CASE WHEN consumed_at IS NULL THEN '' ELSE 'command' END,
       created_at, consumed_at
FROM automation_chat_confirmation_receipts_old;

CREATE TABLE automation_chat_confirmation_inputs (
    input_id TEXT PRIMARY KEY REFERENCES executions(id) ON DELETE CASCADE,
    token_id TEXT NOT NULL REFERENCES automation_chat_confirmation_receipts(token_id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    principal_id TEXT NOT NULL,
    thread_id TEXT NOT NULL,
    confirmation_method TEXT NOT NULL CHECK (confirmation_method = 'command'),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(token_id, input_id)
);

INSERT INTO automation_chat_confirmation_inputs
    (input_id, token_id, project_id, principal_id, thread_id, confirmation_method, created_at)
SELECT input_id, token_id, project_id, principal_id, thread_id, confirmation_method, created_at
FROM automation_chat_confirmation_inputs_old;

DROP TABLE automation_chat_confirmation_inputs_old;
DROP TABLE automation_chat_confirmation_receipts_old;

DROP INDEX IF EXISTS idx_automation_drafts_parent;
ALTER TABLE automation_draft_metadata RENAME TO automation_graph_metadata;
CREATE INDEX idx_automation_graph_metadata_parent
  ON automation_graph_metadata(project_id, automation_id, updated_at DESC);
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
DROP INDEX IF EXISTS idx_automation_graph_metadata_parent;
ALTER TABLE automation_graph_metadata RENAME TO automation_draft_metadata;
CREATE INDEX idx_automation_drafts_parent
  ON automation_draft_metadata(project_id, automation_id, updated_at DESC);

ALTER TABLE automation_chat_confirmation_inputs RENAME TO automation_chat_confirmation_inputs_new;
ALTER TABLE automation_chat_confirmation_receipts RENAME TO automation_chat_confirmation_receipts_new;

CREATE TABLE automation_chat_confirmation_receipts AS
SELECT token_id, project_id, '' AS automation_id, '' AS version_id, '' AS plan_revision,
       principal_id, thread_id, plan_message_id, automation_name, source, candidate_json, expires_at,
       '' AS consumed_attempt_id, confirming_user_input_id, confirmation_method, created_at, consumed_at
FROM automation_chat_confirmation_receipts_new;

CREATE TABLE automation_chat_confirmation_inputs AS
SELECT input_id, token_id, project_id, '' AS automation_id, '' AS version_id,
       principal_id, thread_id, confirmation_method, created_at
FROM automation_chat_confirmation_inputs_new;

DROP TABLE automation_chat_confirmation_inputs_new;
DROP TABLE automation_chat_confirmation_receipts_new;
CREATE INDEX idx_automation_confirmation_scope
  ON automation_chat_confirmation_receipts(project_id, principal_id, thread_id, expires_at);
CREATE INDEX idx_automation_confirmation_inputs_scope
  ON automation_chat_confirmation_inputs(project_id, principal_id, thread_id, created_at);

PRAGMA foreign_keys = ON;

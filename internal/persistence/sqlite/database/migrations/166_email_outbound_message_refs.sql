-- +goose Up
CREATE TABLE IF NOT EXISTS email_outbound_message_refs (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL,
    email_from TEXT NOT NULL,
    outbound_message_id TEXT NOT NULL,
    email_session_key TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_email_outbound_refs_unique
    ON email_outbound_message_refs(project_id, email_from, outbound_message_id);

CREATE INDEX IF NOT EXISTS idx_email_outbound_refs_lookup
    ON email_outbound_message_refs(project_id, email_from, outbound_message_id, email_session_key);

-- +goose Down
DROP INDEX IF EXISTS idx_email_outbound_refs_lookup;
DROP INDEX IF EXISTS idx_email_outbound_refs_unique;
DROP TABLE IF EXISTS email_outbound_message_refs;

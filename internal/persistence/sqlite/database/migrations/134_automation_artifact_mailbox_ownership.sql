-- +goose Up
CREATE TABLE automation_artifact_mailbox_owners (
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    artifact_type TEXT NOT NULL CHECK (artifact_type IN ('alert', 'github_issue')),
    artifact_id TEXT NOT NULL,
    producer_node_key TEXT NOT NULL,
    action_node_key TEXT NOT NULL,
    gate_node_key TEXT NOT NULL,
    mailbox_node_key TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (automation_id, project_id) REFERENCES automations(id, project_id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, automation_id, artifact_type, artifact_id, producer_node_key, action_node_key, gate_node_key, mailbox_node_key)
);
CREATE INDEX idx_automation_artifact_mailbox_lookup
    ON automation_artifact_mailbox_owners(project_id, artifact_type, artifact_id, automation_id, mailbox_node_key);

-- Existing artifacts have no durable logical-mailbox ownership record. Keep them
-- fail-closed rather than inferring or backfilling ownership during upgrade.

-- +goose Down
DROP INDEX IF EXISTS idx_automation_artifact_mailbox_lookup;
DROP TABLE IF EXISTS automation_artifact_mailbox_owners;

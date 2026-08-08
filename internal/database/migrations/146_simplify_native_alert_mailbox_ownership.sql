-- +goose Up
DELETE FROM automation_artifact_mailbox_owners
WHERE artifact_type = 'alert'
  AND rowid NOT IN (
    SELECT MIN(rowid)
    FROM automation_artifact_mailbox_owners
    WHERE artifact_type = 'alert'
    GROUP BY project_id, automation_id, artifact_type, artifact_id
  );

UPDATE automation_artifact_mailbox_owners
SET producer_node_key = '',
    action_node_key = '',
    gate_node_key = '',
    mailbox_node_key = ''
WHERE artifact_type = 'alert';

-- +goose Down
-- Native alert mailbox-owner topology keys are intentionally not reconstructed.

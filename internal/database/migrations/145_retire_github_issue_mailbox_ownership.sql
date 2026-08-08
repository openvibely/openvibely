-- +goose Up
DELETE FROM automation_artifact_mailbox_owners WHERE artifact_type = 'github_issue';

-- +goose Down
-- Retired GitHub issue mailbox-owner rows are intentionally not reconstructed.

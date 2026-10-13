-- +goose Up
ALTER TABLE automation_github_issue_dedup_leases
    ADD COLUMN created_issue_number INTEGER CHECK (created_issue_number IS NULL OR created_issue_number > 0);

-- +goose Down
ALTER TABLE automation_github_issue_dedup_leases
    DROP COLUMN created_issue_number;

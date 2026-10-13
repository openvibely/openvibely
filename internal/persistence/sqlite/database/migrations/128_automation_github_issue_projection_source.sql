-- +goose Up
ALTER TABLE automation_github_issue_dedup_leases
    ADD COLUMN projection_source_json TEXT NOT NULL DEFAULT '';

-- Historical claims have no trustworthy source snapshot. Keep them fail-closed;
-- do not infer Automation provenance during upgrade.

-- +goose Down
ALTER TABLE automation_github_issue_dedup_leases
    DROP COLUMN projection_source_json;

-- +goose Up
CREATE TABLE automation_github_issue_dedup_leases (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    repository_full_name TEXT NOT NULL,
    title_fingerprint TEXT NOT NULL,
    owner_token TEXT NOT NULL,
    lease_expires_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (project_id, repository_full_name, title_fingerprint)
);
CREATE INDEX idx_automation_github_issue_dedup_lease_expiry
    ON automation_github_issue_dedup_leases(lease_expires_at);

-- +goose Down
DROP INDEX IF EXISTS idx_automation_github_issue_dedup_lease_expiry;
DROP TABLE IF EXISTS automation_github_issue_dedup_leases;

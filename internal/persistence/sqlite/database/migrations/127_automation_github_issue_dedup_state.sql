-- +goose Up
ALTER TABLE automation_github_issue_dedup_leases
    ADD COLUMN mutation_state TEXT NOT NULL DEFAULT 'reserved'
        CHECK (mutation_state IN ('reserved', 'dispatched', 'completed'));

-- Existing numberless claims may already represent an external mutation whose
-- result was lost. Conservatively fail them closed rather than making them
-- reclaimable after upgrade.
UPDATE automation_github_issue_dedup_leases
SET mutation_state = CASE
    WHEN created_issue_number IS NULL THEN 'dispatched'
    ELSE 'completed'
END;

-- +goose Down
ALTER TABLE automation_github_issue_dedup_leases
    DROP COLUMN mutation_state;

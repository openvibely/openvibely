-- +goose Up
-- The scheduler scans this partial index globally, so active/pending admission
-- rows are already in the required priority/display/creation order. Keeping the
-- projection columns out of the index limits write and storage overhead; the
-- compact admission query still performs authoritative table reads for its small
-- set of active candidates and retains every ownership exclusion predicate.
CREATE INDEX idx_tasks_active_pending_admission_order
    ON tasks(category, status, priority DESC, display_order ASC, created_at ASC)
    WHERE category = 'active' AND status = 'pending';

-- +goose Down
DROP INDEX idx_tasks_active_pending_admission_order;

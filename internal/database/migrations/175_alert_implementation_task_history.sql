-- +goose Up
ALTER TABLE alerts ADD COLUMN implementation_task_was_linked INTEGER NOT NULL DEFAULT 0
    CHECK(implementation_task_was_linked IN (0, 1));

UPDATE alerts
SET implementation_task_was_linked = 1
WHERE implementation_task_id IS NOT NULL
   OR processing_state IN ('implementation_task_linked', 'completed');

CREATE INDEX idx_alerts_project_implementation_task_was_linked
    ON alerts(project_id, implementation_task_was_linked, created_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_alerts_project_implementation_task_was_linked;
ALTER TABLE alerts DROP COLUMN implementation_task_was_linked;

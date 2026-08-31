-- +goose Up
-- Historical databases can contain current work-item positions that survived
-- after completed/cancelled execution terminalization moved the work item out of
-- that node. Remove those stale positions so Live/portfolio counts do not show
-- terminal work as still active or waiting.
DELETE FROM automation_work_item_positions AS p
WHERE EXISTS (
    SELECT 1
    FROM automation_transitions AS tr
    WHERE tr.project_id = p.project_id
      AND tr.automation_id = p.automation_id
      AND tr.version_id = p.version_id
      AND tr.work_item_id = p.work_item_id
      AND tr.from_node_id = p.node_id
      AND tr.state IN ('completed', 'cancelled')
      AND tr.occurred_at >= p.entered_at
);

-- +goose Down
-- Irreversible data cleanup.

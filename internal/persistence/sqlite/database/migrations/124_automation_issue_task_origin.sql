-- +goose Up
-- Compatibility repair for issue-specific Tasks created by the initial atomic
-- Automation GitHub inbox implementation before it persisted CreatedVia.
-- The activity key and child relation are feature-owned durable provenance;
-- generic create_task resources are intentionally excluded.
UPDATE tasks
SET created_via = (
    SELECT 'automation:' || activity.automation_id || ':' || node.node_key
    FROM automation_activity_resources resource
    JOIN automation_activities activity ON activity.id = resource.activity_id
    JOIN automation_nodes node ON node.id = activity.node_id
        AND node.version_id = activity.version_id
        AND node.automation_id = activity.automation_id
        AND node.project_id = activity.project_id
    WHERE resource.resource_type = 'task'
      AND resource.resource_id = tasks.id
      AND resource.relation = 'child'
      AND activity.activity_type = 'create_task'
      AND activity.work_item_id IS NOT NULL
      AND activity.activity_key = 'work-item:' || activity.work_item_id || ':implementation-task'
      AND node.node_type = 'agent_task'
      AND node.role IN ('task', 'implementation')
    ORDER BY activity.started_at, activity.id
    LIMIT 1
)
WHERE trim(COALESCE(created_via, '')) = ''
  AND EXISTS (
    SELECT 1
    FROM automation_activity_resources resource
    JOIN automation_activities activity ON activity.id = resource.activity_id
    JOIN automation_nodes node ON node.id = activity.node_id
        AND node.version_id = activity.version_id
        AND node.automation_id = activity.automation_id
        AND node.project_id = activity.project_id
    WHERE resource.resource_type = 'task'
      AND resource.resource_id = tasks.id
      AND resource.relation = 'child'
      AND activity.activity_type = 'create_task'
      AND activity.work_item_id IS NOT NULL
      AND activity.activity_key = 'work-item:' || activity.work_item_id || ':implementation-task'
      AND node.node_type = 'agent_task'
      AND node.role IN ('task', 'implementation')
  );

-- +goose Down
SELECT 1;

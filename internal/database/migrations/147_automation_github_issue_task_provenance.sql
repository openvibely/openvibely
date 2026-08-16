-- +goose Up
CREATE TABLE automation_github_issue_task_provenance (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    issue_resource_id TEXT NOT NULL,
    implementation_node_key TEXT NOT NULL,
    created_from_version_id TEXT NOT NULL DEFAULT '',
    created_from_node_id TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (automation_id, project_id) REFERENCES automations(id, project_id) ON DELETE CASCADE,
    UNIQUE(project_id, task_id)
);

CREATE INDEX idx_automation_github_issue_task_provenance_automation
    ON automation_github_issue_task_provenance(project_id, automation_id, issue_resource_id);

INSERT INTO automation_github_issue_task_provenance
    (project_id, automation_id, task_id, issue_resource_id, implementation_node_key, created_from_version_id, created_from_node_id)
SELECT activity.project_id, activity.automation_id, task_resource.resource_id, issue_resource.resource_id,
       node.node_key, activity.version_id, activity.node_id
FROM automation_activities activity
JOIN automation_nodes node ON node.id = activity.node_id
    AND node.version_id = activity.version_id
    AND node.automation_id = activity.automation_id
    AND node.project_id = activity.project_id
JOIN automation_activity_resources task_resource ON task_resource.activity_id = activity.id
    AND task_resource.resource_type = 'task'
    AND task_resource.relation = 'child'
JOIN tasks task ON task.id = task_resource.resource_id
    AND task.project_id = activity.project_id
JOIN automation_activity_resources issue_resource ON issue_resource.activity_id = activity.id
    AND issue_resource.resource_type = 'github_issue'
WHERE activity.activity_type = 'create_task'
  AND activity.work_item_id IS NOT NULL
  AND activity.activity_key = 'work-item:' || activity.work_item_id || ':implementation-task'
  AND node.node_type = 'agent_task'
  AND node.role IN ('task', 'implementation')
ON CONFLICT(project_id, task_id) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS automation_github_issue_task_provenance;

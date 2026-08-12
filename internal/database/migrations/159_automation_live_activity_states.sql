-- +goose Up
CREATE TABLE automation_live_activity_states (
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    state_key TEXT NOT NULL,
    activity_id TEXT NOT NULL,
    invocation_id TEXT,
    work_item_id TEXT,
    activity_status TEXT NOT NULL CHECK (activity_status IN ('pending','running','waiting','completed','failed','cancelled')),
    completed_at DATETIME,
    activity_rowid INTEGER NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (activity_id, version_id, automation_id, project_id)
      REFERENCES automation_activities(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (node_id, version_id, automation_id, project_id)
      REFERENCES automation_nodes(id, version_id, automation_id, project_id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, automation_id, version_id, node_id, state_key)
);

CREATE INDEX idx_automation_live_activity_states_activity ON automation_live_activity_states(activity_id);
CREATE INDEX idx_automation_live_activity_states_counts ON automation_live_activity_states(project_id, automation_id, version_id, activity_status, completed_at, node_id, state_key);
CREATE INDEX idx_automation_thread_input_bindings_live_counts ON automation_thread_input_bindings(project_id, automation_id, version_id, thread_input_id, node_id, work_item_id);
CREATE INDEX idx_automation_transitions_live_recent ON automation_transitions(project_id, automation_id, version_id, state, occurred_at, to_node_id, work_item_id);

WITH ranked_activities AS (
    SELECT a.rowid AS activity_rowid, a.id AS activity_id, a.project_id, a.automation_id, a.version_id, a.node_id,
        a.invocation_id, a.work_item_id, a.status AS activity_status, a.completed_at,
        CASE
            WHEN a.work_item_id IS NOT NULL THEN 'work:' || a.work_item_id
            WHEN task_resource.resource_id IS NOT NULL THEN 'task:' || task_resource.resource_id
            ELSE 'activity:' || a.id
        END AS state_key,
        ROW_NUMBER() OVER (PARTITION BY a.project_id, a.automation_id, a.version_id, a.node_id, CASE
            WHEN a.work_item_id IS NOT NULL THEN 'work:' || a.work_item_id
            WHEN task_resource.resource_id IS NOT NULL THEN 'task:' || task_resource.resource_id
            ELSE 'activity:' || a.id END
            ORDER BY a.rowid DESC) AS activity_rank
    FROM automation_activities a
    LEFT JOIN automation_activity_resources task_resource ON task_resource.activity_id = a.id
        AND task_resource.resource_type = 'task' AND task_resource.relation = 'subject'
)
INSERT INTO automation_live_activity_states
    (project_id, automation_id, version_id, node_id, state_key, activity_id, invocation_id, work_item_id, activity_status, completed_at, activity_rowid)
SELECT project_id, automation_id, version_id, node_id, state_key, activity_id, invocation_id, work_item_id, activity_status, completed_at, activity_rowid
FROM ranked_activities
WHERE activity_rank = 1;

-- +goose Down
DROP INDEX IF EXISTS idx_automation_transitions_live_recent;
DROP INDEX IF EXISTS idx_automation_thread_input_bindings_live_counts;
DROP INDEX IF EXISTS idx_automation_live_activity_states_counts;
DROP INDEX IF EXISTS idx_automation_live_activity_states_activity;
DROP TABLE IF EXISTS automation_live_activity_states;

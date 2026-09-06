-- +goose Up
-- Migration 175 treated every completed alert as linked, but completion was
-- historically allowed before task linkage. Keep only durable or explicit
-- legacy evidence that an implementation task was actually assigned.
UPDATE alerts AS alert
SET implementation_task_was_linked = 0
WHERE implementation_task_was_linked = 1
  AND implementation_task_id IS NULL
  AND processing_state <> 'implementation_task_linked'
  AND NOT (
      INSTR(LOWER(processing_error), 'linked implementation task') > 0
      OR INSTR(LOWER(processing_error), 'linked and started implementation task') > 0
      OR INSTR(LOWER(processing_error), 'created and started backlog implementation task') > 0
      OR (
          INSTR(LOWER(processing_error), 'created, linked,') > 0
          AND INSTR(LOWER(processing_error), 'implementation task') > 0
      )
  )
  AND NOT EXISTS (
      SELECT 1
      FROM automation_activities activity
      JOIN automation_activity_resources alert_resource
        ON alert_resource.activity_id = activity.id
       AND alert_resource.resource_type = 'alert'
       AND alert_resource.resource_id = alert.id
      JOIN automation_activity_resources task_resource
        ON task_resource.activity_id = activity.id
       AND task_resource.resource_type = 'task'
      WHERE activity.project_id = alert.project_id
  );

-- +goose Down
UPDATE alerts
SET implementation_task_was_linked = 1
WHERE processing_state = 'completed';

-- +goose Up
UPDATE alerts AS alert
SET implementation_task_was_linked = CASE
    WHEN implementation_task_id IS NOT NULL
      OR processing_state = 'implementation_task_linked'
      OR EXISTS (
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
            AND activity.activity_type = 'create_implementation_task'
      )
    THEN 1 ELSE 0
END;

-- +goose Down
UPDATE alerts AS alert
SET implementation_task_was_linked = CASE
    WHEN implementation_task_id IS NOT NULL
      OR processing_state = 'implementation_task_linked'
      OR INSTR(LOWER(processing_error), 'linked implementation task') > 0
      OR INSTR(LOWER(processing_error), 'linked and started implementation task') > 0
      OR INSTR(LOWER(processing_error), 'created and started backlog implementation task') > 0
      OR (
          INSTR(LOWER(processing_error), 'created, linked,') > 0
          AND INSTR(LOWER(processing_error), 'implementation task') > 0
      )
      OR EXISTS (
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
      )
    THEN 1 ELSE 0
END;

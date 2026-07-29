-- +goose Up
-- Per-schedule enablement is no longer an Automation setting. Active Automations
-- run all current owned schedules; Pause and Archive remain the disabling controls.
UPDATE schedules
SET enabled = 1,
    updated_at = CURRENT_TIMESTAMP
WHERE EXISTS (
    SELECT 1
    FROM automation_trigger_owners owner
    JOIN automations automation
      ON automation.id = owner.automation_id
     AND automation.project_id = owner.project_id
     AND automation.published_version_id = owner.version_id
    WHERE owner.schedule_id = schedules.id
      AND automation.lifecycle_state = 'active'
);

-- +goose Down
SELECT 1;

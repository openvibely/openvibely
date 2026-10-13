-- +goose Up
-- Remove exclusively owned Scheduler rows while retained draft/current-version
-- provenance still identifies them. Schedules reference preserved domain Tasks,
-- so deleting graph rows alone would otherwise orphan runnable schedules.
DELETE FROM schedules
WHERE id IN (
    SELECT owner.schedule_id
    FROM automation_trigger_owners owner
    JOIN automations automation
      ON automation.id = owner.automation_id
     AND automation.project_id = owner.project_id
    WHERE automation.published_version_id IS NULL
       OR owner.version_id <> automation.published_version_id
    UNION
    SELECT resource.resource_id
    FROM automation_definition_resources resource
    JOIN automations automation
      ON automation.id = resource.automation_id
     AND automation.project_id = resource.project_id
    WHERE resource.resource_type = 'schedule'
      AND resource.relation = 'owned'
      AND (automation.published_version_id IS NULL
       OR resource.version_id <> automation.published_version_id)
)
AND NOT EXISTS (
    SELECT 1
    FROM automation_definition_resources current_resource
    JOIN automations current_automation
      ON current_automation.id = current_resource.automation_id
     AND current_automation.project_id = current_resource.project_id
    WHERE current_resource.resource_type = 'schedule'
      AND current_resource.resource_id = schedules.id
      AND current_automation.published_version_id = current_resource.version_id
);

-- Remove persisted publication-era drafts. Automation Graphs now retain exactly
-- one current saved graph and browser-local unsaved candidates are not database
-- identities.
DELETE FROM automations
WHERE published_version_id IS NULL;

DELETE FROM automation_versions
WHERE EXISTS (
    SELECT 1
    FROM automations
    WHERE automations.id = automation_versions.automation_id
      AND automations.project_id = automation_versions.project_id
      AND automations.published_version_id IS NOT NULL
      AND automations.published_version_id <> automation_versions.id
);

UPDATE automation_versions
SET version = 1,
    state = 'published'
WHERE EXISTS (
    SELECT 1
    FROM automations
    WHERE automations.id = automation_versions.automation_id
      AND automations.project_id = automation_versions.project_id
      AND automations.published_version_id = automation_versions.id
);

-- Pause needs to remember which activity-created child Tasks were admitted before
-- demotion. A Task's Backlog category alone cannot distinguish configured
-- Backlog work from work made non-runnable by lifecycle deactivation.
CREATE TABLE automation_paused_task_admissions (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    version_id TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (automation_id, project_id)
      REFERENCES automations(id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (version_id, automation_id, project_id)
      REFERENCES automation_versions(id, automation_id, project_id) ON DELETE CASCADE
);

CREATE INDEX idx_automation_paused_task_admissions_parent
  ON automation_paused_task_admissions(project_id, automation_id, version_id);

-- +goose Down
DROP INDEX IF EXISTS idx_automation_paused_task_admissions_parent;
DROP TABLE IF EXISTS automation_paused_task_admissions;

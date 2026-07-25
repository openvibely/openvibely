-- +goose Up
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

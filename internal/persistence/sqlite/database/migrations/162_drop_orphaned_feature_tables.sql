-- +goose Up
DROP INDEX IF EXISTS idx_automation_activity_resources_activity_type;
DROP INDEX IF EXISTS idx_automation_activity_resources_type_resource_activity;
DROP INDEX IF EXISTS idx_automation_activity_resources_activity_page;
DROP INDEX IF EXISTS idx_automation_activity_resources_reverse;

CREATE TABLE automation_activity_resources_new (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    activity_id TEXT NOT NULL REFERENCES automation_activities(id) ON DELETE CASCADE,
    resource_type TEXT NOT NULL CHECK (resource_type IN ('task','execution','alert','goal','github_issue','pull_request','review')),
    resource_id TEXT NOT NULL,
    relation TEXT NOT NULL DEFAULT 'subject',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(activity_id, resource_type, resource_id, relation)
);

INSERT INTO automation_activity_resources_new (id, activity_id, resource_type, resource_id, relation, created_at)
SELECT id, activity_id, resource_type, resource_id, relation, created_at
FROM automation_activity_resources
WHERE resource_type <> 'workflow_execution';

DROP TABLE automation_activity_resources;
ALTER TABLE automation_activity_resources_new RENAME TO automation_activity_resources;

CREATE INDEX idx_automation_activity_resources_reverse
ON automation_activity_resources(resource_type, resource_id);

CREATE INDEX idx_automation_activity_resources_activity_page
ON automation_activity_resources(activity_id, id DESC);

CREATE INDEX idx_automation_activity_resources_type_resource_activity
ON automation_activity_resources(resource_type, resource_id, activity_id);

CREATE INDEX idx_automation_activity_resources_activity_type
ON automation_activity_resources(activity_id, resource_type);

DROP TABLE IF EXISTS pattern_usage_history;
DROP TABLE IF EXISTS prompt_patterns;
DROP TABLE IF EXISTS competitor_updates;
DROP TABLE IF EXISTS trend_patterns;
DROP TABLE IF EXISTS trend_entries;
DROP TABLE IF EXISTS trend_sources;
DROP TABLE IF EXISTS x_credentials;
DROP TABLE IF EXISTS task_templates;
DROP TABLE IF EXISTS autonomous_sandboxes;
DROP TABLE IF EXISTS autonomous_build_logs;
DROP TABLE IF EXISTS autonomous_features;
DROP TABLE IF EXISTS autonomous_config;
DROP TABLE IF EXISTS autonomous_builds;
DROP TABLE IF EXISTS backlog_analysis_reports;
DROP TABLE IF EXISTS backlog_health_snapshots;
DROP TABLE IF EXISTS backlog_suggestions;
DROP TABLE IF EXISTS architect_tasks;
DROP TABLE IF EXISTS architect_messages;
DROP TABLE IF EXISTS architect_templates;
DROP TABLE IF EXISTS architect_sessions;
DROP TABLE IF EXISTS vote_records;
DROP TABLE IF EXISTS step_executions;
DROP TABLE IF EXISTS workflow_executions;
DROP TABLE IF EXISTS workflow_steps;
DROP TABLE IF EXISTS workflows;
DROP TABLE IF EXISTS workflow_templates;
DROP TABLE IF EXISTS agent_performance_metrics;
DROP TABLE IF EXISTS agent_leaderboard_snapshots;

-- +goose Down
DROP INDEX IF EXISTS idx_automation_activity_resources_activity_type;
DROP INDEX IF EXISTS idx_automation_activity_resources_type_resource_activity;
DROP INDEX IF EXISTS idx_automation_activity_resources_activity_page;
DROP INDEX IF EXISTS idx_automation_activity_resources_reverse;

CREATE TABLE automation_activity_resources_old (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    activity_id TEXT NOT NULL REFERENCES automation_activities(id) ON DELETE CASCADE,
    resource_type TEXT NOT NULL CHECK (resource_type IN ('task','execution','alert','goal','github_issue','pull_request','review','workflow_execution')),
    resource_id TEXT NOT NULL,
    relation TEXT NOT NULL DEFAULT 'subject',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(activity_id, resource_type, resource_id, relation)
);

INSERT INTO automation_activity_resources_old (id, activity_id, resource_type, resource_id, relation, created_at)
SELECT id, activity_id, resource_type, resource_id, relation, created_at
FROM automation_activity_resources;

DROP TABLE automation_activity_resources;
ALTER TABLE automation_activity_resources_old RENAME TO automation_activity_resources;

CREATE INDEX idx_automation_activity_resources_reverse
ON automation_activity_resources(resource_type, resource_id);

CREATE INDEX idx_automation_activity_resources_activity_page
ON automation_activity_resources(activity_id, id DESC);

CREATE INDEX idx_automation_activity_resources_type_resource_activity
ON automation_activity_resources(resource_type, resource_id, activity_id);

CREATE INDEX idx_automation_activity_resources_activity_type
ON automation_activity_resources(activity_id, resource_type);

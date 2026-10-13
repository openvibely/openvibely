-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS skill_analytics_events (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    project_id   TEXT REFERENCES projects(id) ON DELETE SET NULL,
    task_id      TEXT REFERENCES tasks(id) ON DELETE SET NULL,
    execution_id TEXT REFERENCES executions(id) ON DELETE SET NULL,
    thread_id    TEXT,
    agent_id     TEXT REFERENCES agents(id) ON DELETE SET NULL,
    skill_scope  TEXT NOT NULL CHECK (skill_scope IN ('project','global','agent_owned')),
    skill_handle TEXT NOT NULL,
    event_type   TEXT NOT NULL CHECK (event_type IN ('selected','loaded','viewed','created','edited')),
    source       TEXT NOT NULL CHECK (source IN ('skill_curator','always_use','manual','assigned_agent','lifecycle_hook','system')),
    surface      TEXT NOT NULL CHECK (surface IN ('chat','task_thread','scheduled_task','lifecycle_hook','channel','goal_continuation'))
);

CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_created_at ON skill_analytics_events(created_at);
CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_skill ON skill_analytics_events(skill_handle, event_type, created_at);
CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_project ON skill_analytics_events(project_id, created_at);
CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_agent ON skill_analytics_events(agent_id, created_at);
CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_turn ON skill_analytics_events(execution_id, thread_id, task_id, skill_handle, event_type);
CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_surface ON skill_analytics_events(surface, created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_skill_analytics_events_surface;
DROP INDEX IF EXISTS idx_skill_analytics_events_turn;
DROP INDEX IF EXISTS idx_skill_analytics_events_agent;
DROP INDEX IF EXISTS idx_skill_analytics_events_project;
DROP INDEX IF EXISTS idx_skill_analytics_events_skill;
DROP INDEX IF EXISTS idx_skill_analytics_events_created_at;
DROP TABLE IF EXISTS skill_analytics_events;
-- +goose StatementEnd

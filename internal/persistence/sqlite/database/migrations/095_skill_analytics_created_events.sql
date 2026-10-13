-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;

ALTER TABLE skill_analytics_events RENAME TO skill_analytics_events_095_old;

CREATE TABLE skill_analytics_events (
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

INSERT INTO skill_analytics_events (
    id, created_at, project_id, task_id, execution_id, thread_id, agent_id,
    skill_scope, skill_handle, event_type, source, surface
)
SELECT id, created_at, project_id, task_id, execution_id, thread_id, agent_id,
       skill_scope, skill_handle, event_type, source, surface
FROM skill_analytics_events_095_old;

DROP TABLE skill_analytics_events_095_old;

CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_created_at ON skill_analytics_events(created_at);
CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_skill ON skill_analytics_events(skill_handle, event_type, created_at);
CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_project ON skill_analytics_events(project_id, created_at);
CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_agent ON skill_analytics_events(agent_id, created_at);
CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_turn ON skill_analytics_events(execution_id, thread_id, task_id, skill_handle, event_type);
CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_surface ON skill_analytics_events(surface, created_at);

PRAGMA foreign_keys=ON;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;

ALTER TABLE skill_analytics_events RENAME TO skill_analytics_events_095_new;

CREATE TABLE skill_analytics_events (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    project_id   TEXT REFERENCES projects(id) ON DELETE SET NULL,
    task_id      TEXT REFERENCES tasks(id) ON DELETE SET NULL,
    execution_id TEXT REFERENCES executions(id) ON DELETE SET NULL,
    thread_id    TEXT,
    agent_id     TEXT REFERENCES agents(id) ON DELETE SET NULL,
    skill_scope  TEXT NOT NULL CHECK (skill_scope IN ('project','global','agent_owned')),
    skill_handle TEXT NOT NULL,
    event_type   TEXT NOT NULL CHECK (event_type IN ('selected','loaded','viewed','edited')),
    source       TEXT NOT NULL CHECK (source IN ('skill_curator','always_use','manual','assigned_agent','lifecycle_hook','system')),
    surface      TEXT NOT NULL CHECK (surface IN ('chat','task_thread','scheduled_task','lifecycle_hook','channel','goal_continuation'))
);

INSERT INTO skill_analytics_events (
    id, created_at, project_id, task_id, execution_id, thread_id, agent_id,
    skill_scope, skill_handle, event_type, source, surface
)
SELECT id, created_at, project_id, task_id, execution_id, thread_id, agent_id,
       skill_scope, skill_handle,
       CASE WHEN event_type = 'created' THEN 'edited' ELSE event_type END,
       source, surface
FROM skill_analytics_events_095_new;

DROP TABLE skill_analytics_events_095_new;

CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_created_at ON skill_analytics_events(created_at);
CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_skill ON skill_analytics_events(skill_handle, event_type, created_at);
CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_project ON skill_analytics_events(project_id, created_at);
CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_agent ON skill_analytics_events(agent_id, created_at);
CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_turn ON skill_analytics_events(execution_id, thread_id, task_id, skill_handle, event_type);
CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_surface ON skill_analytics_events(surface, created_at);

PRAGMA foreign_keys=ON;
-- +goose StatementEnd

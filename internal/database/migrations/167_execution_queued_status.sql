-- +goose Up
-- +goose NO TRANSACTION

PRAGMA foreign_keys=OFF;
PRAGMA legacy_alter_table=ON;

DROP INDEX IF EXISTS idx_executions_chat_active_project_status_started;
DROP INDEX IF EXISTS idx_executions_chat_history_project_started;
DROP INDEX IF EXISTS idx_executions_task_started_at;
DROP INDEX IF EXISTS idx_executions_dispatch_id;
DROP INDEX IF EXISTS idx_executions_started_at;
DROP INDEX IF EXISTS idx_executions_agent_status;
DROP INDEX IF EXISTS idx_executions_task_status;
DROP INDEX IF EXISTS idx_executions_status;
DROP INDEX IF EXISTS idx_executions_task_id;
DROP TRIGGER IF EXISTS executions_history_metadata_task_update;
DROP TRIGGER IF EXISTS executions_history_metadata_task_id_update;
DROP TRIGGER IF EXISTS executions_history_metadata_insert;

ALTER TABLE executions RENAME TO executions_old;

CREATE TABLE executions (
    id              TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    task_id         TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_config_id TEXT REFERENCES agent_configs(id),
    status          TEXT NOT NULL DEFAULT 'running'
                    CHECK (status IN ('queued', 'running', 'completed', 'failed', 'cancelled')),
    prompt_sent     TEXT NOT NULL DEFAULT '',
    output          TEXT NOT NULL DEFAULT '',
    error_message   TEXT NOT NULL DEFAULT '',
    tokens_used     INTEGER NOT NULL DEFAULT 0,
    duration_ms     INTEGER NOT NULL DEFAULT 0,
    started_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    completed_at    DATETIME,
    is_followup     INTEGER NOT NULL DEFAULT 0,
    diff_output     TEXT NOT NULL DEFAULT '',
    cli_session_id  TEXT NOT NULL DEFAULT '',
    dispatch_id     TEXT,
    reasoning_content TEXT NOT NULL DEFAULT '',
    starts_new_context INTEGER NOT NULL DEFAULT 0,
    task_project_id TEXT NOT NULL DEFAULT '',
    task_category   TEXT NOT NULL DEFAULT '',
    history_order   INTEGER NOT NULL DEFAULT 0
);

INSERT INTO executions (
    id, task_id, agent_config_id, status, prompt_sent, output, error_message,
    tokens_used, duration_ms, started_at, completed_at, is_followup,
    diff_output, cli_session_id, dispatch_id, reasoning_content,
    starts_new_context, task_project_id, task_category, history_order
)
SELECT
    id, task_id, agent_config_id, status, prompt_sent, output, error_message,
    tokens_used, duration_ms, started_at, completed_at, is_followup,
    diff_output, cli_session_id, dispatch_id, reasoning_content,
    starts_new_context, task_project_id, task_category, history_order
FROM executions_old;

DROP TABLE executions_old;

CREATE INDEX idx_executions_task_id ON executions(task_id);
CREATE INDEX idx_executions_status ON executions(status);
CREATE INDEX idx_executions_task_status ON executions(task_id, status);
CREATE INDEX idx_executions_agent_status ON executions(agent_config_id, status);
CREATE INDEX idx_executions_started_at ON executions(started_at);
CREATE INDEX IF NOT EXISTS idx_executions_task_started_at ON executions(task_id, started_at);
CREATE UNIQUE INDEX idx_executions_dispatch_id ON executions(dispatch_id) WHERE dispatch_id IS NOT NULL;

-- +goose StatementBegin
CREATE TRIGGER executions_history_metadata_insert
AFTER INSERT ON executions
WHEN NEW.task_project_id = '' OR NEW.task_category = '' OR NEW.history_order = 0
BEGIN
    UPDATE executions
    SET task_project_id = COALESCE((SELECT project_id FROM tasks WHERE tasks.id = NEW.task_id), ''),
        task_category = COALESCE((SELECT category FROM tasks WHERE tasks.id = NEW.task_id), ''),
        history_order = NEW.rowid
    WHERE rowid = NEW.rowid;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER executions_history_metadata_task_id_update
AFTER UPDATE OF task_id ON executions
BEGIN
    UPDATE executions
    SET task_project_id = COALESCE((SELECT project_id FROM tasks WHERE tasks.id = NEW.task_id), ''),
        task_category = COALESCE((SELECT category FROM tasks WHERE tasks.id = NEW.task_id), '')
    WHERE rowid = NEW.rowid;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER executions_history_metadata_task_update
AFTER UPDATE OF project_id, category ON tasks
BEGIN
    UPDATE executions
    SET task_project_id = NEW.project_id,
        task_category = NEW.category
    WHERE task_id = NEW.id;
END;
-- +goose StatementEnd

CREATE INDEX idx_executions_chat_history_project_started
    ON executions(task_project_id, task_category, started_at DESC, history_order DESC);

CREATE INDEX idx_executions_chat_active_project_status_started
    ON executions(task_project_id, task_category, status, started_at DESC, history_order DESC);

PRAGMA foreign_keys=ON;
PRAGMA legacy_alter_table=OFF;

-- +goose Down
-- Downgrades keep the wider status constraint. Existing queued executions are
-- converted to running so older code can still read them.
UPDATE executions SET status = 'running' WHERE status = 'queued';

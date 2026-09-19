-- +goose Up
CREATE TABLE openai_async_tool_calls (
    id                  TEXT PRIMARY KEY,
    project_id          TEXT NOT NULL DEFAULT '',
    task_id             TEXT NOT NULL DEFAULT '',
    execution_id        TEXT NOT NULL DEFAULT '',
    response_id         TEXT NOT NULL DEFAULT '',
    call_id             TEXT NOT NULL,
    tool_name           TEXT NOT NULL,
    arguments_json      TEXT NOT NULL DEFAULT '{}',
    status              TEXT NOT NULL CHECK (status IN ('pending', 'running', 'completed', 'delivered', 'failed', 'cancelled', 'expired', 'provider_rejected', 'stale')),
    result              TEXT NOT NULL DEFAULT '',
    is_error            INTEGER NOT NULL DEFAULT 0,
    error_message       TEXT NOT NULL DEFAULT '',
    deliver_attempts    INTEGER NOT NULL DEFAULT 0,
    deadline_at         DATETIME NOT NULL,
    delivered_at        DATETIME,
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (execution_id) REFERENCES executions(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_openai_async_tool_calls_execution_call
    ON openai_async_tool_calls(execution_id, call_id);

CREATE INDEX idx_openai_async_tool_calls_status_deadline
    ON openai_async_tool_calls(status, deadline_at, created_at);

CREATE INDEX idx_openai_async_tool_calls_project_task
    ON openai_async_tool_calls(project_id, task_id, status);

-- +goose Down
DROP INDEX IF EXISTS idx_openai_async_tool_calls_project_task;
DROP INDEX IF EXISTS idx_openai_async_tool_calls_status_deadline;
DROP INDEX IF EXISTS idx_openai_async_tool_calls_execution_call;
DROP TABLE IF EXISTS openai_async_tool_calls;

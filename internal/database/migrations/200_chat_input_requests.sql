-- +goose Up
CREATE TABLE chat_input_requests (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    execution_id TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
    questions_json TEXT NOT NULL,
    answers_json TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'resolved')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL,
    resolved_at DATETIME
);

CREATE INDEX idx_chat_input_requests_project_status_expires
    ON chat_input_requests(project_id, status, expires_at, created_at);

CREATE INDEX idx_chat_input_requests_execution
    ON chat_input_requests(execution_id);

-- +goose Down
DROP INDEX IF EXISTS idx_chat_input_requests_execution;
DROP INDEX IF EXISTS idx_chat_input_requests_project_status_expires;
DROP TABLE IF EXISTS chat_input_requests;

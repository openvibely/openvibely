-- +goose Up
CREATE TABLE thread_inputs (
    id                    TEXT PRIMARY KEY,
    scope                 TEXT NOT NULL CHECK (scope IN ('chat', 'task_thread')),
    project_id            TEXT NOT NULL,
    task_id               TEXT,
    run_execution_id      TEXT,
    agent_config_id       TEXT,
    input_mode            TEXT NOT NULL CHECK (input_mode IN ('queued', 'steering')),
    input_status          TEXT NOT NULL DEFAULT 'pending' CHECK (input_status IN ('pending', 'applied', 'cancelled')),
    turn_id               TEXT,
    expected_turn_id      TEXT,
    content               TEXT NOT NULL,
    attachment_session_id TEXT,
    queue_position        INTEGER NOT NULL,
    chat_mode             TEXT,
    source                TEXT NOT NULL DEFAULT '',
    telegram_chat_id      INTEGER NOT NULL DEFAULT 0,
    slack_team_id         TEXT NOT NULL DEFAULT '',
    slack_channel_id      TEXT NOT NULL DEFAULT '',
    slack_thread_ts       TEXT NOT NULL DEFAULT '',
    slack_user_id         TEXT NOT NULL DEFAULT '',
    created_at            DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at            DATETIME DEFAULT CURRENT_TIMESTAMP,
    applied_at            DATETIME,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
    FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    FOREIGN KEY (run_execution_id) REFERENCES executions(id) ON DELETE SET NULL,
    FOREIGN KEY (agent_config_id) REFERENCES agent_configs(id) ON DELETE SET NULL
);

CREATE INDEX idx_thread_inputs_pending_task ON thread_inputs(task_id, input_status, input_mode, queue_position, created_at);
CREATE INDEX idx_thread_inputs_pending_chat ON thread_inputs(scope, project_id, input_status, input_mode, queue_position, created_at);
CREATE INDEX idx_thread_inputs_steering_turn ON thread_inputs(run_execution_id, turn_id, input_mode, input_status, created_at);

-- +goose Down
DROP TABLE IF EXISTS thread_inputs;

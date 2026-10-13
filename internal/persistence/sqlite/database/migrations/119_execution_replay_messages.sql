-- +goose Up
CREATE TABLE execution_replay_messages (
    execution_id TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    user_content TEXT NOT NULL DEFAULT '',
    assistant_content TEXT NOT NULL DEFAULT '',
    reasoning_content TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (execution_id, sequence)
);

-- +goose Down
DROP TABLE execution_replay_messages;

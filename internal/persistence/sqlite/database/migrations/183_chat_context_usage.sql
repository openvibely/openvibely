-- +goose Up
CREATE TABLE chat_context_usage (
 scope_type TEXT NOT NULL,
 scope_id TEXT NOT NULL,
 model_key TEXT NOT NULL,
 source_execution_id TEXT NOT NULL,
 context_tokens INTEGER NOT NULL,
 overhead_tokens INTEGER NOT NULL,
 PRIMARY KEY (scope_type, scope_id, model_key)
);
-- +goose Down
DROP TABLE chat_context_usage;

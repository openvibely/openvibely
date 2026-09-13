-- +goose Up
ALTER TABLE thread_inputs ADD COLUMN retry_source_execution_id TEXT NOT NULL DEFAULT '';
ALTER TABLE chat_compaction_checkpoints ADD COLUMN compatibility_key TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE chat_compaction_checkpoints DROP COLUMN compatibility_key;
ALTER TABLE thread_inputs DROP COLUMN retry_source_execution_id;

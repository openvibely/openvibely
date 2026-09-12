-- +goose Up
ALTER TABLE chat_compaction_checkpoints ADD COLUMN provider_state_json TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE chat_compaction_checkpoints DROP COLUMN provider_state_json;

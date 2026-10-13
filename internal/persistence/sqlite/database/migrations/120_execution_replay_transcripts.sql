-- +goose Up
ALTER TABLE execution_replay_messages ADD COLUMN transcript_json TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE execution_replay_messages DROP COLUMN transcript_json;

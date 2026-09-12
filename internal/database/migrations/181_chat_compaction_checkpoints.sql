-- +goose Up
CREATE TABLE chat_compaction_checkpoints (
  scope_type TEXT NOT NULL,
  scope_id TEXT NOT NULL,
  model_config_id TEXT NOT NULL DEFAULT '',
  source_execution_id TEXT NOT NULL DEFAULT '',
  history_json TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  strategy TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (scope_type, scope_id)
);

CREATE INDEX idx_chat_compaction_checkpoints_updated
  ON chat_compaction_checkpoints(updated_at DESC);

ALTER TABLE agent_configs ADD COLUMN context_window INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_configs ADD COLUMN compaction_threshold INTEGER NOT NULL DEFAULT 0;

-- +goose Down
DROP INDEX IF EXISTS idx_chat_compaction_checkpoints_updated;
DROP TABLE IF EXISTS chat_compaction_checkpoints;
ALTER TABLE agent_configs DROP COLUMN compaction_threshold;
ALTER TABLE agent_configs DROP COLUMN context_window;

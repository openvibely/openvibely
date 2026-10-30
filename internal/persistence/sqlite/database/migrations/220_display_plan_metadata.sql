-- +goose Up
-- A batch needs to invalidate a cached projection only once.
DROP TRIGGER display_digest_insert;
-- +goose StatementBegin
CREATE TRIGGER display_digest_insert AFTER INSERT ON conversation_items
WHEN NEW.execution_id IS NOT NULL AND NEW.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=NEW.execution_id AND display_digest IS NOT NULL;
END;
-- +goose StatementEnd
DROP TRIGGER display_digest_update;
-- +goose StatementBegin
CREATE TRIGGER display_digest_update AFTER UPDATE ON conversation_items
WHEN NEW.execution_id IS NOT NULL AND NEW.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=NEW.execution_id AND display_digest IS NOT NULL;
END;
-- +goose StatementEnd
DROP TRIGGER display_digest_delete;
-- +goose StatementBegin
CREATE TRIGGER display_digest_delete AFTER DELETE ON conversation_items
WHEN OLD.execution_id IS NOT NULL AND OLD.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=OLD.execution_id AND display_digest IS NOT NULL;
END;
-- +goose StatementEnd
-- Each owner only needs its own indexes; exclude the other owner's NULL keys.
DROP INDEX conversation_execution_changes;
CREATE INDEX conversation_execution_changes ON conversation_items(execution_id, lane, sequence) WHERE execution_id IS NOT NULL;
DROP INDEX conversation_checkpoint_changes;
CREATE INDEX conversation_checkpoint_changes ON conversation_items(checkpoint_scope_type, checkpoint_scope_id, lane, sequence) WHERE checkpoint_scope_type IS NOT NULL;
DROP INDEX conversation_execution_item;
CREATE INDEX conversation_execution_item ON conversation_items(execution_id, generation, lane, item_id, sequence) WHERE execution_id IS NOT NULL;
DROP INDEX conversation_checkpoint_item;
CREATE INDEX conversation_checkpoint_item ON conversation_items(checkpoint_scope_type, checkpoint_scope_id, generation, lane, item_id, sequence) WHERE checkpoint_scope_type IS NOT NULL;
DROP INDEX conversation_execution_page;
CREATE INDEX conversation_execution_page ON conversation_items(execution_id, generation, lane, position, item_id) WHERE execution_id IS NOT NULL AND is_current = 1;
DROP INDEX conversation_checkpoint_page;
CREATE INDEX conversation_checkpoint_page ON conversation_items(checkpoint_scope_type, checkpoint_scope_id, generation, lane, position, item_id) WHERE checkpoint_scope_type IS NOT NULL AND is_current = 1;
-- Restore reads headers and provider items in frame order without sorting their
-- potentially multi-megabyte payloads in a temporary SQLite B-tree.
CREATE INDEX conversation_replay_order ON conversation_items(execution_id,item_id)
 WHERE generation=0 AND is_current=1 AND operation='put' AND lane IN ('provider','metadata');
ALTER TABLE executions ADD COLUMN display_has_plan INTEGER;
-- +goose StatementBegin
CREATE TRIGGER display_plan_invalidate AFTER UPDATE OF display_digest ON executions
WHEN NEW.display_digest IS NULL
BEGIN
 UPDATE executions SET display_has_plan=NULL WHERE id=NEW.id;
END;
-- +goose StatementEnd

ALTER TABLE chat_compaction_checkpoints ADD COLUMN history_digest BLOB;
-- +goose StatementBegin
CREATE TRIGGER checkpoint_digest_insert AFTER INSERT ON conversation_items
WHEN NEW.checkpoint_scope_type IS NOT NULL
BEGIN
 UPDATE chat_compaction_checkpoints SET history_digest=NULL
 WHERE scope_type=NEW.checkpoint_scope_type AND scope_id=NEW.checkpoint_scope_id AND history_digest IS NOT NULL;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER checkpoint_digest_update AFTER UPDATE ON conversation_items
WHEN NEW.checkpoint_scope_type IS NOT NULL
BEGIN
 UPDATE chat_compaction_checkpoints SET history_digest=NULL
 WHERE scope_type=NEW.checkpoint_scope_type AND scope_id=NEW.checkpoint_scope_id AND history_digest IS NOT NULL;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER checkpoint_digest_delete AFTER DELETE ON conversation_items
WHEN OLD.checkpoint_scope_type IS NOT NULL
BEGIN
 UPDATE chat_compaction_checkpoints SET history_digest=NULL
 WHERE scope_type=OLD.checkpoint_scope_type AND scope_id=OLD.checkpoint_scope_id AND history_digest IS NOT NULL;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER display_digest_insert;
-- +goose StatementBegin
CREATE TRIGGER display_digest_insert AFTER INSERT ON conversation_items
WHEN NEW.execution_id IS NOT NULL AND NEW.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=NEW.execution_id;
END;
-- +goose StatementEnd
DROP TRIGGER display_digest_update;
-- +goose StatementBegin
CREATE TRIGGER display_digest_update AFTER UPDATE ON conversation_items
WHEN NEW.execution_id IS NOT NULL AND NEW.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=NEW.execution_id;
END;
-- +goose StatementEnd
DROP TRIGGER display_digest_delete;
-- +goose StatementBegin
CREATE TRIGGER display_digest_delete AFTER DELETE ON conversation_items
WHEN OLD.execution_id IS NOT NULL AND OLD.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=OLD.execution_id;
END;
-- +goose StatementEnd
DROP TRIGGER checkpoint_digest_insert;
DROP TRIGGER checkpoint_digest_update;
DROP TRIGGER checkpoint_digest_delete;
ALTER TABLE chat_compaction_checkpoints DROP COLUMN history_digest;
DROP INDEX conversation_execution_changes;
CREATE INDEX conversation_execution_changes ON conversation_items(execution_id, lane, sequence);
DROP INDEX conversation_checkpoint_changes;
CREATE INDEX conversation_checkpoint_changes ON conversation_items(checkpoint_scope_type, checkpoint_scope_id, lane, sequence);
DROP INDEX conversation_execution_item;
CREATE INDEX conversation_execution_item ON conversation_items(execution_id, generation, lane, item_id, sequence);
DROP INDEX conversation_checkpoint_item;
CREATE INDEX conversation_checkpoint_item ON conversation_items(checkpoint_scope_type, checkpoint_scope_id, generation, lane, item_id, sequence);
DROP INDEX conversation_execution_page;
CREATE INDEX conversation_execution_page ON conversation_items(execution_id, generation, lane, position, item_id) WHERE is_current = 1;
DROP INDEX conversation_checkpoint_page;
CREATE INDEX conversation_checkpoint_page ON conversation_items(checkpoint_scope_type, checkpoint_scope_id, generation, lane, position, item_id) WHERE is_current = 1;
DROP INDEX conversation_replay_order;
-- Older display readers require an explicit self pointer.
UPDATE conversation_items SET snapshot_sequence=sequence WHERE lane='display' AND snapshot_sequence=0;
DROP TRIGGER display_plan_invalidate;
ALTER TABLE executions DROP COLUMN display_has_plan;

-- +goose Up
-- Only unfinished output lives here. Offsets are relative to this execution's
-- pending suffix; finalized display items remain independently pageable.
CREATE TABLE execution_stream_text (
 execution_id TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
 end_offset INTEGER NOT NULL,
 text TEXT NOT NULL,
 PRIMARY KEY(execution_id,end_offset)
) WITHOUT ROWID;
ALTER TABLE executions ADD COLUMN display_digest BLOB;

-- +goose StatementBegin
CREATE TRIGGER display_digest_insert AFTER INSERT ON conversation_items
WHEN NEW.execution_id IS NOT NULL AND NEW.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=NEW.execution_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER display_digest_update AFTER UPDATE ON conversation_items
WHEN NEW.execution_id IS NOT NULL AND NEW.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=NEW.execution_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER display_digest_delete AFTER DELETE ON conversation_items
WHEN OLD.execution_id IS NOT NULL AND OLD.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=OLD.execution_id;
END;
-- +goose StatementEnd

-- +goose Down
-- Preserve pending output as ordinary display items before dropping the buffer.
INSERT INTO conversation_items(execution_id,generation,lane,item_id,position,operation,payload,mutation_id,display_bytes)
SELECT execution_id,0,'display','stream-downgrade:' || hex(randomblob(16)),
 COALESCE((SELECT max(position)+1 FROM conversation_items c WHERE c.execution_id=s.execution_id AND c.generation=0 AND c.lane='display' AND c.is_current=1),0)+end_offset,
 'put',json_object('type','text','text',text,'complete',json('true')),
 'stream-downgrade:' || hex(randomblob(16)),length(CAST(text AS BLOB))
FROM execution_stream_text s ORDER BY execution_id,end_offset;
UPDATE conversation_items SET snapshot_sequence=sequence WHERE item_id LIKE 'stream-downgrade:%' AND snapshot_sequence=0;
DROP TRIGGER display_digest_insert;
DROP TRIGGER display_digest_update;
DROP TRIGGER display_digest_delete;
DROP TABLE execution_stream_text;
ALTER TABLE executions DROP COLUMN display_digest;

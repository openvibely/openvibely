package migrations

// These unregistered SQL steps support the single transcript migration and
// upgrades of development databases that used its unpublished intermediate IDs.
import (
	"context"
	"database/sql"
	"fmt"
)

func ApplyTranscriptDevelopmentStep(ctx context.Context, tx *sql.Tx, version int64, up bool) error {
	if version == 217 {
		if up {
			return upConversationItems217(ctx, tx)
		}
		return downConversationItems217(ctx, tx)
	}
	var statement string
	switch version {
	case 218:
		if up {
			statement = legacyUp218
		} else {
			statement = legacyDown218
		}
	case 219:
		if up {
			statement = legacyUp219
		} else {
			statement = legacyDown219
		}
	case 220:
		if up {
			statement = legacyUp220
		} else {
			statement = legacyDown220
		}
	case 221:
		if up {
			statement = legacyUp221
		} else {
			statement = legacyDown221
		}
	default:
		return fmt.Errorf("unknown transcript development step %d", version)
	}
	_, err := tx.ExecContext(ctx, statement)
	return err
}
func upTranscriptStore219(ctx context.Context, tx *sql.Tx) error {
	for v := int64(217); v <= 221; v++ {
		if err := ApplyTranscriptDevelopmentStep(ctx, tx, v, true); err != nil {
			return err
		}
	}
	return nil
}
func downTranscriptStore219(ctx context.Context, tx *sql.Tx) error {
	for v := int64(221); v >= 217; v-- {
		if err := ApplyTranscriptDevelopmentStep(ctx, tx, v, false); err != nil {
			return err
		}
	}
	return nil
}

const legacyUp218 = `
-- New provider writes index their compact JSON so checkpoints can locate retained
-- items without loading the execution's entire replay. Older rows safely fall
-- back to checkpoint-owned payloads; upgrading needs no full transcript scan.
ALTER TABLE conversation_items ADD COLUMN payload_hash BLOB;
CREATE INDEX conversation_provider_payload ON conversation_items(execution_id,payload_hash)
 WHERE generation=0 AND lane='provider' AND is_current=1 AND operation='put';

-- Checkpoints borrow immutable execution revisions. Removing a source revision
-- materializes its borrowers before the delete, including execution cascades.
CREATE TABLE conversation_checkpoint_references (
    checkpoint_sequence INTEGER PRIMARY KEY REFERENCES conversation_items(sequence) ON DELETE CASCADE,
    source_sequence INTEGER NOT NULL REFERENCES conversation_items(sequence) ON DELETE RESTRICT,
    CHECK(checkpoint_sequence != source_sequence)
);
CREATE INDEX conversation_checkpoint_source ON conversation_checkpoint_references(source_sequence);


CREATE TRIGGER conversation_checkpoint_materialize BEFORE DELETE ON conversation_items
BEGIN
    UPDATE conversation_items SET payload=OLD.payload
    WHERE sequence IN (SELECT checkpoint_sequence FROM conversation_checkpoint_references WHERE source_sequence=OLD.sequence);
    DELETE FROM conversation_checkpoint_references WHERE source_sequence=OLD.sequence;
END;


CREATE VIEW conversation_resolved_items AS
SELECT item.sequence, item.execution_id, item.checkpoint_scope_type, item.checkpoint_scope_id,
       item.generation, item.lane, item.item_id, item.position, item.operation,
       COALESCE(source.payload,item.payload) AS payload,
       item.previous_revision, item.mutation_id, item.snapshot_sequence, item.is_current, item.display_bytes
FROM conversation_items item
LEFT JOIN conversation_checkpoint_references ref ON ref.checkpoint_sequence=item.sequence
LEFT JOIN conversation_items source ON source.sequence=ref.source_sequence;

`
const legacyDown218 = `
UPDATE conversation_items SET payload=(SELECT source.payload FROM conversation_checkpoint_references ref
 JOIN conversation_items source ON source.sequence=ref.source_sequence WHERE ref.checkpoint_sequence=conversation_items.sequence)
WHERE sequence IN (SELECT checkpoint_sequence FROM conversation_checkpoint_references);
DROP VIEW conversation_resolved_items;
DROP TRIGGER conversation_checkpoint_materialize;
DROP TABLE conversation_checkpoint_references;
DROP INDEX conversation_provider_payload;
ALTER TABLE conversation_items DROP COLUMN payload_hash;
`

const legacyUp219 = `
-- Only unfinished output lives here. Offsets are relative to this execution's
-- pending suffix; finalized display items remain independently pageable.
CREATE TABLE execution_stream_text (
 execution_id TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
 end_offset INTEGER NOT NULL,
 text TEXT NOT NULL,
 PRIMARY KEY(execution_id,end_offset)
) WITHOUT ROWID;
ALTER TABLE executions ADD COLUMN display_digest BLOB;


CREATE TRIGGER display_digest_insert AFTER INSERT ON conversation_items
WHEN NEW.execution_id IS NOT NULL AND NEW.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=NEW.execution_id;
END;



CREATE TRIGGER display_digest_update AFTER UPDATE ON conversation_items
WHEN NEW.execution_id IS NOT NULL AND NEW.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=NEW.execution_id;
END;



CREATE TRIGGER display_digest_delete AFTER DELETE ON conversation_items
WHEN OLD.execution_id IS NOT NULL AND OLD.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=OLD.execution_id;
END;


`
const legacyDown219 = `
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
`

const legacyUp220 = `
-- A batch needs to invalidate a cached projection only once.
DROP TRIGGER display_digest_insert;

CREATE TRIGGER display_digest_insert AFTER INSERT ON conversation_items
WHEN NEW.execution_id IS NOT NULL AND NEW.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=NEW.execution_id AND display_digest IS NOT NULL;
END;

DROP TRIGGER display_digest_update;

CREATE TRIGGER display_digest_update AFTER UPDATE ON conversation_items
WHEN NEW.execution_id IS NOT NULL AND NEW.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=NEW.execution_id AND display_digest IS NOT NULL;
END;

DROP TRIGGER display_digest_delete;

CREATE TRIGGER display_digest_delete AFTER DELETE ON conversation_items
WHEN OLD.execution_id IS NOT NULL AND OLD.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=OLD.execution_id AND display_digest IS NOT NULL;
END;

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

CREATE TRIGGER display_plan_invalidate AFTER UPDATE OF display_digest ON executions
WHEN NEW.display_digest IS NULL
BEGIN
 UPDATE executions SET display_has_plan=NULL WHERE id=NEW.id;
END;


ALTER TABLE chat_compaction_checkpoints ADD COLUMN history_digest BLOB;

CREATE TRIGGER checkpoint_digest_insert AFTER INSERT ON conversation_items
WHEN NEW.checkpoint_scope_type IS NOT NULL
BEGIN
 UPDATE chat_compaction_checkpoints SET history_digest=NULL
 WHERE scope_type=NEW.checkpoint_scope_type AND scope_id=NEW.checkpoint_scope_id AND history_digest IS NOT NULL;
END;


CREATE TRIGGER checkpoint_digest_update AFTER UPDATE ON conversation_items
WHEN NEW.checkpoint_scope_type IS NOT NULL
BEGIN
 UPDATE chat_compaction_checkpoints SET history_digest=NULL
 WHERE scope_type=NEW.checkpoint_scope_type AND scope_id=NEW.checkpoint_scope_id AND history_digest IS NOT NULL;
END;


CREATE TRIGGER checkpoint_digest_delete AFTER DELETE ON conversation_items
WHEN OLD.checkpoint_scope_type IS NOT NULL
BEGIN
 UPDATE chat_compaction_checkpoints SET history_digest=NULL
 WHERE scope_type=OLD.checkpoint_scope_type AND scope_id=OLD.checkpoint_scope_id AND history_digest IS NOT NULL;
END;


`
const legacyDown220 = `
DROP TRIGGER display_digest_insert;

CREATE TRIGGER display_digest_insert AFTER INSERT ON conversation_items
WHEN NEW.execution_id IS NOT NULL AND NEW.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=NEW.execution_id;
END;

DROP TRIGGER display_digest_update;

CREATE TRIGGER display_digest_update AFTER UPDATE ON conversation_items
WHEN NEW.execution_id IS NOT NULL AND NEW.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=NEW.execution_id;
END;

DROP TRIGGER display_digest_delete;

CREATE TRIGGER display_digest_delete AFTER DELETE ON conversation_items
WHEN OLD.execution_id IS NOT NULL AND OLD.lane='display'
BEGIN
 UPDATE executions SET display_digest=NULL WHERE id=OLD.execution_id;
END;

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
`

const legacyUp221 = `
-- Contiguous immutable provider revisions need one retained boundary, not one
-- reference per message. Noncontiguous histories keep individual references.
CREATE TABLE conversation_checkpoint_ranges (
 checkpoint_sequence INTEGER PRIMARY KEY REFERENCES conversation_items(sequence) ON DELETE CASCADE,
 first_source_sequence INTEGER NOT NULL REFERENCES conversation_items(sequence) ON DELETE RESTRICT,
 last_source_sequence INTEGER NOT NULL REFERENCES conversation_items(sequence) ON DELETE RESTRICT,
 CHECK(last_source_sequence >= first_source_sequence),
 CHECK(checkpoint_sequence > last_source_sequence)
);
CREATE INDEX conversation_checkpoint_range_source ON conversation_checkpoint_ranges(first_source_sequence,last_source_sequence);

-- Before any source disappears, preserve the entire borrowed range in its
-- checkpoint row. This also covers execution/task deletion cascades.

CREATE TRIGGER conversation_checkpoint_range_materialize BEFORE DELETE ON conversation_items
BEGIN
 UPDATE conversation_items SET payload=json_set(payload,'$.items',json((
  SELECT json_group_array(json(payload)) FROM (
   SELECT source.payload FROM conversation_items source
   WHERE source.sequence BETWEEN
    (SELECT first_source_sequence FROM conversation_checkpoint_ranges WHERE checkpoint_sequence=conversation_items.sequence) AND
    (SELECT last_source_sequence FROM conversation_checkpoint_ranges WHERE checkpoint_sequence=conversation_items.sequence)
   ORDER BY source.sequence
  )
 ))) WHERE sequence IN (SELECT checkpoint_sequence FROM conversation_checkpoint_ranges WHERE OLD.sequence BETWEEN first_source_sequence AND last_source_sequence);
 DELETE FROM conversation_checkpoint_ranges WHERE OLD.sequence BETWEEN first_source_sequence AND last_source_sequence;
END;


`
const legacyDown221 = `
-- Expand the compressed rows back to the previous per-item representation.
DELETE FROM conversation_items WHERE checkpoint_scope_type IS NOT NULL AND lane='provider' AND operation='remove';

INSERT INTO conversation_items(checkpoint_scope_type,checkpoint_scope_id,generation,lane,item_id,position,operation,payload,mutation_id)
SELECT item.checkpoint_scope_type,item.checkpoint_scope_id,0,'provider',
 json_extract(item.payload,'$.prefix')||printf('%012d',json_extract(item.payload,'$.first_index')+entry.key),
 json_extract(item.payload,'$.position')+entry.key,'put',entry.value,'range-downgrade:'||item.sequence||':'||entry.key
FROM conversation_items item, json_each(COALESCE(json_extract(item.payload,'$.items'),
 (SELECT json_group_array(json(payload)) FROM (SELECT source.payload FROM conversation_items source WHERE source.sequence BETWEEN json_extract(item.payload,'$.first_source') AND json_extract(item.payload,'$.last_source') ORDER BY source.sequence)))) entry
WHERE item.item_id LIKE 'checkpoint:range:%' AND item.is_current=1 AND item.operation='put';

DROP TRIGGER conversation_checkpoint_range_materialize;
DROP TABLE conversation_checkpoint_ranges;
DELETE FROM conversation_items WHERE item_id LIKE 'checkpoint:range:%';
`

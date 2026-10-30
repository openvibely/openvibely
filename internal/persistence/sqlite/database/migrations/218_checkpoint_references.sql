-- +goose Up
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

-- +goose StatementBegin
CREATE TRIGGER conversation_checkpoint_materialize BEFORE DELETE ON conversation_items
BEGIN
    UPDATE conversation_items SET payload=OLD.payload
    WHERE sequence IN (SELECT checkpoint_sequence FROM conversation_checkpoint_references WHERE source_sequence=OLD.sequence);
    DELETE FROM conversation_checkpoint_references WHERE source_sequence=OLD.sequence;
END;
-- +goose StatementEnd

CREATE VIEW conversation_resolved_items AS
SELECT item.sequence, item.execution_id, item.checkpoint_scope_type, item.checkpoint_scope_id,
       item.generation, item.lane, item.item_id, item.position, item.operation,
       COALESCE(source.payload,item.payload) AS payload,
       item.previous_revision, item.mutation_id, item.snapshot_sequence, item.is_current, item.display_bytes
FROM conversation_items item
LEFT JOIN conversation_checkpoint_references ref ON ref.checkpoint_sequence=item.sequence
LEFT JOIN conversation_items source ON source.sequence=ref.source_sequence;

-- +goose Down
UPDATE conversation_items SET payload=(SELECT source.payload FROM conversation_checkpoint_references ref
 JOIN conversation_items source ON source.sequence=ref.source_sequence WHERE ref.checkpoint_sequence=conversation_items.sequence)
WHERE sequence IN (SELECT checkpoint_sequence FROM conversation_checkpoint_references);
DROP VIEW conversation_resolved_items;
DROP TRIGGER conversation_checkpoint_materialize;
DROP TABLE conversation_checkpoint_references;
DROP INDEX conversation_provider_payload;
ALTER TABLE conversation_items DROP COLUMN payload_hash;

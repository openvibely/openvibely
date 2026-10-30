-- +goose Up
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
-- +goose StatementBegin
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
-- +goose StatementEnd

-- +goose Down
-- Expand the compressed rows back to the previous per-item representation.
DELETE FROM conversation_items WHERE checkpoint_scope_type IS NOT NULL AND lane='provider' AND operation='remove';
-- +goose StatementBegin
INSERT INTO conversation_items(checkpoint_scope_type,checkpoint_scope_id,generation,lane,item_id,position,operation,payload,mutation_id)
SELECT item.checkpoint_scope_type,item.checkpoint_scope_id,0,'provider',
 json_extract(item.payload,'$.prefix')||printf('%012d',json_extract(item.payload,'$.first_index')+entry.key),
 json_extract(item.payload,'$.position')+entry.key,'put',entry.value,'range-downgrade:'||item.sequence||':'||entry.key
FROM conversation_items item, json_each(COALESCE(json_extract(item.payload,'$.items'),
 (SELECT json_group_array(json(payload)) FROM (SELECT source.payload FROM conversation_items source WHERE source.sequence BETWEEN json_extract(item.payload,'$.first_source') AND json_extract(item.payload,'$.last_source') ORDER BY source.sequence)))) entry
WHERE item.item_id LIKE 'checkpoint:range:%' AND item.is_current=1 AND item.operation='put';
-- +goose StatementEnd
DROP TRIGGER conversation_checkpoint_range_materialize;
DROP TABLE conversation_checkpoint_ranges;
DELETE FROM conversation_items WHERE item_id LIKE 'checkpoint:range:%';

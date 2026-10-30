-- +goose Up
-- Keep offset lookups off the overflow pages holding large tool output.
CREATE INDEX execution_stream_offsets ON execution_stream_text(execution_id,end_offset);

-- +goose Down
DROP INDEX execution_stream_offsets;

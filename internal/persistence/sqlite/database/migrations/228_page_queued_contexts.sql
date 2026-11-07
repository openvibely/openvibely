-- +goose Up
CREATE TABLE page_queued_contexts (
 input_id TEXT PRIMARY KEY REFERENCES thread_inputs(id) ON DELETE CASCADE,
 context_json TEXT NOT NULL
);
-- +goose Down
DROP TABLE page_queued_contexts;

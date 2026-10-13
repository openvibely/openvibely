-- +goose Up
CREATE TABLE execution_output_chunks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    execution_id TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
    output TEXT NOT NULL
);

CREATE INDEX idx_execution_output_chunks_execution_id_id
    ON execution_output_chunks(execution_id, id);

-- +goose Down
DROP TABLE execution_output_chunks;

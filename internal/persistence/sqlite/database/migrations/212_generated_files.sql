-- +goose Up
-- The table may already exist without this migration version being recorded.
-- Preserve existing generated files when reconciling that migration history.
CREATE TABLE IF NOT EXISTS generated_files (
 id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 execution_id TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
 file_name TEXT NOT NULL,
 file_path TEXT NOT NULL,
 media_type TEXT NOT NULL,
 file_size INTEGER NOT NULL,
 display TEXT NOT NULL DEFAULT '' CHECK (display IN ('', 'canvas')),
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_generated_files_project_execution ON generated_files(project_id, execution_id, created_at);

-- +goose Down
DROP TABLE generated_files;

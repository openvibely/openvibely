-- +goose Up
-- Chat turns have separate task IDs; the project owns their shared file list.
ALTER TABLE chat_attachments ADD COLUMN project_id TEXT REFERENCES projects(id) ON DELETE CASCADE;
UPDATE chat_attachments
SET project_id = (
    SELECT t.project_id
    FROM executions e JOIN tasks t ON t.id = e.task_id
    WHERE e.id = chat_attachments.execution_id
);
CREATE INDEX idx_chat_attachments_project_id ON chat_attachments(project_id, created_at, id);

-- +goose Down
DROP INDEX IF EXISTS idx_chat_attachments_project_id;
ALTER TABLE chat_attachments DROP COLUMN project_id;

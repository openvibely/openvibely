-- +goose Up
-- Task-thread message attachments move to task_attachments. The conversation type
-- comes from the persisted task category, not from where the file was uploaded.
ALTER TABLE task_attachments ADD COLUMN execution_id TEXT REFERENCES executions(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_task_attachments_execution_id ON task_attachments(execution_id);

INSERT INTO task_attachments (id, task_id, execution_id, file_name, file_path, media_type, file_size, created_at)
SELECT ca.id, e.task_id, ca.execution_id, ca.file_name, ca.file_path, ca.media_type, ca.file_size, ca.created_at
FROM chat_attachments ca
JOIN executions e ON e.id = ca.execution_id
JOIN tasks t ON t.id = e.task_id
WHERE t.category <> 'chat'
  AND NOT EXISTS (SELECT 1 FROM task_attachments ta WHERE ta.id = ca.id);

DELETE FROM chat_attachments
WHERE id IN (
  SELECT ca.id
  FROM chat_attachments ca
  JOIN executions e ON e.id = ca.execution_id
  JOIN tasks t ON t.id = e.task_id
  WHERE t.category <> 'chat'
);

-- +goose Down
INSERT INTO chat_attachments (id, execution_id, file_name, file_path, media_type, file_size, created_at)
SELECT ta.id, ta.execution_id, ta.file_name, ta.file_path, ta.media_type, ta.file_size, ta.created_at
FROM task_attachments ta
JOIN executions e ON e.id = ta.execution_id
WHERE ta.execution_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM chat_attachments ca WHERE ca.id = ta.id);

DELETE FROM task_attachments WHERE execution_id IS NOT NULL;

DROP INDEX IF EXISTS idx_task_attachments_execution_id;
DROP INDEX IF EXISTS idx_task_attachments_file_path;
DROP INDEX IF EXISTS idx_task_attachments_task_id;

CREATE TABLE task_attachments_old (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    task_id     TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    file_name   TEXT NOT NULL,
    file_path   TEXT NOT NULL,
    media_type  TEXT NOT NULL,
    file_size   INTEGER NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO task_attachments_old (id, task_id, file_name, file_path, media_type, file_size, created_at)
SELECT id, task_id, file_name, file_path, media_type, file_size, created_at FROM task_attachments;
DROP TABLE task_attachments;
ALTER TABLE task_attachments_old RENAME TO task_attachments;
CREATE INDEX idx_task_attachments_task_id ON task_attachments(task_id);
CREATE INDEX IF NOT EXISTS idx_task_attachments_file_path ON task_attachments(file_path);

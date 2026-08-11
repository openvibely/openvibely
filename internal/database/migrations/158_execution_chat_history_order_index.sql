-- +goose Up
-- Denormalize each execution's owning task project/category plus a rowid-equivalent
-- history order so project Chat history can be read from an equality-prefixed
-- ordered execution index instead of sorting every matching task execution.
ALTER TABLE executions ADD COLUMN task_project_id TEXT NOT NULL DEFAULT '';
ALTER TABLE executions ADD COLUMN task_category TEXT NOT NULL DEFAULT '';
ALTER TABLE executions ADD COLUMN history_order INTEGER NOT NULL DEFAULT 0;

UPDATE executions
SET task_project_id = COALESCE((SELECT project_id FROM tasks WHERE tasks.id = executions.task_id), ''),
    task_category = COALESCE((SELECT category FROM tasks WHERE tasks.id = executions.task_id), ''),
    history_order = rowid
WHERE task_project_id = '' OR task_category = '' OR history_order = 0;

-- +goose StatementBegin
CREATE TRIGGER executions_history_metadata_insert
AFTER INSERT ON executions
WHEN NEW.task_project_id = '' OR NEW.task_category = '' OR NEW.history_order = 0
BEGIN
    UPDATE executions
    SET task_project_id = COALESCE((SELECT project_id FROM tasks WHERE tasks.id = NEW.task_id), ''),
        task_category = COALESCE((SELECT category FROM tasks WHERE tasks.id = NEW.task_id), ''),
        history_order = NEW.rowid
    WHERE rowid = NEW.rowid;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER executions_history_metadata_task_id_update
AFTER UPDATE OF task_id ON executions
BEGIN
    UPDATE executions
    SET task_project_id = COALESCE((SELECT project_id FROM tasks WHERE tasks.id = NEW.task_id), ''),
        task_category = COALESCE((SELECT category FROM tasks WHERE tasks.id = NEW.task_id), '')
    WHERE rowid = NEW.rowid;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER executions_history_metadata_task_update
AFTER UPDATE OF project_id, category ON tasks
BEGIN
    UPDATE executions
    SET task_project_id = NEW.project_id,
        task_category = NEW.category
    WHERE task_id = NEW.id;
END;
-- +goose StatementEnd

CREATE INDEX idx_executions_chat_history_project_started
    ON executions(task_project_id, task_category, started_at DESC, history_order DESC);

CREATE INDEX idx_executions_chat_active_project_status_started
    ON executions(task_project_id, task_category, status, started_at DESC, history_order DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_executions_chat_active_project_status_started;
DROP INDEX IF EXISTS idx_executions_chat_history_project_started;
DROP TRIGGER IF EXISTS executions_history_metadata_task_update;
DROP TRIGGER IF EXISTS executions_history_metadata_task_id_update;
DROP TRIGGER IF EXISTS executions_history_metadata_insert;
ALTER TABLE executions DROP COLUMN history_order;
ALTER TABLE executions DROP COLUMN task_category;
ALTER TABLE executions DROP COLUMN task_project_id;

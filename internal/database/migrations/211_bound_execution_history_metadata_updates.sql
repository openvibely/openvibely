-- +goose Up
-- task_category is only used to select Chat history. Non-chat lane changes
-- must not rewrite every execution's large output/diff payload. Preserve the
-- cached non-chat value until the project or Chat membership actually changes.
DROP TRIGGER IF EXISTS executions_history_metadata_task_update;

-- +goose StatementBegin
CREATE TRIGGER executions_history_metadata_task_update
AFTER UPDATE OF project_id, category ON tasks
WHEN OLD.project_id IS NOT NEW.project_id
  OR ((OLD.category = 'chat') != (NEW.category = 'chat'))
BEGIN
    UPDATE executions
    SET task_project_id = NEW.project_id,
        task_category = NEW.category
    WHERE task_id = NEW.id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS executions_history_metadata_task_update;

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

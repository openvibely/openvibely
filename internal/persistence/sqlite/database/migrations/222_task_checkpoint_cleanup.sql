-- +goose Up
-- Checkpoint scopes are polymorphic, so task ownership cannot use a foreign
-- key. Deleting the checkpoint cascades to its conversation items.
-- Existing orphan checkpoints are intentionally left untouched.
-- +goose StatementBegin
CREATE TRIGGER task_checkpoint_cleanup AFTER DELETE ON tasks
BEGIN
    DELETE FROM chat_compaction_checkpoints
    WHERE scope_type = 'task' AND scope_id = OLD.id;
END;
-- +goose StatementEnd

-- Reject late saves after deletion, even when their caller was not cancelled.
-- +goose StatementBegin
CREATE TRIGGER task_checkpoint_owner_insert BEFORE INSERT ON chat_compaction_checkpoints
WHEN NEW.scope_type = 'task' AND NOT EXISTS (SELECT 1 FROM tasks WHERE id = NEW.scope_id)
BEGIN
    SELECT RAISE(ABORT, 'task checkpoint owner does not exist');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER task_checkpoint_owner_update BEFORE UPDATE OF scope_type, scope_id ON chat_compaction_checkpoints
WHEN NEW.scope_type = 'task' AND NOT EXISTS (SELECT 1 FROM tasks WHERE id = NEW.scope_id)
BEGIN
    SELECT RAISE(ABORT, 'task checkpoint owner does not exist');
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER task_checkpoint_owner_update;
DROP TRIGGER task_checkpoint_owner_insert;
DROP TRIGGER task_checkpoint_cleanup;

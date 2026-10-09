-- +goose Up
CREATE TABLE schedule_dispatch_admissions (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    schedule_id TEXT NOT NULL UNIQUE REFERENCES schedules(id) ON DELETE CASCADE,
    execution_id TEXT UNIQUE REFERENCES executions(id) ON DELETE CASCADE,
    starts_new_context INTEGER NOT NULL DEFAULT 0 CHECK (starts_new_context IN (0, 1))
);
-- +goose StatementBegin
CREATE TRIGGER schedule_admission_deleted AFTER DELETE ON schedule_dispatch_admissions
BEGIN
    UPDATE executions SET status = 'cancelled', error_message = 'Scheduled admission withdrawn', completed_at = datetime('now')
    WHERE id = OLD.execution_id AND status = 'queued';
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER schedule_admission_execution_started AFTER UPDATE OF status ON executions
WHEN NEW.status != 'queued'
BEGIN
    DELETE FROM schedule_dispatch_admissions WHERE execution_id = NEW.id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER schedule_admission_task_withdrawn AFTER UPDATE OF status, category ON tasks
WHEN NEW.status IN ('cancelled','failed','completed') OR NEW.category NOT IN ('active','scheduled')
BEGIN
    DELETE FROM schedule_dispatch_admissions WHERE task_id = NEW.id;
END;
-- +goose StatementEnd
-- +goose Down
DROP TRIGGER schedule_admission_task_withdrawn;
DROP TRIGGER schedule_admission_execution_started;
DROP TRIGGER schedule_admission_deleted;
DROP TABLE schedule_dispatch_admissions;

-- +goose Up
DROP TRIGGER schedule_admission_task_withdrawn;
-- +goose StatementBegin
CREATE TRIGGER schedule_admission_task_withdrawn AFTER UPDATE OF status, category ON tasks
WHEN NEW.status IN ('cancelled','failed','completed') OR NEW.category NOT IN ('active','scheduled')
BEGIN
    UPDATE tasks SET status = 'cancelled', updated_at = datetime('now')
    WHERE status = 'pending' AND id != NEW.id AND id IN (
        SELECT task_id FROM schedule_dispatch_admissions
        WHERE schedule_id IN (SELECT id FROM schedules WHERE task_id = NEW.id)
    );
    DELETE FROM schedule_dispatch_admissions
    WHERE task_id = NEW.id OR schedule_id IN (SELECT id FROM schedules WHERE task_id = NEW.id);
END;
-- +goose StatementEnd
-- +goose Down
DROP TRIGGER schedule_admission_task_withdrawn;
-- +goose StatementBegin
CREATE TRIGGER schedule_admission_task_withdrawn AFTER UPDATE OF status, category ON tasks
WHEN NEW.status IN ('cancelled','failed','completed') OR NEW.category NOT IN ('active','scheduled')
BEGIN
    DELETE FROM schedule_dispatch_admissions WHERE task_id = NEW.id;
END;
-- +goose StatementEnd

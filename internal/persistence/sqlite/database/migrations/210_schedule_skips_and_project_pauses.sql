-- +goose Up
CREATE TABLE schedule_skips (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    schedule_id TEXT NOT NULL DEFAULT '',
    start_at INTEGER NOT NULL,
    end_at INTEGER NOT NULL CHECK (end_at > start_at),
    restored INTEGER NOT NULL DEFAULT 0 CHECK (restored IN (0, 1)),
    PRIMARY KEY (project_id, schedule_id, start_at, end_at, restored)
);
CREATE TABLE schedule_project_pauses (
    project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    started_at INTEGER NOT NULL
);

-- +goose StatementBegin
CREATE TRIGGER schedule_skips_delete_schedule AFTER DELETE ON schedules
BEGIN
    DELETE FROM schedule_skips WHERE schedule_id = OLD.id;
END;
-- +goose StatementEnd

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
    UPDATE tasks SET status = 'cancelled', updated_at = datetime('now')
    WHERE status = 'pending' AND id != NEW.id AND id IN (
        SELECT task_id FROM schedule_dispatch_admissions
        WHERE schedule_id IN (SELECT id FROM schedules WHERE task_id = NEW.id)
    );
    DELETE FROM schedule_dispatch_admissions
    WHERE task_id = NEW.id OR schedule_id IN (SELECT id FROM schedules WHERE task_id = NEW.id);
END;
-- +goose StatementEnd

-- Withdraw queued work before the schedule foreign key removes its admission.
-- Started executions have already consumed their admission and are unaffected.
-- +goose StatementBegin
CREATE TRIGGER schedule_delete_withdraw_pending BEFORE DELETE ON schedules
BEGIN
    UPDATE tasks SET status = 'cancelled', updated_at = datetime('now')
    WHERE status = 'pending' AND id IN (
        SELECT task_id FROM schedule_dispatch_admissions WHERE schedule_id = OLD.id
    );
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER schedule_delete_withdraw_pending;
DROP TRIGGER schedule_admission_task_withdrawn;
DROP TRIGGER schedule_admission_execution_started;
DROP TRIGGER schedule_admission_deleted;
DROP TABLE schedule_dispatch_admissions;
DROP TRIGGER schedule_skips_delete_schedule;
DROP TABLE schedule_project_pauses;
DROP TABLE schedule_skips;

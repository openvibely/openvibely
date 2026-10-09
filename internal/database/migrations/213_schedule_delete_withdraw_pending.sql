-- +goose Up
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

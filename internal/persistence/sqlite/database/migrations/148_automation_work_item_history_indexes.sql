-- +goose Up
CREATE INDEX idx_automation_work_items_history ON automation_work_items(project_id, automation_id, created_at DESC, id DESC);
CREATE INDEX idx_automation_work_items_history_status ON automation_work_items(project_id, automation_id, status, created_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_automation_work_items_history_status;
DROP INDEX IF EXISTS idx_automation_work_items_history;

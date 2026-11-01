-- +goose Up
ALTER TABLE task_computer_sessions ADD COLUMN handoff_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE task_computer_sessions ADD COLUMN handoff_expected_text TEXT NOT NULL DEFAULT '';
ALTER TABLE task_computer_sessions ADD COLUMN handoff_alert_id TEXT NOT NULL DEFAULT '';
ALTER TABLE task_computer_sessions ADD COLUMN tool_target TEXT NOT NULL DEFAULT 'worktree' CHECK(tool_target IN ('worktree','computer'));

-- +goose Down
ALTER TABLE task_computer_sessions DROP COLUMN tool_target;
ALTER TABLE task_computer_sessions DROP COLUMN handoff_alert_id;
ALTER TABLE task_computer_sessions DROP COLUMN handoff_expected_text;
ALTER TABLE task_computer_sessions DROP COLUMN handoff_reason;

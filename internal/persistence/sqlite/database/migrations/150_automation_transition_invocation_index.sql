-- +goose Up
CREATE INDEX idx_automation_transitions_invocation ON automation_transitions(project_id, automation_id, invocation_id, occurred_at, id);

-- +goose Down
DROP INDEX IF EXISTS idx_automation_transitions_invocation;

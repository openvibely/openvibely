-- +goose Up
CREATE INDEX idx_thread_inputs_recover_chat_project
ON thread_inputs(scope, input_status, input_mode, project_id, run_execution_id)
WHERE scope = 'chat' AND input_status = 'pending' AND input_mode = 'queued';

-- +goose Down
DROP INDEX IF EXISTS idx_thread_inputs_recover_chat_project;

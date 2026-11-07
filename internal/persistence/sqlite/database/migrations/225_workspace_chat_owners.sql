-- +goose Up
-- Existing workspace conversations were shared, even when messages had authors.
-- Keep them unowned (admin-only); only newly created conversations are personal.
ALTER TABLE tasks ADD COLUMN chat_owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_inputs ADD COLUMN chat_owner_user_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_tasks_chat_owner ON tasks(project_id, chat_owner_user_id, category);
CREATE INDEX idx_thread_inputs_chat_owner ON thread_inputs(project_id, chat_owner_user_id, input_status);

DROP INDEX idx_thread_inputs_recover_chat_project;
CREATE INDEX idx_thread_inputs_recover_chat_project
ON thread_inputs(scope, input_status, input_mode, project_id, run_execution_id, chat_owner_user_id)
WHERE scope = 'chat' AND input_status = 'pending' AND input_mode = 'queued';

-- +goose Down
DROP INDEX idx_thread_inputs_recover_chat_project;
CREATE INDEX idx_thread_inputs_recover_chat_project
ON thread_inputs(scope, input_status, input_mode, project_id, run_execution_id)
WHERE scope = 'chat' AND input_status = 'pending' AND input_mode = 'queued';
DROP INDEX idx_thread_inputs_chat_owner;
DROP INDEX idx_tasks_chat_owner;
ALTER TABLE thread_inputs DROP COLUMN chat_owner_user_id;
ALTER TABLE tasks DROP COLUMN chat_owner_user_id;

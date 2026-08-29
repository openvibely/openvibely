-- +goose Up
ALTER TABLE x_task_context ADD COLUMN account_id TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_inputs ADD COLUMN x_account_id TEXT NOT NULL DEFAULT '';

-- Existing rows predate durable provider-account binding. Leave them empty so
-- reply delivery fails closed rather than risking a post through another account.

-- +goose Down
ALTER TABLE thread_inputs DROP COLUMN x_account_id;
ALTER TABLE x_task_context DROP COLUMN account_id;

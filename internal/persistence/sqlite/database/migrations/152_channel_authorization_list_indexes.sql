-- +goose Up
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_telegram_auth_list_covering
  ON telegram_authorized_users(added_at, id, project_id, telegram_user_id, telegram_username, display_name, added_by);
CREATE INDEX IF NOT EXISTS idx_slack_auth_list_covering
  ON slack_authorized_users(added_at, id, project_id, slack_user_id, display_name, added_by);
CREATE INDEX IF NOT EXISTS idx_discord_auth_list_covering
  ON discord_authorized_users(added_at, id, project_id, discord_user_id, display_name, added_by);
CREATE INDEX IF NOT EXISTS idx_email_auth_list_covering
  ON email_authorized_senders(added_at, id, project_id, email_address, display_name, added_by);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_telegram_auth_list_covering;
DROP INDEX IF EXISTS idx_slack_auth_list_covering;
DROP INDEX IF EXISTS idx_discord_auth_list_covering;
DROP INDEX IF EXISTS idx_email_auth_list_covering;
-- +goose StatementEnd

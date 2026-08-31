-- +goose Up
CREATE INDEX IF NOT EXISTS idx_telegram_auth_username_lower_unknown_id
ON telegram_authorized_users(telegram_user_id, lower(telegram_username))
WHERE telegram_user_id = 0 AND telegram_username != '';

-- +goose Down
DROP INDEX IF EXISTS idx_telegram_auth_username_lower_unknown_id;

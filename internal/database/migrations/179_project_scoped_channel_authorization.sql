-- +goose Up
-- Restore project ownership for terminal-managed inbound channel authorization rows.
-- Migration 108 made these identities globally unique, which prevented a row's
-- project_id from describing a management boundary. The channel access routes
-- now list and delete within that persisted boundary.
DROP INDEX IF EXISTS idx_slack_auth_unique_user_id;
CREATE UNIQUE INDEX IF NOT EXISTS idx_slack_auth_unique_user_id
  ON slack_authorized_users(project_id, slack_user_id);

DROP INDEX IF EXISTS idx_discord_auth_unique_user_id;
CREATE UNIQUE INDEX IF NOT EXISTS idx_discord_auth_unique_user_id
  ON discord_authorized_users(project_id, discord_user_id);

DROP INDEX IF EXISTS idx_email_auth_unique_address;
CREATE UNIQUE INDEX IF NOT EXISTS idx_email_auth_unique_address
  ON email_authorized_senders(project_id, lower(email_address));

DROP INDEX IF EXISTS idx_telegram_auth_unique_user_id;
CREATE UNIQUE INDEX IF NOT EXISTS idx_telegram_auth_unique_user_id
  ON telegram_authorized_users(project_id, telegram_user_id)
  WHERE telegram_user_id != 0;

DROP INDEX IF EXISTS idx_telegram_auth_unique_username;
CREATE UNIQUE INDEX IF NOT EXISTS idx_telegram_auth_unique_username
  ON telegram_authorized_users(project_id, lower(telegram_username))
  WHERE telegram_username != '';

-- +goose Down
-- Returning to global identities necessarily retains one oldest row per
-- identity before restoring the global uniqueness constraints.
DROP INDEX IF EXISTS idx_slack_auth_unique_user_id;
DELETE FROM slack_authorized_users
 WHERE id NOT IN (
   SELECT id FROM (
     SELECT id, ROW_NUMBER() OVER (PARTITION BY slack_user_id ORDER BY added_at ASC, id ASC) AS rn
       FROM slack_authorized_users
   ) WHERE rn = 1
 );
CREATE UNIQUE INDEX IF NOT EXISTS idx_slack_auth_unique_user_id
  ON slack_authorized_users(slack_user_id);

DROP INDEX IF EXISTS idx_discord_auth_unique_user_id;
DELETE FROM discord_authorized_users
 WHERE id NOT IN (
   SELECT id FROM (
     SELECT id, ROW_NUMBER() OVER (PARTITION BY discord_user_id ORDER BY added_at ASC, id ASC) AS rn
       FROM discord_authorized_users
   ) WHERE rn = 1
 );
CREATE UNIQUE INDEX IF NOT EXISTS idx_discord_auth_unique_user_id
  ON discord_authorized_users(discord_user_id);

DROP INDEX IF EXISTS idx_email_auth_unique_address;
DELETE FROM email_authorized_senders
 WHERE id NOT IN (
   SELECT id FROM (
     SELECT id, ROW_NUMBER() OVER (PARTITION BY lower(email_address) ORDER BY added_at ASC, id ASC) AS rn
       FROM email_authorized_senders
   ) WHERE rn = 1
 );
CREATE UNIQUE INDEX IF NOT EXISTS idx_email_auth_unique_address
  ON email_authorized_senders(lower(email_address));

DROP INDEX IF EXISTS idx_telegram_auth_unique_user_id;
DELETE FROM telegram_authorized_users
 WHERE telegram_user_id != 0
   AND id NOT IN (
     SELECT id FROM (
       SELECT id, ROW_NUMBER() OVER (PARTITION BY telegram_user_id ORDER BY added_at ASC, id ASC) AS rn
         FROM telegram_authorized_users
        WHERE telegram_user_id != 0
     ) WHERE rn = 1
   );
CREATE UNIQUE INDEX IF NOT EXISTS idx_telegram_auth_unique_user_id
  ON telegram_authorized_users(telegram_user_id)
  WHERE telegram_user_id != 0;

DROP INDEX IF EXISTS idx_telegram_auth_unique_username;
DELETE FROM telegram_authorized_users
 WHERE telegram_username != ''
   AND id NOT IN (
     SELECT id FROM (
       SELECT id, ROW_NUMBER() OVER (PARTITION BY lower(telegram_username) ORDER BY added_at ASC, id ASC) AS rn
         FROM telegram_authorized_users
        WHERE telegram_username != ''
     ) WHERE rn = 1
   );
CREATE UNIQUE INDEX IF NOT EXISTS idx_telegram_auth_unique_username
  ON telegram_authorized_users(lower(telegram_username))
  WHERE telegram_username != '';

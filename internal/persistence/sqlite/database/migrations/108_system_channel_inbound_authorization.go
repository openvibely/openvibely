package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("108_system_channel_inbound_authorization.go", upSystemChannelInboundAuthorization108, downSystemChannelInboundAuthorization108)
}

func upSystemChannelInboundAuthorization108(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`DROP INDEX IF EXISTS idx_slack_auth_unique_user_id`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_slack_auth_unique_user_id ON slack_authorized_users(slack_user_id)`,
		`DROP INDEX IF EXISTS idx_discord_auth_unique_user_id`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_discord_auth_unique_user_id ON discord_authorized_users(discord_user_id)`,
		`DROP INDEX IF EXISTS idx_email_auth_unique_address`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_email_auth_unique_address ON email_authorized_senders(lower(email_address))`,
		`DROP INDEX IF EXISTS idx_telegram_auth_unique_user_id`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_telegram_auth_unique_user_id ON telegram_authorized_users(telegram_user_id) WHERE telegram_user_id != 0`,
		`DROP INDEX IF EXISTS idx_telegram_auth_unique_username`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_telegram_auth_unique_username ON telegram_authorized_users(lower(telegram_username)) WHERE telegram_username != ''`,
	}

	if err := dedupeChannelAuthorizationRows108(ctx, tx); err != nil {
		return err
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("updating system channel authorization indexes: %w", err)
		}
	}
	return nil
}

func downSystemChannelInboundAuthorization108(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`DROP INDEX IF EXISTS idx_slack_auth_unique_user_id`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_slack_auth_unique_user_id ON slack_authorized_users(project_id, slack_user_id)`,
		`DROP INDEX IF EXISTS idx_discord_auth_unique_user_id`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_discord_auth_unique_user_id ON discord_authorized_users(project_id, discord_user_id)`,
		`DROP INDEX IF EXISTS idx_email_auth_unique_address`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_email_auth_unique_address ON email_authorized_senders(project_id, lower(email_address))`,
		`DROP INDEX IF EXISTS idx_telegram_auth_unique_user_id`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_telegram_auth_unique_user_id ON telegram_authorized_users(project_id, telegram_user_id) WHERE telegram_user_id != 0`,
		`DROP INDEX IF EXISTS idx_telegram_auth_unique_username`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_telegram_auth_unique_username ON telegram_authorized_users(project_id, telegram_username) WHERE telegram_username != ''`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("restoring project channel authorization indexes: %w", err)
		}
	}
	return nil
}

func dedupeChannelAuthorizationRows108(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`DELETE FROM slack_authorized_users
		 WHERE id NOT IN (
			 SELECT id FROM (
				 SELECT id,
						ROW_NUMBER() OVER (PARTITION BY slack_user_id ORDER BY added_at ASC, id ASC) AS rn
				 FROM slack_authorized_users
			 ) WHERE rn = 1
		 )`,
		`DELETE FROM discord_authorized_users
		 WHERE id NOT IN (
			 SELECT id FROM (
				 SELECT id,
						ROW_NUMBER() OVER (PARTITION BY discord_user_id ORDER BY added_at ASC, id ASC) AS rn
				 FROM discord_authorized_users
			 ) WHERE rn = 1
		 )`,
		`DELETE FROM email_authorized_senders
		 WHERE id NOT IN (
			 SELECT id FROM (
				 SELECT id,
						ROW_NUMBER() OVER (PARTITION BY lower(email_address) ORDER BY added_at ASC, id ASC) AS rn
				 FROM email_authorized_senders
			 ) WHERE rn = 1
		 )`,
		`DELETE FROM telegram_authorized_users
		 WHERE telegram_user_id != 0
		   AND id NOT IN (
			 SELECT id FROM (
				 SELECT id,
						ROW_NUMBER() OVER (PARTITION BY telegram_user_id ORDER BY added_at ASC, id ASC) AS rn
				 FROM telegram_authorized_users
				 WHERE telegram_user_id != 0
			 ) WHERE rn = 1
		 )`,
		`DELETE FROM telegram_authorized_users
		 WHERE telegram_username != ''
		   AND id NOT IN (
			 SELECT id FROM (
				 SELECT id,
						ROW_NUMBER() OVER (PARTITION BY lower(telegram_username) ORDER BY added_at ASC, id ASC) AS rn
				 FROM telegram_authorized_users
				 WHERE telegram_username != ''
			 ) WHERE rn = 1
		 )`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("dedupe channel authorization rows: %w", err)
		}
	}
	return nil
}

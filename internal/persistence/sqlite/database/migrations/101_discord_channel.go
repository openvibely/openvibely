package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("101_discord_channel.go", upDiscordChannel101, downDiscordChannel101)
}

func upDiscordChannel101(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS discord_authorized_users (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			project_id TEXT NOT NULL,
			discord_user_id TEXT NOT NULL,
			display_name TEXT NOT NULL DEFAULT '',
			added_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			added_by TEXT NOT NULL DEFAULT 'web',
			FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_discord_auth_unique_user_id
			ON discord_authorized_users(project_id, discord_user_id);
		CREATE INDEX IF NOT EXISTS idx_discord_auth_project
			ON discord_authorized_users(project_id);
		CREATE INDEX IF NOT EXISTS idx_discord_auth_user
			ON discord_authorized_users(discord_user_id);

		CREATE TABLE IF NOT EXISTS discord_task_context (
			task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
			discord_channel_id TEXT NOT NULL,
			discord_thread_id TEXT NOT NULL DEFAULT '',
			discord_message_id TEXT NOT NULL DEFAULT '',
			discord_user_id TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_discord_task_context_channel
			ON discord_task_context(discord_channel_id, discord_thread_id);
	`); err != nil {
		return fmt.Errorf("creating discord channel tables: %w", err)
	}

	columns, err := tableColumns101(ctx, tx, "thread_inputs")
	if err != nil {
		return err
	}
	for _, column := range []struct {
		name string
		def  string
	}{
		{"discord_channel_id", "TEXT NOT NULL DEFAULT ''"},
		{"discord_thread_id", "TEXT NOT NULL DEFAULT ''"},
		{"discord_message_id", "TEXT NOT NULL DEFAULT ''"},
		{"discord_user_id", "TEXT NOT NULL DEFAULT ''"},
	} {
		if !columns[column.name] {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE thread_inputs ADD COLUMN %s %s", column.name, column.def)); err != nil {
				return fmt.Errorf("adding thread_inputs.%s: %w", column.name, err)
			}
		}
	}
	return nil
}

func downDiscordChannel101(ctx context.Context, tx *sql.Tx) error {
	columns, err := tableColumns101(ctx, tx, "thread_inputs")
	if err != nil {
		return err
	}
	for _, column := range []string{"discord_user_id", "discord_message_id", "discord_thread_id", "discord_channel_id"} {
		if columns[column] {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE thread_inputs DROP COLUMN "+column); err != nil {
				return fmt.Errorf("dropping thread_inputs.%s: %w", column, err)
			}
		}
	}
	_, err = tx.ExecContext(ctx, `
		DROP INDEX IF EXISTS idx_discord_task_context_channel;
		DROP TABLE IF EXISTS discord_task_context;
		DROP INDEX IF EXISTS idx_discord_auth_user;
		DROP INDEX IF EXISTS idx_discord_auth_project;
		DROP INDEX IF EXISTS idx_discord_auth_unique_user_id;
		DROP TABLE IF EXISTS discord_authorized_users;
	`)
	return err
}

func tableColumns101(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, fmt.Errorf("reading %s columns: %w", table, err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("scanning %s columns: %w", table, err)
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

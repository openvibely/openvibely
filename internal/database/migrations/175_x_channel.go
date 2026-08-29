package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("175_x_channel.go", upXChannel175, downXChannel175)
}

func upXChannel175(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS x_authorized_users (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			x_user_id TEXT NOT NULL,
			username TEXT NOT NULL DEFAULT '',
			added_at DATETIME NOT NULL DEFAULT (datetime('now')),
			UNIQUE(project_id, x_user_id)
		);

		CREATE TABLE IF NOT EXISTS x_user_projects (
			x_user_id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS x_task_context (
			task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			account_id TEXT NOT NULL DEFAULT '',
			conversation_id TEXT NOT NULL,
			reply_to_tweet_id TEXT NOT NULL,
			x_user_id TEXT NOT NULL,
			username TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS x_inbound_receipts (
			tweet_id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			status TEXT NOT NULL CHECK (status IN ('processing', 'completed')),
			lease_expires_at DATETIME,
			lease_token TEXT NOT NULL DEFAULT '',
			task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL,
			created_at DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);
	`); err != nil {
		return fmt.Errorf("creating X channel tables: %w", err)
	}

	for table, columns := range map[string][]struct {
		name string
		def  string
	}{
		"x_task_context": {
			{name: "account_id", def: "TEXT NOT NULL DEFAULT ''"},
		},
		"x_inbound_receipts": {
			{name: "lease_token", def: "TEXT NOT NULL DEFAULT ''"},
		},
		"thread_inputs": {
			{name: "x_account_id", def: "TEXT NOT NULL DEFAULT ''"},
			{name: "x_conversation_id", def: "TEXT NOT NULL DEFAULT ''"},
			{name: "x_reply_to_tweet_id", def: "TEXT NOT NULL DEFAULT ''"},
			{name: "x_user_id", def: "TEXT NOT NULL DEFAULT ''"},
			{name: "x_username", def: "TEXT NOT NULL DEFAULT ''"},
		},
	} {
		existing, err := tableColumns175(ctx, tx, table)
		if err != nil {
			return err
		}
		for _, column := range columns {
			if existing[column.name] {
				continue
			}
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column.name, column.def)); err != nil {
				return fmt.Errorf("adding %s.%s: %w", table, column.name, err)
			}
		}
	}

	if _, err := tx.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_x_authorized_users_project ON x_authorized_users(project_id);
		CREATE INDEX IF NOT EXISTS idx_x_user_projects_project ON x_user_projects(project_id);
		CREATE INDEX IF NOT EXISTS idx_x_task_context_conversation ON x_task_context(project_id, conversation_id);
		CREATE INDEX IF NOT EXISTS idx_x_inbound_receipts_project ON x_inbound_receipts(project_id);
		CREATE INDEX IF NOT EXISTS idx_x_inbound_receipts_task ON x_inbound_receipts(task_id) WHERE task_id IS NOT NULL;

		UPDATE x_inbound_receipts
		SET lease_expires_at = datetime('now', '-1 second')
		WHERE status = 'processing' AND lease_token = '';
	`); err != nil {
		return fmt.Errorf("finalizing X channel schema: %w", err)
	}
	return nil
}

func downXChannel175(ctx context.Context, tx *sql.Tx) error {
	columns, err := tableColumns175(ctx, tx, "thread_inputs")
	if err != nil {
		return err
	}
	for _, column := range []string{"x_username", "x_user_id", "x_reply_to_tweet_id", "x_conversation_id", "x_account_id"} {
		if !columns[column] {
			continue
		}
		if _, err := tx.ExecContext(ctx, "ALTER TABLE thread_inputs DROP COLUMN "+column); err != nil {
			return fmt.Errorf("dropping thread_inputs.%s: %w", column, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		DROP INDEX IF EXISTS idx_x_inbound_receipts_task;
		DROP INDEX IF EXISTS idx_x_inbound_receipts_project;
		DROP TABLE IF EXISTS x_inbound_receipts;
		DROP INDEX IF EXISTS idx_x_task_context_conversation;
		DROP TABLE IF EXISTS x_task_context;
		DROP INDEX IF EXISTS idx_x_user_projects_project;
		DROP TABLE IF EXISTS x_user_projects;
		DROP INDEX IF EXISTS idx_x_authorized_users_project;
		DROP TABLE IF EXISTS x_authorized_users;
	`); err != nil {
		return fmt.Errorf("dropping X channel schema: %w", err)
	}
	return nil
}

func tableColumns175(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, fmt.Errorf("reading %s columns: %w", table, err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("scanning %s columns: %w", table, err)
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

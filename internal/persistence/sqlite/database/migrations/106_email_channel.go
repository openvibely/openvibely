package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("106_email_channel.go", upEmailChannel106, downEmailChannel106)
}

func upEmailChannel106(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS email_authorized_senders (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			project_id TEXT NOT NULL,
			email_address TEXT NOT NULL,
			display_name TEXT NOT NULL DEFAULT '',
			added_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			added_by TEXT NOT NULL DEFAULT 'web',
			FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_email_auth_unique_address ON email_authorized_senders(project_id, lower(email_address));
		CREATE INDEX IF NOT EXISTS idx_email_auth_project ON email_authorized_senders(project_id);
		CREATE INDEX IF NOT EXISTS idx_email_auth_address ON email_authorized_senders(lower(email_address));
			CREATE TABLE IF NOT EXISTS email_task_context (
				task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
				email_from TEXT NOT NULL,
				email_message_id TEXT NOT NULL,
				email_references TEXT NOT NULL DEFAULT '',
				email_subject TEXT NOT NULL DEFAULT '',
				email_session_key TEXT NOT NULL DEFAULT '',
				created_at DATETIME NOT NULL DEFAULT (datetime('now')),
				updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
			);
			CREATE INDEX IF NOT EXISTS idx_email_task_context_from ON email_task_context(email_from);
			CREATE INDEX IF NOT EXISTS idx_email_task_context_session ON email_task_context(email_session_key);
	`); err != nil {
		return fmt.Errorf("creating email channel tables: %w", err)
	}

	contextColumns, err := tableColumns106(ctx, tx, "email_task_context")
	if err != nil {
		return err
	}
	if !contextColumns["email_session_key"] {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE email_task_context ADD COLUMN email_session_key TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("adding email_task_context.email_session_key: %w", err)
		}
	}

	columns, err := tableColumns106(ctx, tx, "thread_inputs")
	if err != nil {
		return err
	}
	for _, column := range []struct {
		name string
		def  string
	}{
		{"email_from", "TEXT NOT NULL DEFAULT ''"},
		{"email_message_id", "TEXT NOT NULL DEFAULT ''"},
		{"email_references", "TEXT NOT NULL DEFAULT ''"},
		{"email_subject", "TEXT NOT NULL DEFAULT ''"},
		{"email_session_key", "TEXT NOT NULL DEFAULT ''"},
	} {
		if !columns[column.name] {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE thread_inputs ADD COLUMN %s %s", column.name, column.def)); err != nil {
				return fmt.Errorf("adding thread_inputs.%s: %w", column.name, err)
			}
		}
	}
	return nil
}

func downEmailChannel106(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
			DROP INDEX IF EXISTS idx_email_task_context_session;
			DROP INDEX IF EXISTS idx_email_task_context_from;
			DROP TABLE IF EXISTS email_task_context;
		DROP INDEX IF EXISTS idx_email_auth_address;
		DROP INDEX IF EXISTS idx_email_auth_project;
		DROP INDEX IF EXISTS idx_email_auth_unique_address;
		DROP TABLE IF EXISTS email_authorized_senders;
	`)
	return err
}

func tableColumns106(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
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

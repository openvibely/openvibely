package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("225_chat_ownership_repair.go", upChatOwnershipRepair, downChatOwnershipRepair)
}

// Some installations applied 224 before chat ownership was appended to it.
// Check the schema so both those databases and fresh installations can upgrade.
func upChatOwnershipRepair(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"tasks", "thread_inputs"} {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = 'chat_owner_user_id'`, table).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			// Historical conversations remain unowned/admin-only. Do not infer ownership
			// from a message author or overwrite existing personal conversation owners.
			if _, err := tx.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN chat_owner_user_id TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("adding %s chat ownership: %w", table, err)
			}
		}
	}
	_, err := tx.ExecContext(ctx, `
 CREATE INDEX IF NOT EXISTS idx_tasks_chat_owner ON tasks(project_id, chat_owner_user_id, category);
 CREATE INDEX IF NOT EXISTS idx_thread_inputs_chat_owner ON thread_inputs(project_id, chat_owner_user_id, input_status);
 DROP INDEX IF EXISTS idx_thread_inputs_recover_chat_project;
 CREATE INDEX idx_thread_inputs_recover_chat_project
 ON thread_inputs(scope, input_status, input_mode, project_id, run_execution_id, chat_owner_user_id)
 WHERE scope = 'chat' AND input_status = 'pending' AND input_mode = 'queued';
 `)
	return err
}

// Version 224's current schema already includes these columns and owns their
// rollback. Retain ownership data when rolling back only this repair.
func downChatOwnershipRepair(context.Context, *sql.Tx) error { return nil }

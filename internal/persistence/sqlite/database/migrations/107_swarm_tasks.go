package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("107_swarm_tasks.go", upSwarmTasks107, downSwarmTasks107)
}

func upSwarmTasks107(ctx context.Context, tx *sql.Tx) error {
	columns, err := tableColumns107(ctx, tx, "tasks")
	if err != nil {
		return err
	}
	for _, column := range []struct {
		name string
		def  string
	}{
		{"swarm_role", "TEXT NOT NULL DEFAULT ''"},
		{"swarm_status", "TEXT NOT NULL DEFAULT ''"},
		{"swarm_config", "TEXT NOT NULL DEFAULT '{}'"},
		{"swarm_sequence", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if !columns[column.name] {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE tasks ADD COLUMN %s %s", column.name, column.def)); err != nil {
				return fmt.Errorf("adding tasks.%s: %w", column.name, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_tasks_swarm_parent
		  ON tasks(parent_task_id, swarm_role, swarm_sequence);
	`); err != nil {
		return fmt.Errorf("creating idx_tasks_swarm_parent: %w", err)
	}
	return nil
}

func downSwarmTasks107(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		DROP INDEX IF EXISTS idx_tasks_swarm_parent;
		ALTER TABLE tasks DROP COLUMN swarm_sequence;
		ALTER TABLE tasks DROP COLUMN swarm_config;
		ALTER TABLE tasks DROP COLUMN swarm_status;
		ALTER TABLE tasks DROP COLUMN swarm_role;
	`)
	return err
}

func tableColumns107(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
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

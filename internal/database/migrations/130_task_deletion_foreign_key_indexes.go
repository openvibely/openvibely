package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("130_task_deletion_foreign_key_indexes.go", upTaskDeletionForeignKeyIndexes130, downTaskDeletionForeignKeyIndexes130)
}

func upTaskDeletionForeignKeyIndexes130(ctx context.Context, tx *sql.Tx) error {
	// Some databases upgraded from the pre-baseline migration history do not
	// contain every feature table represented in 068_baseline.sql. An absent
	// table also means there is no task foreign key lookup to optimize, so skip
	// its index instead of preventing the database from starting.
	indexes := []struct {
		table string
		sql   string
	}{
		{"alerts", `CREATE INDEX IF NOT EXISTS idx_alerts_execution_id ON alerts(execution_id) WHERE execution_id IS NOT NULL`},
		{"alerts", `CREATE INDEX IF NOT EXISTS idx_alerts_task_id ON alerts(task_id) WHERE task_id IS NOT NULL`},
		{"alerts", `CREATE INDEX IF NOT EXISTS idx_alerts_source_task_id ON alerts(source_task_id) WHERE source_task_id IS NOT NULL`},
		{"architect_tasks", `CREATE INDEX IF NOT EXISTS idx_architect_tasks_task_id ON architect_tasks(task_id) WHERE task_id IS NOT NULL`},
		{"automation_dispatch_outbox", `CREATE INDEX IF NOT EXISTS idx_automation_dispatch_outbox_task ON automation_dispatch_outbox(task_id)`},
		{"conflict_history", `CREATE INDEX IF NOT EXISTS idx_conflict_history_task_a ON conflict_history(task_a_id)`},
		{"conflict_history", `CREATE INDEX IF NOT EXISTS idx_conflict_history_task_b ON conflict_history(task_b_id)`},
		{"insights", `CREATE INDEX IF NOT EXISTS idx_insights_task_id ON insights(task_id) WHERE task_id IS NOT NULL`},
		{"llm_usage_events", `CREATE INDEX IF NOT EXISTS idx_llm_usage_events_task ON llm_usage_events(task_id) WHERE task_id IS NOT NULL`},
		{"schedules", `CREATE INDEX IF NOT EXISTS idx_schedules_task_id ON schedules(task_id)`},
		{"skill_analytics_events", `CREATE INDEX IF NOT EXISTS idx_skill_analytics_events_task ON skill_analytics_events(task_id) WHERE task_id IS NOT NULL`},
		{"task_attachments", `CREATE INDEX IF NOT EXISTS idx_task_attachments_file_path ON task_attachments(file_path)`},
		{"chat_attachments", `CREATE INDEX IF NOT EXISTS idx_chat_attachments_file_path ON chat_attachments(file_path)`},
		{"thread_inputs", `CREATE INDEX IF NOT EXISTS idx_thread_inputs_attachment_session_id ON thread_inputs(attachment_session_id)
			WHERE attachment_session_id IS NOT NULL AND attachment_session_id <> ''`},
	}

	for _, index := range indexes {
		var tableExists bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)`,
			index.table,
		).Scan(&tableExists); err != nil {
			return fmt.Errorf("checking table %s for task deletion index: %w", index.table, err)
		}
		if !tableExists {
			continue
		}
		if _, err := tx.ExecContext(ctx, index.sql); err != nil {
			return fmt.Errorf("creating task deletion index on %s: %w", index.table, err)
		}
	}
	return nil
}

func downTaskDeletionForeignKeyIndexes130(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		DROP INDEX IF EXISTS idx_thread_inputs_attachment_session_id;
		DROP INDEX IF EXISTS idx_chat_attachments_file_path;
		DROP INDEX IF EXISTS idx_task_attachments_file_path;
		DROP INDEX IF EXISTS idx_skill_analytics_events_task;
		DROP INDEX IF EXISTS idx_schedules_task_id;
		DROP INDEX IF EXISTS idx_llm_usage_events_task;
		DROP INDEX IF EXISTS idx_insights_task_id;
		DROP INDEX IF EXISTS idx_conflict_history_task_b;
		DROP INDEX IF EXISTS idx_conflict_history_task_a;
		DROP INDEX IF EXISTS idx_automation_dispatch_outbox_task;
		DROP INDEX IF EXISTS idx_architect_tasks_task_id;
		DROP INDEX IF EXISTS idx_alerts_source_task_id;
		DROP INDEX IF EXISTS idx_alerts_task_id;
		DROP INDEX IF EXISTS idx_alerts_execution_id;
	`)
	return err
}

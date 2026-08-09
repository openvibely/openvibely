package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("150_llm_usage_local_bucket_indexes.go", upLLMUsageLocalBucketIndexes150, downLLMUsageLocalBucketIndexes150)
}

func upLLMUsageLocalBucketIndexes150(ctx context.Context, tx *sql.Tx) error {
	columns, err := tableColumns091(ctx, tx, "llm_usage_events")
	if err != nil {
		return err
	}
	for _, column := range []struct {
		name string
		def  string
	}{
		{"occurred_local_hour", "TEXT"},
		{"occurred_local_day", "TEXT"},
		{"occurred_local_week", "TEXT"},
		{"occurred_local_month", "TEXT"},
	} {
		if !columns[column.name] {
			if err := addColumn091(ctx, tx, "llm_usage_events", column.name, column.def); err != nil {
				return err
			}
		}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE llm_usage_events
		SET occurred_local_hour = strftime('%Y-%m-%d %H:00:00', occurred_at, 'localtime'),
		    occurred_local_day = date(occurred_at, 'localtime'),
		    occurred_local_week = strftime('%Y-W%W', occurred_at, 'localtime'),
		    occurred_local_month = strftime('%Y-%m', occurred_at, 'localtime')
		WHERE occurred_local_hour IS NULL
		   OR occurred_local_day IS NULL
		   OR occurred_local_week IS NULL
		   OR occurred_local_month IS NULL;
	`); err != nil {
		return fmt.Errorf("backfilling llm usage local buckets: %w", err)
	}

	return execStatements091(ctx, tx, []string{
		`CREATE TRIGGER IF NOT EXISTS trg_llm_usage_events_local_buckets_ai
		AFTER INSERT ON llm_usage_events
		FOR EACH ROW
		BEGIN
		  UPDATE llm_usage_events
		  SET occurred_local_hour = strftime('%Y-%m-%d %H:00:00', NEW.occurred_at, 'localtime'),
		      occurred_local_day = date(NEW.occurred_at, 'localtime'),
		      occurred_local_week = strftime('%Y-W%W', NEW.occurred_at, 'localtime'),
		      occurred_local_month = strftime('%Y-%m', NEW.occurred_at, 'localtime')
		  WHERE rowid = NEW.rowid;
		END`,
		`CREATE TRIGGER IF NOT EXISTS trg_llm_usage_events_local_buckets_au
		AFTER UPDATE OF occurred_at ON llm_usage_events
		FOR EACH ROW
		BEGIN
		  UPDATE llm_usage_events
		  SET occurred_local_hour = strftime('%Y-%m-%d %H:00:00', NEW.occurred_at, 'localtime'),
		      occurred_local_day = date(NEW.occurred_at, 'localtime'),
		      occurred_local_week = strftime('%Y-W%W', NEW.occurred_at, 'localtime'),
		      occurred_local_month = strftime('%Y-%m', NEW.occurred_at, 'localtime')
		  WHERE rowid = NEW.rowid;
		END`,
		`CREATE INDEX IF NOT EXISTS idx_llm_usage_events_project_local_hour_model ON llm_usage_events(project_id, occurred_local_hour, provider, model, account_id, occurred_at)`,
		`CREATE INDEX IF NOT EXISTS idx_llm_usage_events_project_local_day_model ON llm_usage_events(project_id, occurred_local_day, provider, model, account_id, occurred_at)`,
		`CREATE INDEX IF NOT EXISTS idx_llm_usage_events_project_local_week_model ON llm_usage_events(project_id, occurred_local_week, provider, model, account_id, occurred_at)`,
		`CREATE INDEX IF NOT EXISTS idx_llm_usage_events_project_local_month_model ON llm_usage_events(project_id, occurred_local_month, provider, model, account_id, occurred_at)`,
		`CREATE INDEX IF NOT EXISTS idx_llm_usage_events_project_provider_model_time ON llm_usage_events(project_id, provider, model, account_id, occurred_at)`,
	})
}

func downLLMUsageLocalBucketIndexes150(ctx context.Context, tx *sql.Tx) error {
	return execStatements091(ctx, tx, []string{
		`DROP TRIGGER IF EXISTS trg_llm_usage_events_local_buckets_ai`,
		`DROP TRIGGER IF EXISTS trg_llm_usage_events_local_buckets_au`,
		`DROP INDEX IF EXISTS idx_llm_usage_events_project_local_hour_model`,
		`DROP INDEX IF EXISTS idx_llm_usage_events_project_local_day_model`,
		`DROP INDEX IF EXISTS idx_llm_usage_events_project_local_week_model`,
		`DROP INDEX IF EXISTS idx_llm_usage_events_project_local_month_model`,
		`DROP INDEX IF EXISTS idx_llm_usage_events_project_provider_model_time`,
	})
}

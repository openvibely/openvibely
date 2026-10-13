package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("091_llm_usage_tracking.go", upLLMUsageTracking091, downLLMUsageTracking091)
}

func upLLMUsageTracking091(ctx context.Context, tx *sql.Tx) error {
	if err := ensureLLMUsageEvents091(ctx, tx); err != nil {
		return err
	}
	if err := ensureAccountUsageSnapshots091(ctx, tx); err != nil {
		return err
	}
	if err := ensureAccountUsageExtraLimits091(ctx, tx); err != nil {
		return err
	}
	if err := backfillLLMUsageEvents091(ctx, tx); err != nil {
		return err
	}
	return nil
}

func downLLMUsageTracking091(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM llm_usage_events WHERE json_extract(raw_usage_json, '$.source') = 'executions_backfill'`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		DROP TABLE IF EXISTS account_usage_extra_limits;
		DROP TABLE IF EXISTS account_usage_snapshots;
		DROP TABLE IF EXISTS llm_usage_events;
	`)
	return err
}

func ensureLLMUsageEvents091(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS llm_usage_events (
		  id TEXT PRIMARY KEY,
		  provider TEXT NOT NULL,
		  account_id TEXT,
		  project_id TEXT,
		  task_id TEXT,
		  execution_id TEXT,
		  chat_thread_id TEXT,
		  turn_id TEXT,
		  agent_config_id TEXT,
		  model TEXT NOT NULL DEFAULT '',
		  operation TEXT NOT NULL DEFAULT '',
		  status TEXT NOT NULL DEFAULT '',
		  error_message TEXT NOT NULL DEFAULT '',
		  input_tokens INTEGER NOT NULL DEFAULT 0,
		  output_tokens INTEGER NOT NULL DEFAULT 0,
		  cached_input_tokens INTEGER NOT NULL DEFAULT 0,
		  cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
		  cache_read_input_tokens INTEGER NOT NULL DEFAULT 0,
		  reasoning_output_tokens INTEGER NOT NULL DEFAULT 0,
		  total_tokens INTEGER NOT NULL DEFAULT 0,
		  cost_usd REAL,
		  latency_ms INTEGER,
		  context_window INTEGER,
		  max_output_tokens INTEGER,
		  provider_response_id TEXT,
		  raw_usage_json TEXT NOT NULL DEFAULT '{}',
		  occurred_at TEXT NOT NULL DEFAULT (datetime('now')),
		  created_at TEXT NOT NULL DEFAULT (datetime('now')),
		  FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE SET NULL,
		  FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE SET NULL,
		  FOREIGN KEY(execution_id) REFERENCES executions(id) ON DELETE SET NULL,
		  FOREIGN KEY(agent_config_id) REFERENCES agent_configs(id) ON DELETE SET NULL
		);
	`); err != nil {
		return fmt.Errorf("creating llm_usage_events: %w", err)
	}

	columns, err := tableColumns091(ctx, tx, "llm_usage_events")
	if err != nil {
		return err
	}
	if !columns["status"] {
		if columns["request_status"] {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE llm_usage_events RENAME COLUMN request_status TO status`); err != nil {
				return fmt.Errorf("renaming llm_usage_events.request_status: %w", err)
			}
			columns["status"] = true
			delete(columns, "request_status")
		} else if err := addColumn091(ctx, tx, "llm_usage_events", "status", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}

	for _, column := range []struct {
		name string
		def  string
	}{
		{"provider", "TEXT NOT NULL DEFAULT ''"},
		{"account_id", "TEXT"},
		{"project_id", "TEXT"},
		{"task_id", "TEXT"},
		{"execution_id", "TEXT"},
		{"chat_thread_id", "TEXT"},
		{"turn_id", "TEXT"},
		{"agent_config_id", "TEXT"},
		{"model", "TEXT NOT NULL DEFAULT ''"},
		{"operation", "TEXT NOT NULL DEFAULT ''"},
		{"error_message", "TEXT NOT NULL DEFAULT ''"},
		{"input_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"output_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"cached_input_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"cache_creation_input_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"cache_read_input_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"reasoning_output_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"total_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"cost_usd", "REAL"},
		{"latency_ms", "INTEGER"},
		{"context_window", "INTEGER"},
		{"max_output_tokens", "INTEGER"},
		{"provider_response_id", "TEXT"},
		{"raw_usage_json", "TEXT NOT NULL DEFAULT '{}'"},
		{"occurred_at", "TEXT NOT NULL DEFAULT (datetime('now'))"},
		{"created_at", "TEXT NOT NULL DEFAULT (datetime('now'))"},
	} {
		if !columns[column.name] {
			if err := addColumn091(ctx, tx, "llm_usage_events", column.name, column.def); err != nil {
				return err
			}
			columns[column.name] = true
		}
	}

	return execStatements091(ctx, tx, []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_llm_usage_events_execution_operation ON llm_usage_events(execution_id, operation) WHERE execution_id IS NOT NULL AND execution_id != ''`,
		`CREATE INDEX IF NOT EXISTS idx_llm_usage_events_project_time ON llm_usage_events(project_id, occurred_at)`,
		`CREATE INDEX IF NOT EXISTS idx_llm_usage_events_provider_time ON llm_usage_events(provider, occurred_at)`,
		`CREATE INDEX IF NOT EXISTS idx_llm_usage_events_model_time ON llm_usage_events(model, occurred_at)`,
		`CREATE INDEX IF NOT EXISTS idx_llm_usage_events_execution ON llm_usage_events(execution_id)`,
	})
}

func ensureAccountUsageSnapshots091(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS account_usage_snapshots (
		  id TEXT PRIMARY KEY,
		  provider TEXT NOT NULL,
		  account_id TEXT,
		  agent_config_id TEXT,
		  plan_type TEXT NOT NULL DEFAULT '',
		  credits_remaining REAL,
		  primary_label TEXT NOT NULL DEFAULT '',
		  primary_used_percent REAL,
		  primary_window_minutes INTEGER,
		  primary_resets_at TEXT,
		  secondary_label TEXT NOT NULL DEFAULT '',
		  secondary_used_percent REAL,
		  secondary_window_minutes INTEGER,
		  secondary_resets_at TEXT,
		  model_limit_label TEXT NOT NULL DEFAULT '',
		  model_limit_used_percent REAL,
		  model_limit_window_minutes INTEGER,
		  model_limit_resets_at TEXT,
		  rate_limit_reached_type TEXT NOT NULL DEFAULT '',
		  raw_json TEXT NOT NULL DEFAULT '{}',
		  fetched_at TEXT NOT NULL DEFAULT (datetime('now')),
		  created_at TEXT NOT NULL DEFAULT (datetime('now')),
		  account_display_name TEXT NOT NULL DEFAULT '',
		  account_detail TEXT NOT NULL DEFAULT '',
		  billing_label TEXT NOT NULL DEFAULT '',
		  subscription_status TEXT NOT NULL DEFAULT '',
		  extra_usage_label TEXT NOT NULL DEFAULT '',
		  extra_usage_monthly_limit_usd REAL,
		  extra_usage_used_usd REAL,
		  FOREIGN KEY(agent_config_id) REFERENCES agent_configs(id) ON DELETE SET NULL
		);
	`); err != nil {
		return fmt.Errorf("creating account_usage_snapshots: %w", err)
	}

	columns, err := tableColumns091(ctx, tx, "account_usage_snapshots")
	if err != nil {
		return err
	}
	for _, column := range []struct {
		name string
		def  string
	}{
		{"provider", "TEXT NOT NULL DEFAULT ''"},
		{"account_id", "TEXT"},
		{"agent_config_id", "TEXT"},
		{"plan_type", "TEXT NOT NULL DEFAULT ''"},
		{"credits_remaining", "REAL"},
		{"primary_label", "TEXT NOT NULL DEFAULT ''"},
		{"primary_used_percent", "REAL"},
		{"primary_window_minutes", "INTEGER"},
		{"primary_resets_at", "TEXT"},
		{"secondary_label", "TEXT NOT NULL DEFAULT ''"},
		{"secondary_used_percent", "REAL"},
		{"secondary_window_minutes", "INTEGER"},
		{"secondary_resets_at", "TEXT"},
		{"model_limit_label", "TEXT NOT NULL DEFAULT ''"},
		{"model_limit_used_percent", "REAL"},
		{"model_limit_window_minutes", "INTEGER"},
		{"model_limit_resets_at", "TEXT"},
		{"rate_limit_reached_type", "TEXT NOT NULL DEFAULT ''"},
		{"raw_json", "TEXT NOT NULL DEFAULT '{}'"},
		{"fetched_at", "TEXT NOT NULL DEFAULT (datetime('now'))"},
		{"created_at", "TEXT NOT NULL DEFAULT (datetime('now'))"},
		{"account_display_name", "TEXT NOT NULL DEFAULT ''"},
		{"account_detail", "TEXT NOT NULL DEFAULT ''"},
		{"billing_label", "TEXT NOT NULL DEFAULT ''"},
		{"subscription_status", "TEXT NOT NULL DEFAULT ''"},
		{"extra_usage_label", "TEXT NOT NULL DEFAULT ''"},
		{"extra_usage_monthly_limit_usd", "REAL"},
		{"extra_usage_used_usd", "REAL"},
	} {
		if !columns[column.name] {
			if err := addColumn091(ctx, tx, "account_usage_snapshots", column.name, column.def); err != nil {
				return err
			}
			columns[column.name] = true
		}
	}

	return execStatements091(ctx, tx, []string{
		`CREATE INDEX IF NOT EXISTS idx_account_usage_snapshots_provider_time ON account_usage_snapshots(provider, fetched_at)`,
		`CREATE INDEX IF NOT EXISTS idx_account_usage_snapshots_agent_time ON account_usage_snapshots(agent_config_id, fetched_at)`,
	})
}

func ensureAccountUsageExtraLimits091(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS account_usage_extra_limits (
		  id TEXT PRIMARY KEY,
		  snapshot_id TEXT NOT NULL,
		  provider TEXT NOT NULL,
		  account_id TEXT,
		  agent_config_id TEXT,
		  limit_key TEXT NOT NULL,
		  label TEXT NOT NULL DEFAULT '',
		  used_percent REAL,
		  window_minutes INTEGER,
		  reset_at TEXT,
		  raw_json TEXT NOT NULL DEFAULT '{}',
		  created_at TEXT NOT NULL DEFAULT (datetime('now')),
		  FOREIGN KEY(snapshot_id) REFERENCES account_usage_snapshots(id) ON DELETE CASCADE,
		  FOREIGN KEY(agent_config_id) REFERENCES agent_configs(id) ON DELETE SET NULL
		);
	`); err != nil {
		return fmt.Errorf("creating account_usage_extra_limits: %w", err)
	}

	columns, err := tableColumns091(ctx, tx, "account_usage_extra_limits")
	if err != nil {
		return err
	}
	for _, column := range []struct {
		name string
		def  string
	}{
		{"snapshot_id", "TEXT NOT NULL DEFAULT ''"},
		{"provider", "TEXT NOT NULL DEFAULT ''"},
		{"account_id", "TEXT"},
		{"agent_config_id", "TEXT"},
		{"limit_key", "TEXT NOT NULL DEFAULT ''"},
		{"label", "TEXT NOT NULL DEFAULT ''"},
		{"used_percent", "REAL"},
		{"window_minutes", "INTEGER"},
		{"reset_at", "TEXT"},
		{"raw_json", "TEXT NOT NULL DEFAULT '{}'"},
		{"created_at", "TEXT NOT NULL DEFAULT (datetime('now'))"},
	} {
		if !columns[column.name] {
			if err := addColumn091(ctx, tx, "account_usage_extra_limits", column.name, column.def); err != nil {
				return err
			}
			columns[column.name] = true
		}
	}

	return execStatements091(ctx, tx, []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_account_usage_extra_limits_snapshot_key ON account_usage_extra_limits(snapshot_id, limit_key)`,
		`CREATE INDEX IF NOT EXISTS idx_account_usage_extra_limits_account ON account_usage_extra_limits(provider, account_id, limit_key)`,
		`CREATE INDEX IF NOT EXISTS idx_account_usage_extra_limits_snapshot ON account_usage_extra_limits(snapshot_id)`,
	})
}

func backfillLLMUsageEvents091(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO llm_usage_events (
		  id, provider, account_id, project_id, task_id, execution_id, chat_thread_id, turn_id,
		  agent_config_id, model, operation, status, error_message,
		  input_tokens, output_tokens, cached_input_tokens, cache_creation_input_tokens,
		  cache_read_input_tokens, reasoning_output_tokens, total_tokens, cost_usd, latency_ms,
		  context_window, max_output_tokens, provider_response_id, raw_usage_json, occurred_at
		)
		SELECT
		  lower(hex(randomblob(16))), ac.provider, NULLIF(ac.oauth_account_id, ''), t.project_id, e.task_id, e.id, NULL, e.id,
		  ac.id, ac.model, CASE WHEN e.is_followup = 1 THEN 'task_followup' WHEN t.category = 'chat' THEN 'streaming' ELSE 'task' END,
		  e.status, e.error_message,
		  0, 0, 0, 0,
		  0, 0, e.tokens_used, NULL, NULLIF(e.duration_ms, 0),
		  NULL, NULL, NULL, json_object('total_tokens', e.tokens_used, 'source', 'executions_backfill'), COALESCE(e.completed_at, e.started_at, datetime('now'))
		FROM executions e
		JOIN tasks t ON t.id = e.task_id
		JOIN agent_configs ac ON ac.id = e.agent_config_id
		WHERE e.tokens_used > 0
		  AND ac.provider IN ('anthropic', 'openai')
		  AND (ac.auth_method IN ('oauth', 'api_key') OR ac.api_key != '')
		  AND NOT EXISTS (
		    SELECT 1 FROM llm_usage_events existing
		    WHERE existing.execution_id = e.id
		      AND existing.operation = CASE WHEN e.is_followup = 1 THEN 'task_followup' WHEN t.category = 'chat' THEN 'streaming' ELSE 'task' END
		  );`)
	if err != nil {
		return fmt.Errorf("backfilling llm usage events: %w", err)
	}
	return nil
}

func tableColumns091(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("reading %s columns: %w", table, err)
	}
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, fmt.Errorf("scanning %s column: %w", table, err)
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func addColumn091(ctx context.Context, tx *sql.Tx, table, name, definition string) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+name+` `+definition); err != nil {
		return fmt.Errorf("adding %s.%s: %w", table, name, err)
	}
	return nil
}

func execStatements091(ctx context.Context, tx *sql.Tx, statements []string) error {
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("150_llm_usage_local_bucket_indexes.go", upLLMUsageLocalBucketIndexes150, downLLMUsageLocalBucketIndexes150)
}

func upLLMUsageLocalBucketIndexes150(ctx context.Context, tx *sql.Tx) error {
	return execStatements091(ctx, tx, []string{
		`CREATE INDEX IF NOT EXISTS idx_llm_usage_events_project_time_aggregate ON llm_usage_events(project_id, occurred_at, provider, account_id, model, input_tokens, output_tokens, cached_input_tokens, reasoning_output_tokens, total_tokens, cost_usd)`,
	})
}

func downLLMUsageLocalBucketIndexes150(ctx context.Context, tx *sql.Tx) error {
	return execStatements091(ctx, tx, []string{
		`DROP INDEX IF EXISTS idx_llm_usage_events_project_time_aggregate`,
	})
}

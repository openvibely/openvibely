package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("199_agents_live_page_indexes.go", upAgentsLivePageIndexes199, downAgentsLivePageIndexes199)
}

func upAgentsLivePageIndexes199(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE INDEX IF NOT EXISTS idx_agents_live_name_page
			ON agents(name COLLATE NOCASE ASC, name ASC, id ASC)
			WHERE COALESCE(generated_status, 'user_edited') <> 'archived'`,
		`CREATE INDEX IF NOT EXISTS idx_agents_live_updated_page
			ON agents(updated_at DESC, id DESC)
			WHERE COALESCE(generated_status, 'user_edited') <> 'archived'`,
		`CREATE INDEX IF NOT EXISTS idx_agents_live_created_page
			ON agents(created_at DESC, id DESC)
			WHERE COALESCE(generated_status, 'user_edited') <> 'archived'`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func downAgentsLivePageIndexes199(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`DROP INDEX IF EXISTS idx_agents_live_created_page`,
		`DROP INDEX IF EXISTS idx_agents_live_updated_page`,
		`DROP INDEX IF EXISTS idx_agents_live_name_page`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

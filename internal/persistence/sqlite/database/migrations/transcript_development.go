package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// NormalizeTranscriptDevelopmentVersions adopts unpublished 217–221 databases
// without rebuilding their transcript rows. Run before Goose examines versions.
// Main owns versions 217–219; the consolidated transcript migration is 220.
func NormalizeTranscriptDevelopmentVersions(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='conversation_items'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return tx.Commit()
	}
	var legacy int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM goose_db_version g WHERE is_applied=1 AND id=(SELECT max(id) FROM goose_db_version latest WHERE latest.version_id=g.version_id) AND version_id IN (217,218,220,221)`).Scan(&legacy); err != nil {
		return err
	}
	var ranges int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='conversation_checkpoint_ranges'`).Scan(&ranges); err != nil {
		return err
	}
	// A consolidated schema without intermediate IDs needs no adoption. A main
	// organization table means 217/218 can belong to main, not development.
	var organizations int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='organizations'`).Scan(&organizations); err != nil {
		return err
	}
	if ranges != 0 && (legacy == 0 || organizations != 0) {
		// Main's 219 removes account snapshots. Earlier branch databases used
		// 219 for transcripts instead. Only that known schema collision needs
		// applying main's 219; never delete unrelated later migration records.
		var snapshots int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='account_usage_snapshots'`).Scan(&snapshots); err != nil {
			return err
		}
		if snapshots != 0 {
			migration, readErr := FS.ReadFile("219_drop_account_usage_snapshots.sql")
			if readErr != nil {
				return readErr
			}
			up, _, _ := strings.Cut(string(migration), "-- +goose Down")
			if _, err = tx.ExecContext(ctx, up); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO goose_db_version(version_id,is_applied) SELECT 219,1 WHERE NOT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id=219 AND is_applied=1 AND id=(SELECT max(id) FROM goose_db_version WHERE version_id=219))`); err != nil {
				return err
			}
		}
		// The complete schema is already installed, including databases which
		// recorded the short-lived offset-only 220. Adopt its final number.
		if _, err = tx.ExecContext(ctx, `INSERT INTO goose_db_version(version_id,is_applied) SELECT 220,1 WHERE NOT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id=220 AND is_applied=1 AND id=(SELECT max(id) FROM goose_db_version WHERE version_id=220))`); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, streamOffsetIndexSQL); err != nil {
			return err
		}
		return tx.Commit()
	}
	if organizations != 0 {
		return fmt.Errorf("ambiguous intermediate transcript schema with main organizations; refusing to rewrite migration history")
	}
	var version int64
	if err = tx.QueryRowContext(ctx, `SELECT max(version_id) FROM goose_db_version g WHERE is_applied=1 AND id=(SELECT max(id) FROM goose_db_version latest WHERE latest.version_id=g.version_id)`).Scan(&version); err != nil {
		return err
	}
	if version < 217 || version > 221 {
		return fmt.Errorf("unexpected development transcript version %d", version)
	}
	// Validate the recorded stage before changing anything. Missing/partial DDL
	// must fail closed, not silently relabel a damaged database.
	for _, check := range []struct {
		name       string
		introduced int64
	}{
		{"conversation_checkpoint_references", 218}, {"execution_stream_text", 219}, {"conversation_checkpoint_ranges", 221},
	} {
		var found int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, check.name).Scan(&found); err != nil {
			return err
		}
		if (found != 0) != (version >= check.introduced) {
			return fmt.Errorf("development transcript schema does not match version %d: %s", version, check.name)
		}
	}
	var plan int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('executions') WHERE name='display_has_plan'`).Scan(&plan); err != nil {
		return err
	}
	if (plan != 0) != (version >= 220) {
		return fmt.Errorf("development transcript metadata does not match version %d", version)
	}
	for next := version + 1; next <= 221; next++ {
		if err = ApplyTranscriptDevelopmentStep(ctx, tx, next, true); err != nil {
			return fmt.Errorf("adopting transcript stage %d: %w", next, err)
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM goose_db_version WHERE version_id BETWEEN 217 AND 221; INSERT INTO goose_db_version(version_id,is_applied) VALUES(220,1)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, streamOffsetIndexSQL); err != nil {
		return err
	}
	return tx.Commit()
}

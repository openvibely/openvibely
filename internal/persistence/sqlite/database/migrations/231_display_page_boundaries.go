package migrations

import (
	"context"
	"database/sql"

	"github.com/openvibely/openvibely/internal/persistence/displayindex"
	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddNamedMigrationContext("231_display_page_boundaries.go", upDisplayPageBoundaries, downDisplayPageBoundaries)
}
func upDisplayPageBoundaries(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, displayindex.Schema+displayindex.StreamSchema); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT execution_id FROM conversation_items WHERE execution_id IS NOT NULL AND generation=0 AND lane='display'`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = displayindex.Rebuild(ctx, tx, id); err != nil {
			return err
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT DISTINCT execution_id FROM execution_stream_text`)
	if err != nil {
		return err
	}
	ids = nil
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = displayindex.IndexStream(ctx, tx, id, true); err != nil {
			return err
		}
		if err = displayindex.RefreshPlan(ctx, tx, id); err != nil {
			return err
		}
	}

	return nil
}
func downDisplayPageBoundaries(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE execution_stream_page_state; DROP TABLE execution_stream_groups; DROP TABLE execution_display_plans; DROP TABLE execution_display_groups`)
	return err
}

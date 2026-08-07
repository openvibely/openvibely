package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// deleteByID deletes a single row identified by id from table and returns a
// not-found error (naming entityLabel) when no row matched.
func deleteByID(ctx context.Context, db *sql.DB, table, entityLabel, id string) error {
	result, err := db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, table), id)
	if err != nil {
		return fmt.Errorf("delete %s: %w", entityLabel, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("%s not found", entityLabel)
	}
	return nil
}

// countAny reports whether table contains any rows. errLabel is used only in
// the wrapped error message on query failure.
func countAny(ctx context.Context, db *sql.DB, table, errLabel string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table)).Scan(&count); err != nil {
		return false, fmt.Errorf("count %s: %w", errLabel, err)
	}
	return count > 0, nil
}

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

// upsertUserProject persists a single-key channel user's active project
// selection, refreshing updated_at on conflict. errLabel is used only in the
// wrapped error message on failure.
func upsertUserProject(ctx context.Context, db *sql.DB, table, keyCol, keyVal, projectID, errLabel string) error {
	query := fmt.Sprintf(
		`INSERT INTO %s (%s, project_id, updated_at)
		 VALUES (?, ?, datetime('now'))
		 ON CONFLICT(%s) DO UPDATE
		 SET project_id = excluded.project_id, updated_at = datetime('now')`,
		table, keyCol, keyCol)
	if _, err := db.ExecContext(ctx, query, keyVal, projectID); err != nil {
		return fmt.Errorf("set %s: %w", errLabel, err)
	}
	return nil
}

// getUserProject returns the active project ID for a single-key channel
// user, or "" if not set. errLabel is used only in the wrapped error message
// on query failure.
func getUserProject(ctx context.Context, db *sql.DB, table, keyCol, keyVal, errLabel string) (string, error) {
	query := fmt.Sprintf(`SELECT project_id FROM %s WHERE %s = ?`, table, keyCol)
	var projectID string
	err := db.QueryRowContext(ctx, query, keyVal).Scan(&projectID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get %s: %w", errLabel, err)
	}
	return projectID, nil
}

// deleteUserProject removes a single-key channel user's active project
// selection. errLabel is used only in the wrapped error message on failure.
func deleteUserProject(ctx context.Context, db *sql.DB, table, keyCol, keyVal, errLabel string) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE %s = ?`, table, keyCol)
	if _, err := db.ExecContext(ctx, query, keyVal); err != nil {
		return fmt.Errorf("delete %s: %w", errLabel, err)
	}
	return nil
}

// deleteByTaskID removes a task-context row keyed by task_id from table.
// errLabel is used only in the wrapped error message on failure.
func deleteByTaskID(ctx context.Context, db *sql.DB, table, taskID, errLabel string) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE task_id = ?`, table), taskID)
	if err != nil {
		return fmt.Errorf("delete %s: %w", errLabel, err)
	}
	return nil
}

// Package sqlitefixtures implements test state manipulation for SQLite.
package sqlitefixtures

import (
	"context"
	"database/sql"
	"time"

	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
)

type fixtures struct{ db *sql.DB }

// New uses the same writable database as the test's stores. The caller owns it.
func New(db *sql.DB) storefixture.Fixtures { return &fixtures{db: db} }

func (f *fixtures) setTime(ctx context.Context, query, id string, at time.Time) error {
	result, err := f.db.ExecContext(ctx, query, at.UTC(), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return persistence.ErrNotFound
	}
	return nil
}

func (f *fixtures) SetExecutionStartedAt(ctx context.Context, id string, at time.Time) error {
	return f.setTime(ctx, `UPDATE executions SET started_at = ? WHERE id = ?`, id, at)
}
func (f *fixtures) SetTaskUpdatedAt(ctx context.Context, id string, at time.Time) error {
	return f.setTime(ctx, `UPDATE tasks SET updated_at = ? WHERE id = ?`, id, at)
}
func (f *fixtures) SetTaskCompletedAt(ctx context.Context, id string, at time.Time) error {
	return f.setTime(ctx, `UPDATE tasks SET completed_at = ? WHERE id = ?`, id, at)
}
func (f *fixtures) CountAutomations(ctx context.Context) (int, error) {
	var count int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automations`).Scan(&count)
	return count, err
}

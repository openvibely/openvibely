package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	modernsqlite "modernc.org/sqlite"
)

// SQLite can execute BEGIN before the driver reports cancellation. Inject that
// outcome after a real BEGIN, so this regression does not depend on race timing.
type interruptedBeginConnector struct {
	dsn    string
	cancel context.CancelFunc
	fired  atomic.Bool
}

func (c *interruptedBeginConnector) Driver() driver.Driver { return &modernsqlite.Driver{} }
func (c *interruptedBeginConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &interruptedBeginConn{Conn: conn, connector: c}, nil
}

type interruptedBeginConn struct {
	driver.Conn
	connector *interruptedBeginConnector
}

func (c *interruptedBeginConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if err == nil && query == "BEGIN IMMEDIATE" && c.connector.fired.CompareAndSwap(false, true) {
		c.connector.cancel()
		return nil, context.Canceled
	}
	return result, err
}

func TestInterruptedBeginDoesNotPoisonWriterConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	connector := &interruptedBeginConnector{dsn: filepath.Join(t.TempDir(), "recovery.db"), cancel: cancel}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer db.Close()
	_, err := db.ExecContext(t.Context(), "CREATE TABLE recovery (value TEXT)")
	require.NoError(t, err)
	require.ErrorIs(t, withImmediateTx(ctx, db, func(sqlExecutor) error {
		t.Fatal("transaction body ran after interrupted BEGIN")
		return nil
	}), context.Canceled)
	require.True(t, connector.fired.Load())
	// A subsequent unrelated request must be able to write without restarting.
	require.NoError(t, withImmediateTx(t.Context(), db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO recovery VALUES ('saved')")
		return err
	}))
	var count int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM recovery").Scan(&count))
	require.Equal(t, 1, count)
}

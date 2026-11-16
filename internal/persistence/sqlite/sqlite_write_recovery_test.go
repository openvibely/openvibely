package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	modernsqlite "modernc.org/sqlite"
)

// SQLite can execute BEGIN before the driver reports cancellation. Inject that
// outcome after a real BEGIN, so this regression does not depend on race timing.
type interruptedBeginConnector struct {
	dsn              string
	cancel           context.CancelFunc
	fired            atomic.Bool
	skipBegin        bool
	failRollback     bool
	opens            atomic.Int32
	closes           atomic.Int32
	rollbacks        atomic.Int32
	cleanupCancelled atomic.Bool
}

func (c *interruptedBeginConnector) Driver() driver.Driver { return &modernsqlite.Driver{} }
func (c *interruptedBeginConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	c.opens.Add(1)
	return &interruptedBeginConn{Conn: conn, connector: c}, nil
}

type interruptedBeginConn struct {
	driver.Conn
	connector *interruptedBeginConnector
}

func (c *interruptedBeginConn) Close() error {
	c.connector.closes.Add(1)
	return c.Conn.Close()
}

func (c *interruptedBeginConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if query == "ROLLBACK" {
		c.connector.rollbacks.Add(1)
		if ctx.Err() != nil {
			c.connector.cleanupCancelled.Store(true)
		}
		if c.connector.failRollback {
			return nil, errors.New("injected rollback failure")
		}
	}
	if query == "BEGIN IMMEDIATE" && c.connector.fired.CompareAndSwap(false, true) {
		if !c.connector.skipBegin {
			if _, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args); err != nil {
				return nil, err
			}
		}
		c.connector.cancel()
		return nil, context.Canceled
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func TestInterruptedBeginDoesNotPoisonWriterConnection(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		skipBegin, failRollback bool
		wantConnections         int32
	}{
		{name: "rollback_open_transaction", wantConnections: 1},
		{name: "no_transaction_started", skipBegin: true, wantConnections: 1},
		{name: "discard_after_cleanup_failure", failRollback: true, wantConnections: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			connector := &interruptedBeginConnector{dsn: filepath.Join(t.TempDir(), "recovery.db"), cancel: cancel, skipBegin: tc.skipBegin, failRollback: tc.failRollback}
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
			require.Equal(t, int32(1), connector.rollbacks.Load())
			require.False(t, connector.cleanupCancelled.Load())
			require.Equal(t, tc.wantConnections-1, connector.closes.Load())
			// A subsequent unrelated request must be able to write without restarting.
			require.NoError(t, withImmediateTx(t.Context(), db, func(tx sqlExecutor) error {
				_, err := tx.ExecContext(t.Context(), "INSERT INTO recovery VALUES ('saved')")
				return err
			}))
			var count int
			require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM recovery").Scan(&count))
			require.Equal(t, 1, count)
			require.Equal(t, tc.wantConnections, connector.opens.Load())
		})
	}
}

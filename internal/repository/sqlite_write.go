package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"sync"
	"time"
)

type SQLExecutor interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

type sqlExecutor = SQLExecutor

type queryExecutor interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}

var dedicatedWriters sync.Map

func RegisterDedicatedWriter(reader, writer *sql.DB) func() {
	if reader == nil || writer == nil || reader == writer {
		return func() {}
	}
	dedicatedWriters.Store(reader, writer)
	return func() {
		dedicatedWriters.CompareAndDelete(reader, writer)
	}
}

func writeDatabase(db *sql.DB) *sql.DB {
	if writer, ok := dedicatedWriters.Load(db); ok {
		return writer.(*sql.DB)
	}
	return db
}

func beginImmediateTx(ctx context.Context, db *sql.DB) (*manualTx, func(), error) {
	conn, cleanup, err := beginImmediateConn(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	tx := &manualTx{conn: conn, ctx: ctx}
	return tx, func() {
		_ = tx.Rollback()
		cleanup()
	}, nil
}

func withImmediateTx(ctx context.Context, db *sql.DB, fn func(SQLExecutor) error) error {
	tx, cleanup, err := beginImmediateTx(ctx, db)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func execBoundSQLite(ctx context.Context, db *sql.DB, query string, args ...interface{}) (result sql.Result, err error) {
	err = withBoundSQLiteConn(ctx, db, func(conn *sql.Conn) error {
		result, err = conn.ExecContext(ctx, query, args...)
		return err
	})
	return result, err
}

type boundSQLiteRow struct {
	ctx   context.Context
	db    *sql.DB
	query string
	args  []interface{}
}

func queryRowBoundSQLite(ctx context.Context, db *sql.DB, query string, args ...interface{}) *boundSQLiteRow {
	return &boundSQLiteRow{ctx: ctx, db: db, query: query, args: args}
}

func (r *boundSQLiteRow) Scan(dest ...interface{}) error {
	return withBoundSQLiteConn(r.ctx, r.db, func(conn *sql.Conn) error {
		return conn.QueryRowContext(r.ctx, r.query, r.args...).Scan(dest...)
	})
}

func withBoundSQLiteConn(ctx context.Context, db *sql.DB, fn func(*sql.Conn) error) error {
	db = writeDatabase(db)
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	restoreBusyTimeout, err := boundSQLiteBusyTimeoutToContext(ctx, conn)
	if err != nil {
		return err
	}
	defer restoreBusyTimeout()
	for {
		err := fn(conn)
		if !shouldRetrySQLiteBusyUntilCancellation(ctx, err) {
			if ctx.Err() != nil && isSQLiteBusy(err) {
				return ctx.Err()
			}
			return err
		}
	}
}

func beginImmediateConn(ctx context.Context, db *sql.DB) (*sql.Conn, func(), error) {
	db = writeDatabase(db)
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	restoreBusyTimeout, err := boundSQLiteBusyTimeoutToContext(ctx, conn)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	for {
		_, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`)
		if !shouldRetrySQLiteBusyUntilCancellation(ctx, err) {
			break
		}
	}
	if err != nil {
		if ctx.Err() != nil && isSQLiteBusy(err) {
			err = ctx.Err()
		}
		restoreBusyTimeout()
		_ = conn.Close()
		return nil, nil, err
	}
	cleaned := false
	return conn, func() {
		if cleaned {
			return
		}
		cleaned = true
		rollbackCtx, cancel := context.WithTimeout(context.Background(), sqliteBusyTimeoutRestoreReserve)
		defer cancel()
		_, _ = conn.ExecContext(rollbackCtx, `ROLLBACK`)
		restoreBusyTimeout()
		_ = conn.Close()
	}, nil
}

const (
	sqliteBusyTimeoutRestoreReserve = 250 * time.Millisecond
	sqliteCancellationPollInterval  = 50 * time.Millisecond
)

func boundSQLiteBusyTimeoutToContext(ctx context.Context, conn *sql.Conn) (func(), error) {
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline && ctx.Done() == nil {
		return func() {}, nil
	}
	var previousMS int
	if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&previousMS); err != nil {
		return nil, err
	}
	boundedMS := int(sqliteCancellationPollInterval / time.Millisecond)
	if hasDeadline {
		deadlineBoundMS := int((time.Until(deadline) - sqliteBusyTimeoutRestoreReserve) / time.Millisecond)
		if deadlineBoundMS < 1 {
			deadlineBoundMS = 1
		}
		if deadlineBoundMS < boundedMS {
			boundedMS = deadlineBoundMS
		}
	}
	if previousMS > 0 && previousMS <= boundedMS {
		return func() {}, nil
	}

	var restoreOnce sync.Once
	restore := func() {
		restoreOnce.Do(func() {
			// A canceled modernc statement can finish applying a PRAGMA before it
			// reports the context error. Let interruption cleanup settle first.
			if ctx.Err() != nil {
				time.Sleep(sqliteCancellationPollInterval)
			}
			restoreCtx, cancel := context.WithTimeout(context.Background(), sqliteBusyTimeoutRestoreReserve)
			defer cancel()
			query := fmt.Sprintf(`PRAGMA busy_timeout=%d`, previousMS)
			for restoreCtx.Err() == nil {
				if _, err := conn.ExecContext(restoreCtx, query); err == nil {
					var restoredMS int
					if err := conn.QueryRowContext(restoreCtx, `PRAGMA busy_timeout`).Scan(&restoredMS); err == nil && restoredMS == previousMS {
						return
					}
				}
				time.Sleep(5 * time.Millisecond)
			}
			// Never return a connection with altered connection-local state.
			_ = conn.Raw(func(raw any) error {
				if driverConn, ok := raw.(driver.Conn); ok {
					_ = driverConn.Close()
				}
				return driver.ErrBadConn
			})
		})
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA busy_timeout=%d`, boundedMS)); err != nil {
		// SQLite may apply the PRAGMA before modernc reports cancellation.
		restore()
		return nil, err
	}
	return restore, nil
}

func shouldRetrySQLiteBusyUntilCancellation(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil || ctx.Done() == nil {
		return false
	}
	return isSQLiteBusy(err)
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToUpper(err.Error())
	return !strings.Contains(message, "BUSY_SNAPSHOT") &&
		(strings.Contains(message, "SQLITE_BUSY") || strings.Contains(message, "DATABASE IS LOCKED"))
}

type manualTx struct {
	conn *sql.Conn
	ctx  context.Context
	done bool
}

func (t *manualTx) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return t.conn.ExecContext(ctx, query, args...)
}

func (t *manualTx) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return t.conn.QueryContext(ctx, query, args...)
}

func (t *manualTx) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return t.conn.QueryRowContext(ctx, query, args...)
}

func (t *manualTx) Commit() error {
	if t.done {
		return nil
	}
	_, err := t.conn.ExecContext(t.ctx, `COMMIT`)
	if err == nil {
		t.done = true
	}
	return err
}

func (t *manualTx) Rollback() error {
	if t.done {
		return nil
	}
	t.done = true
	rollbackCtx := t.ctx
	cancel := func() {}
	if rollbackCtx.Err() != nil {
		rollbackCtx, cancel = context.WithTimeout(context.WithoutCancel(t.ctx), sqliteBusyTimeoutRestoreReserve)
	}
	defer cancel()
	_, err := t.conn.ExecContext(rollbackCtx, `ROLLBACK`)
	return err
}

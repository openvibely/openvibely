package repository

import (
	"context"
	"database/sql"
	"sync"

	"github.com/openvibely/openvibely/internal/database"
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
	restoreBusyTimeout, err := database.BindSQLiteBusyTimeoutToContext(ctx, conn)
	if err != nil {
		return err
	}
	defer restoreBusyTimeout()
	for {
		err := fn(conn)
		if !database.ShouldRetrySQLiteBusyUntilCancellation(ctx, err) {
			if ctx.Err() != nil && database.IsSQLiteBusy(err) {
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
	restoreBusyTimeout, err := database.BindSQLiteBusyTimeoutToContext(ctx, conn)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	for {
		_, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`)
		if !database.ShouldRetrySQLiteBusyUntilCancellation(ctx, err) {
			break
		}
	}
	if err != nil {
		if ctx.Err() != nil && database.IsSQLiteBusy(err) {
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
		rollbackCtx, cancel := database.NewSQLiteBusyTimeoutRestoreContext(context.Background())
		defer cancel()
		_, _ = conn.ExecContext(rollbackCtx, `ROLLBACK`)
		restoreBusyTimeout()
		_ = conn.Close()
	}, nil
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
		rollbackCtx, cancel = database.NewSQLiteBusyTimeoutRestoreContext(context.WithoutCancel(t.ctx))
	}
	defer cancel()
	_, err := t.conn.ExecContext(rollbackCtx, `ROLLBACK`)
	return err
}

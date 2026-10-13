package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	sqliteBusyTimeoutRestoreReserve = 250 * time.Millisecond
	sqliteCancellationPollInterval  = 50 * time.Millisecond
)

// BindSQLiteBusyTimeoutToContext temporarily bounds SQLite's connection-local
// busy timeout so lock waits observe caller cancellation. The returned cleanup
// restores and verifies the previous value, or discards the physical connection
// when restoration cannot be confirmed.
func BindSQLiteBusyTimeoutToContext(ctx context.Context, conn *sql.Conn) (func(), error) {
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
			restoreCtx, cancel := NewSQLiteBusyTimeoutRestoreContext(context.Background())
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

// NewSQLiteBusyTimeoutRestoreContext returns the shared bounded context used for
// SQLite rollback and connection-state restoration cleanup.
func NewSQLiteBusyTimeoutRestoreContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, sqliteBusyTimeoutRestoreReserve)
}

// ShouldRetrySQLiteBusyUntilCancellation reports whether an ordinary SQLite
// busy error should be retried while waiting for caller cancellation.
func ShouldRetrySQLiteBusyUntilCancellation(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil || ctx.Done() == nil {
		return false
	}
	return IsSQLiteBusy(err)
}

// IsSQLiteBusy identifies retryable SQLite writer-lock errors while excluding
// snapshot conflicts, which require restarting the transaction.
func IsSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToUpper(err.Error())
	return !strings.Contains(message, "BUSY_SNAPSHOT") &&
		(strings.Contains(message, "SQLITE_BUSY") || strings.Contains(message, "DATABASE IS LOCKED"))
}

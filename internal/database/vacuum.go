package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
)

const (
	vacuumInterval                 = 5 * time.Minute
	vacuumBatchPages               = 1000 // ~4MB per pass at the default 4KB page size
	vacuumMinPages                 = 2500 // ~10MB of waste before it is worth reclaiming
	vacuumMinPercent               = 10   // ...and only if that is a real fraction of the file
	vacuumCancellationPollInterval = 50 * time.Millisecond
	vacuumTimeoutRestoreReserve    = 250 * time.Millisecond
)

// StartIncrementalVacuum reclaims free pages in small batches on a ticker.
// Batches are kept small deliberately: SQLite still permits only one writer, so
// each pass briefly competes with other writes while WAL readers continue. It returns immediately;
// the goroutine exits when ctx is cancelled.
func StartIncrementalVacuum(ctx context.Context, db *sql.DB) {
	startIncrementalVacuum(ctx, db, vacuumInterval, nil)
}

func startIncrementalVacuum(ctx context.Context, db *sql.DB, interval time.Duration, beforeWrite func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reclaimOnceWithHook(ctx, db, beforeWrite)
			}
		}
	}()
	return done
}

func reclaimOnce(ctx context.Context, db *sql.DB) {
	reclaimOnceWithHook(ctx, db, nil)
}

func reclaimOnceWithHook(ctx context.Context, db *sql.DB, beforeWrite func()) {
	var free, total int
	if err := db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free); err != nil {
		return
	}
	if err := db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&total); err != nil {
		return
	}
	if free < vacuumMinPages || total == 0 || free*100/total < vacuumMinPercent {
		return
	}
	if beforeWrite != nil {
		beforeWrite()
	}
	if err := execIncrementalVacuum(ctx, db); err != nil {
		if ctx.Err() == nil && !isVacuumBusy(err) {
			applog.Infof("database: incremental vacuum failed: %v", err)
		}
		return
	}
	applog.Infof("database: reclaimed up to %d pages (%d free of %d)", vacuumBatchPages, free, total)
}

func execIncrementalVacuum(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	restore, err := boundVacuumBusyTimeout(ctx, conn)
	if err != nil {
		return err
	}
	defer restore()

	query := fmt.Sprintf("PRAGMA incremental_vacuum(%d)", vacuumBatchPages)
	for {
		_, err = conn.ExecContext(ctx, query)
		if !retryVacuumBusyUntilCancellation(ctx, err) {
			if ctx.Err() != nil && isVacuumBusy(err) {
				return ctx.Err()
			}
			return err
		}
	}
}

func boundVacuumBusyTimeout(ctx context.Context, conn *sql.Conn) (func(), error) {
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline && ctx.Done() == nil {
		return func() {}, nil
	}
	var previousMS int
	if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&previousMS); err != nil {
		return nil, err
	}
	boundedMS := int(vacuumCancellationPollInterval / time.Millisecond)
	if hasDeadline {
		boundedMS = int((time.Until(deadline) - vacuumTimeoutRestoreReserve) / time.Millisecond)
		if boundedMS < 1 {
			boundedMS = 1
		}
	}
	if previousMS > 0 && previousMS <= boundedMS {
		return func() {}, nil
	}
	restore := func() {
		// Give modernc's context interruption cleanup one polling interval to
		// settle before changing connection-local state again.
		if ctx.Err() != nil {
			time.Sleep(vacuumCancellationPollInterval)
		}
		restoreCtx, cancel := context.WithTimeout(context.Background(), vacuumTimeoutRestoreReserve)
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
		// Never release a physical connection with altered local state.
		_ = conn.Raw(func(raw any) error {
			if driverConn, ok := raw.(driver.Conn); ok {
				_ = driverConn.Close()
			}
			return driver.ErrBadConn
		})
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA busy_timeout=%d`, boundedMS)); err != nil {
		// SQLite may apply the PRAGMA before modernc observes cancellation and
		// returns ctx.Err, so cleanup is required even on a reported failure.
		restore()
		return nil, err
	}
	return restore, nil
}

func retryVacuumBusyUntilCancellation(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil || ctx.Done() == nil {
		return false
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return false
	}
	return isVacuumBusy(err)
}

func isVacuumBusy(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToUpper(err.Error())
	return !strings.Contains(message, "BUSY_SNAPSHOT") &&
		(strings.Contains(message, "SQLITE_BUSY") || strings.Contains(message, "DATABASE IS LOCKED"))
}

package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
)

const (
	vacuumInterval   = 5 * time.Minute
	vacuumBatchPages = 1000 // ~4MB per pass at the default 4KB page size
	vacuumMinPages   = 2500 // ~10MB of waste before it is worth reclaiming
	vacuumMinPercent = 10   // ...and only if that is a real fraction of the file
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
		if ctx.Err() == nil && !IsSQLiteBusy(err) {
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

	restore, err := BindSQLiteBusyTimeoutToContext(ctx, conn)
	if err != nil {
		return err
	}
	defer restore()

	query := fmt.Sprintf("PRAGMA incremental_vacuum(%d)", vacuumBatchPages)
	for {
		_, err = conn.ExecContext(ctx, query)
		if !ShouldRetrySQLiteBusyUntilCancellation(ctx, err) {
			if ctx.Err() != nil && IsSQLiteBusy(err) {
				return ctx.Err()
			}
			return err
		}
	}
}

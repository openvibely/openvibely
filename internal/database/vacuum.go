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
// Batches are kept small deliberately: the pool is capped at one connection, so
// each pass blocks all other queries for its duration. It returns immediately;
// the goroutine exits when ctx is cancelled.
func StartIncrementalVacuum(ctx context.Context, db *sql.DB) {
	go func() {
		ticker := time.NewTicker(vacuumInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reclaimOnce(ctx, db)
			}
		}
	}()
}

func reclaimOnce(ctx context.Context, db *sql.DB) {
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
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA incremental_vacuum(%d)", vacuumBatchPages)); err != nil {
		applog.Infof("database: incremental vacuum failed: %v", err)
		return
	}
	applog.Infof("database: reclaimed up to %d pages (%d free of %d)", vacuumBatchPages, free, total)
}

package database

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
)

const (
	vacuumInterval         = 5 * time.Minute
	vacuumBatchPages       = 1000
	vacuumMinPages         = 2500
	vacuumMinPercent       = 10
	vacuumActiveInterval   = 250 * time.Millisecond
	vacuumLockWait         = 25 * time.Millisecond
	vacuumOperationTimeout = time.Second
)

type vacuumStats struct{ pages, free, pageSize int64 }

func (s vacuumStats) eligible() bool {
	return s.free >= vacuumMinPages && s.pages > 0 && s.free*100/s.pages >= vacuumMinPercent
}

type vacuumResult struct {
	before, after   vacuumStats
	attempted, busy bool
	duration        time.Duration
	err             error
}

func (r vacuumResult) progress() bool {
	return r.attempted && r.err == nil && r.after.pages < r.before.pages
}

// next uses positive uniform jitter: B=min(1s*2^n, 5m), then choose
// uniformly in [B, min(B+B/4, 5m)]. At the cap the delay is exactly 5m.
// Pool contention uses the same backoff as SQLite lock contention.
type vacuumSchedule struct{ busyBase time.Duration }

func (s *vacuumSchedule) next(r vacuumResult, idle time.Duration, jitter func(int64) int64) time.Duration {
	if r.busy {
		if s.busyBase == 0 {
			s.busyBase = time.Second
		} else {
			s.busyBase = min(s.busyBase*2, vacuumInterval)
		}
		spread := min(s.busyBase/4, vacuumInterval-s.busyBase)
		return s.busyBase + time.Duration(jitter(int64(spread)+1))
	}
	if r.progress() {
		s.busyBase = 0
		if r.after.eligible() {
			return vacuumActiveInterval
		}
	}
	return idle
}

type vacuumTimer interface {
	channel() <-chan time.Time
	Reset(time.Duration) bool
	Stop() bool
}
type realVacuumTimer struct{ *time.Timer }

func (t realVacuumTimer) channel() <-chan time.Time { return t.C }

// StartIncrementalVacuum runs serial batches, releasing the connection before
// starting each delay. Logical shrinkage need not immediately shrink the main
// file: WAL checkpointing determines physical filesystem reclamation.
func StartIncrementalVacuum(ctx context.Context, db *sql.DB) {
	startIncrementalVacuum(ctx, db, vacuumInterval, nil)
}
func startIncrementalVacuum(ctx context.Context, db *sql.DB, interval time.Duration, beforeWrite func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		runVacuum(ctx, realVacuumTimer{time.NewTimer(interval)}, interval, func() vacuumResult {
			return reclaimOnceWithHook(ctx, db, beforeWrite)
		}, rand.Int64N)
	}()
	return done
}
func runVacuum(ctx context.Context, timer vacuumTimer, idle time.Duration, batch func() vacuumResult, jitter func(int64) int64) {
	defer timer.Stop()
	var schedule vacuumSchedule
	var lastLog time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.channel():
			if ctx.Err() != nil {
				return
			}
			r := batch()
			if ctx.Err() != nil {
				return
			}
			delay := schedule.next(r, idle, jitter)
			// At most one report per minute, including repeated contention/errors.
			if (r.attempted || r.err != nil) && (lastLog.IsZero() || time.Since(lastLog) >= time.Minute) {
				lastLog = time.Now()
				if r.err != nil {
					applog.Infof("database: incremental vacuum deferred busy=%t duration=%s retry=%s: %v", r.busy, r.duration, delay, r.err)
				} else {
					pages := r.before.pages - r.after.pages
					applog.Infof("database: incremental vacuum logical shrink=%d pages/%d bytes free_change=%d pages remaining_free=%d pages/%d bytes duration=%s progress=%t retry=%s (physical reclamation depends on WAL checkpointing)", pages, pages*r.before.pageSize, r.before.free-r.after.free, r.after.free, r.after.free*r.after.pageSize, r.duration, r.progress(), delay)
				}
			}
			timer.Reset(delay)
		}
	}
}
func reclaimOnce(ctx context.Context, db *sql.DB) { reclaimOnceWithHook(ctx, db, nil) }
func reclaimOnceWithHook(ctx context.Context, db *sql.DB, beforeWrite func()) (r vacuumResult) {
	started := time.Now()
	defer func() { r.duration = time.Since(started) }()
	// The writer pool can contain just one connection. Bound acquisition
	// separately from SQLite's lock wait, including the eligibility queries.
	acquireCtx, cancelAcquire := context.WithTimeout(ctx, vacuumLockWait)
	conn, err := db.Conn(acquireCtx)
	cancelAcquire()
	if err != nil {
		r.err = err
		r.busy = ctx.Err() == nil && acquireCtx.Err() == context.DeadlineExceeded
		return
	}
	defer conn.Close()
	opCtx, cancel := context.WithTimeout(ctx, vacuumOperationTimeout)
	defer cancel()
	restore, err := bindSQLiteBusyTimeout(opCtx, conn, int(vacuumLockWait/time.Millisecond))
	if err != nil {
		r.err = err
		r.busy = IsSQLiteBusy(err)
		return
	}
	defer restore()
	r.before, r.err = readVacuumStats(opCtx, conn)
	if r.err == nil && r.before.eligible() {
		r.attempted = true
		if beforeWrite != nil {
			beforeWrite()
		}
		r.err = execIncrementalVacuum(opCtx, conn)
		if r.err == nil {
			r.after, r.err = readVacuumStats(opCtx, conn)
		}
	}
	r.busy = IsSQLiteBusy(r.err)
	return
}
func readVacuumStats(ctx context.Context, conn *sql.Conn) (s vacuumStats, err error) {
	for _, field := range []struct {
		name  string
		value *int64
	}{
		{"page_size", &s.pageSize}, {"page_count", &s.pages}, {"freelist_count", &s.free},
	} {
		if err = conn.QueryRowContext(ctx, "PRAGMA "+field.name).Scan(field.value); err != nil {
			return
		}
	}
	return
}
func execIncrementalVacuum(ctx context.Context, conn *sql.Conn) error {
	// modernc v1.61.0 Exec drains SQLITE_ROW through SQLITE_DONE. The
	// substantial-freelist regression test verifies the whole batch executes.
	// Do not use ShouldRetrySQLiteBusyUntilCancellation here: the scheduler owns retries.
	_, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA incremental_vacuum(%d)", vacuumBatchPages))
	return err
}

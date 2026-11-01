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
	vacuumInterval      = 5 * time.Minute
	vacuumBatchPages    = 1
	vacuumMaxBatchPages = 512
	vacuumTarget        = 25 * time.Millisecond
	vacuumMinPages      = 2500
	vacuumMinPercent    = 10
	vacuumLockWait      = 25 * time.Millisecond
)

type vacuumStats struct{ pages, free, pageSize int64 }

func (s vacuumStats) eligible() bool {
	return s.free >= vacuumMinPages && s.pages > 0 && s.free*100/s.pages >= vacuumMinPercent
}

type vacuumResult struct {
	before, after                                vacuumStats
	attempted, busy                              bool
	beforeKnown, afterKnown                      bool
	pages                                        int
	stage                                        string
	duration, vacuumDuration, checkpointDuration time.Duration
	yieldDuration                                time.Duration
	checkpoint                                   vacuumCheckpoint
	err                                          error
}

func (r vacuumResult) progress() bool {
	return r.attempted && r.err == nil && r.after.pages < r.before.pages
}

// Only vacuum execution time controls sizing. Checkpoint/connection delays do
// not justify growing the next call. This is feedback, not a runtime deadline:
// even one page (or a passive checkpoint) may take longer than the target.
func nextVacuumPages(pages int, r vacuumResult) int {
	if !r.attempted {
		return pages
	}
	if r.vacuumDuration > vacuumTarget {
		return max(1, int(int64(pages)*int64(vacuumTarget)/int64(r.vacuumDuration)))
	}
	if !r.progress() || r.busy {
		return pages
	}
	if r.vacuumDuration <= 0 {
		return min(pages*2, vacuumMaxBatchPages)
	}
	return min(vacuumMaxBatchPages, pages*2, max(1, int(int64(pages)*int64(vacuumTarget)/int64(r.vacuumDuration))))
}

type vacuumCheckpoint struct{ busy, frames, checkpointed int }

func (c vacuumCheckpoint) complete() bool {
	return c.busy == 0 && c.frames >= 0 && c.checkpointed == c.frames
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
			return 0 // Admission yields to queued application writes, not a fixed sleep.
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
	unregister := RegisterWriterPriority(db)
	go func() {
		defer close(done)
		defer unregister()
		runVacuum(ctx, realVacuumTimer{time.NewTimer(interval)}, interval, func(pages int) vacuumResult {
			return reclaimBatch(ctx, db, pages, beforeWrite)
		}, rand.Int64N)
	}()
	return done
}
func runVacuum(ctx context.Context, timer vacuumTimer, idle time.Duration, batch func(int) vacuumResult, jitter func(int64) int64) {
	defer timer.Stop()
	var schedule vacuumSchedule
	var lastLog time.Time
	pages := vacuumBatchPages
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.channel():
			if ctx.Err() != nil {
				return
			}
			r := batch(pages)
			if ctx.Err() != nil {
				return
			}
			delay := schedule.next(r, idle, jitter)
			pages = nextVacuumPages(pages, r)
			// Include idle checks and checkpoint deferrals, but not every active batch.
			if lastLog.IsZero() || time.Since(lastLog) >= time.Minute {
				lastLog = time.Now()
				reclaimed, remaining := int64(-1), int64(-1)
				statsStage := "unknown"
				if r.beforeKnown {
					remaining = r.before.free * r.before.pageSize
					statsStage = "before"
				}
				if r.afterKnown {
					reclaimed = (r.before.pages - r.after.pages) * r.before.pageSize
					remaining = r.after.free * r.after.pageSize
					statsStage = "after"
				}
				applog.Infof("database: incremental vacuum stage=%s batch_pages=%d next_batch_pages=%d duration=%s yield_duration=%s vacuum_duration=%s checkpoint_duration=%s busy=%t checkpoint_busy=%d wal_frames=%d checkpointed_frames=%d logical_reclaimed_bytes=%d remaining_free_bytes=%d stats=%s next_delay=%s error=%v (bytes=-1 means unknown; logical shrink is not physical file shrink)", r.stage, r.pages, pages, r.duration, r.yieldDuration, r.vacuumDuration, r.checkpointDuration, r.busy, r.checkpoint.busy, r.checkpoint.frames, r.checkpoint.checkpointed, reclaimed, remaining, statsStage, delay, r.err)
			}
			timer.Reset(delay)
		}
	}
}
func reclaimOnce(ctx context.Context, db *sql.DB) { reclaimOnceWithHook(ctx, db, nil) }
func reclaimOnceWithHook(ctx context.Context, db *sql.DB, beforeWrite func()) vacuumResult {
	return reclaimBatch(ctx, db, vacuumBatchPages, beforeWrite)
}
func reclaimBatch(ctx context.Context, db *sql.DB, pages int, beforeWrite func()) (r vacuumResult) {
	r.pages = min(vacuumMaxBatchPages, max(1, pages))
	r.stage = "yield"
	r.checkpoint = vacuumCheckpoint{frames: -1, checkpointed: -1}
	started := time.Now()
	defer func() { r.duration = time.Since(started); r.busy = r.busy || IsSQLiteBusy(r.err) }()
	// This wait holds neither a pool connection nor a SQLite write lock. The
	// short pool acquisition timeout starts only after queued application writes
	// drain. Continuous application traffic may postpone maintenance indefinitely.
	releaseAdmission, err := yieldToApplicationWrites(ctx, db)
	r.yieldDuration = time.Since(started)
	if err != nil {
		r.err = err
		return
	}
	defer releaseAdmission()
	r.stage = "acquire"
	// Pool acquisition is separate from SQLite lock acquisition. Both are short;
	// execution itself remains cancellable by shutdown without a fixed deadline.
	acquireCtx, cancelAcquire := context.WithTimeout(ctx, vacuumLockWait)
	conn, err := db.Conn(acquireCtx)
	cancelAcquire()
	if err != nil {
		r.err = err
		r.busy = ctx.Err() == nil && acquireCtx.Err() == context.DeadlineExceeded
		return
	}
	defer conn.Close()
	r.stage = "configure"
	restore, err := bindSQLiteBusyTimeout(ctx, conn, int(vacuumLockWait/time.Millisecond))
	if err != nil {
		r.err = err
		return
	}
	defer restore()
	r.stage = "stats-before"
	r.before, r.err = readVacuumStats(ctx, conn)
	if r.err != nil {
		return
	}
	r.beforeKnown = true
	if !r.before.eligible() {
		r.stage = "ineligible"
		return
	}
	if beforeWrite != nil {
		beforeWrite()
	}

	// PASSIVE never waits for readers/writers to finish. SQLITE_OK alone does
	// not imply completion: an old reader can leave uncheckpointed WAL frames.
	r.stage = "checkpoint"
	checkpointStarted := time.Now()
	r.err = conn.QueryRowContext(ctx, "PRAGMA main.wal_checkpoint(PASSIVE)").Scan(&r.checkpoint.busy, &r.checkpoint.frames, &r.checkpoint.checkpointed)
	r.checkpointDuration = time.Since(checkpointStarted)
	if r.err != nil {
		return
	}
	if !r.checkpoint.complete() {
		r.stage = "checkpoint-deferred"
		// -1/-1 without BUSY indicates no WAL (e.g. an in-memory database).
		// Idle rather than repeatedly attempting cleanup there.
		r.busy = r.checkpoint.busy != 0 || r.checkpoint.frames >= 0
		return
	}
	r.stage = "vacuum"
	r.attempted = true
	vacuumStarted := time.Now()
	r.err = execIncrementalVacuum(ctx, conn, r.pages)
	r.vacuumDuration = time.Since(vacuumStarted)
	if r.err != nil {
		return
	}
	r.stage = "stats-after"
	r.after, r.err = readVacuumStats(ctx, conn)
	if r.err != nil {
		return
	}
	r.afterKnown = true
	r.stage = "complete"
	if !r.progress() {
		r.stage = "no-progress"
	}
	// Restoration and Close run before the caller schedules any pause or next
	// checkout. Admission is released last, after restoring and closing the
	// connection, so queued application writes run before another vacuum batch.
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
func execIncrementalVacuum(ctx context.Context, conn *sql.Conn, pages int) error {
	// modernc v1.61.0 Exec drains SQLITE_ROW through SQLITE_DONE. The
	// substantial-freelist regression test verifies the whole batch executes.
	// Do not use ShouldRetrySQLiteBusyUntilCancellation here: the scheduler owns retries.
	_, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA incremental_vacuum(%d)", pages))
	return err
}

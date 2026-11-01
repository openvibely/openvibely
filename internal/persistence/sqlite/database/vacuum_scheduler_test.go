package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestVacuumSchedule(t *testing.T) {
	var s vacuumSchedule
	busy := vacuumResult{busy: true}
	for i, base := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, 64 * time.Second, 128 * time.Second, 256 * time.Second, 300 * time.Second, 300 * time.Second} {
		low := s
		if got := low.next(busy, vacuumInterval, func(n int64) int64 { return 0 }); got != base {
			t.Fatalf("step %d lower bound %s != %s", i, got, base)
		}
		got := s.next(busy, vacuumInterval, func(n int64) int64 { return n - 1 })
		if want := min(base+base/4, vacuumInterval); got != want {
			t.Fatalf("step %d upper bound %s != %s", i, got, want)
		}
	}
	progress := vacuumResult{attempted: true, before: vacuumStats{pages: 10000, free: 8000}, after: vacuumStats{pages: 9000, free: 7000}}
	if got := s.next(progress, vacuumInterval, func(int64) int64 { return 0 }); got != 0 || s.busyBase != 0 {
		t.Fatalf("progress: delay=%s state=%+v", got, s)
	}
	if got := s.next(busy, vacuumInterval, func(int64) int64 { return 0 }); got != time.Second {
		t.Fatal(got)
	}
	for _, r := range []vacuumResult{{}, {attempted: true}, {err: errors.New("disk error")}, {attempted: true, before: vacuumStats{pages: 5000}, after: vacuumStats{pages: 4000}}} {
		if got := s.next(r, vacuumInterval, func(int64) int64 { return 0 }); got != vacuumInterval {
			t.Fatalf("idle delay %s for %+v", got, r)
		}
	}
}

type testVacuumTimer struct {
	ticks   chan time.Time
	resets  chan time.Duration
	stopped chan struct{}
}

func (t *testVacuumTimer) channel() <-chan time.Time  { return t.ticks }
func (t *testVacuumTimer) Reset(d time.Duration) bool { t.resets <- d; return false }
func (t *testVacuumTimer) Stop() bool                 { close(t.stopped); return true }
func TestVacuumTimerResetsOnlyAfterBatchAndCancels(t *testing.T) {
	timer := &testVacuumTimer{make(chan time.Time), make(chan time.Duration, 1), make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		runVacuum(ctx, timer, vacuumInterval, func(pages int) vacuumResult { close(entered); <-release; return vacuumResult{busy: true} }, func(int64) int64 { return 0 })
	}()
	timer.ticks <- time.Now()
	<-entered
	select {
	case d := <-timer.resets:
		t.Fatalf("reset during batch: %s", d)
	default:
	}
	close(release)
	if d := <-timer.resets; d != time.Second {
		t.Fatal(d)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timer wait did not cancel")
	}
	<-timer.stopped
}

func vacuumFixture(t *testing.T, rows int, incremental bool) *sql.DB {
	t.Helper()
	return vacuumFixtureWithDelete(t, rows, incremental, "DELETE FROM payload")
}

func vacuumFixtureWithDelete(t *testing.T, rows int, incremental bool, deletion string) *sql.DB {
	t.Helper()
	db, err := newSQLiteDatabase(filepath.Join(t.TempDir(), "vacuum.db"), func(db *sql.DB) error {
		mode := 0
		if incremental {
			mode = 2
		}
		for _, q := range []string{"PRAGMA auto_vacuum=" + []string{"0", "1", "2"}[mode], "PRAGMA journal_mode=WAL", "CREATE TABLE payload(body BLOB)", "CREATE TABLE writes(id INTEGER PRIMARY KEY, body TEXT)"} {
			if _, err := db.Exec(q); err != nil {
				return err
			}
		}
		_, err := db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x < ?) INSERT INTO payload SELECT zeroblob(4096) FROM n`, rows)
		if err != nil {
			return err
		}
		_, err = db.Exec(deletion)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	t.Cleanup(RegisterWriterPriority(db))
	return db
}
func TestVacuumFullBatchAndNoProgress(t *testing.T) {
	for _, incremental := range []bool{true, false} {
		db := vacuumFixture(t, 6000, incremental)
		r := reclaimOnceWithHook(context.Background(), db, nil)
		if r.err != nil || !r.attempted {
			t.Fatalf("%+v", r)
		}
		if incremental {
			if r.before.free-r.after.free != vacuumBatchPages || r.before.pages-r.after.pages < vacuumBatchPages {
				t.Fatalf("incomplete batch: %+v", r)
			}
		} else if r.progress() {
			t.Fatalf("unexpected progress: %+v", r)
		}
		assertVacuumPoolBusyTimeout(t, db)
	}
}
func TestVacuumCanCompleteAfterOneSecond(t *testing.T) {
	db := vacuumFixture(t, 6000, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := reclaimOnceWithHook(ctx, db, func() {
		// Simulate a slow attempt without relying on machine or database size.
		// The former internal one-second deadline expired during this pause.
		timer := time.NewTimer(1100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
		}
	})
	if r.err != nil || !r.progress() {
		t.Fatalf("attempt should complete under the caller's deadline: %+v", r)
	}
	assertVacuumPoolBusyTimeout(t, db)
}

func TestVacuumPoolContention(t *testing.T) {
	db := vacuumFixture(t, 4000, true)
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	r := reclaimOnceWithHook(context.Background(), db, nil)
	if !r.busy || r.err == nil || time.Since(start) > time.Second {
		t.Fatalf("unbounded pool wait: %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	r = reclaimOnceWithHook(ctx, db, nil)
	if !errors.Is(r.err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("pool cancellation: %+v", r)
	}
}

// Drive scheduler time manually while forcing real pool contention. This makes
// busy/backoff/recovery coverage independent of machine speed and write timing.
func TestVacuumSchedulerRecoversFromPoolContention(t *testing.T) {
	db := vacuumFixture(t, 6000, true)
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithCancel(context.Background())
	timer := &testVacuumTimer{make(chan time.Time), make(chan time.Duration, 1), make(chan struct{})}
	done := make(chan struct{})
	conn, err := db.Conn(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer conn.Close()
	go func() {
		defer close(done)
		runVacuum(ctx, timer, vacuumInterval, func(pages int) vacuumResult {
			r := reclaimBatch(ctx, db, pages, nil)
			if r.err != nil && !r.busy && ctx.Err() == nil {
				t.Errorf("batch: %v", r.err)
			}
			return r
		}, func(int64) int64 { return 0 })
	}()
	defer func() { cancel(); <-done }()
	advance := func(want time.Duration) {
		t.Helper()
		select {
		case timer.ticks <- time.Now():
		case <-time.After(2 * time.Second):
			t.Fatal("scheduler did not accept tick")
		}
		select {
		case got := <-timer.resets:
			if got != want {
				t.Fatalf("delay=%s, want %s", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("scheduler did not complete attempt")
		}
	}
	advance(time.Second)
	advance(2 * time.Second)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	advance(0)
	conn, err = db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	advance(time.Second) // Successful reclamation reset the busy backoff.
}

func TestVacuumHeldWriterReturnsBusyWithoutRetry(t *testing.T) {
	db := vacuumFixture(t, 4000, true)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	r := reclaimOnceWithHook(context.Background(), db, nil)
	if !r.busy || r.duration > time.Second {
		t.Fatalf("expected short busy wait: %+v", r)
	}
	t.Logf("held writer: cleanup returned after %s", r.duration)
	// The next user operation reuses the cleanup connection. It must still
	// wait longer than 25ms for the writer instead of inheriting that budget.
	userDone := make(chan error, 1)
	go func() {
		_, err := db.Exec("INSERT INTO writes(body) VALUES ('after cleanup')")
		userDone <- err
	}()
	select {
	case err := <-userDone:
		t.Fatalf("user write returned while writer held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-userDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("user write did not resume after lock release")
	}
}

// Disposable experiments use the production single-connection writer pool and
// scheduler. Each phase offers 500 writes/s (three seconds for small fixtures,
// ten seconds for the >1 GiB fixture). Arrival times are
// fixed in advance: a slow write leaves a queue of due requests, never dropped
// ticker events. Latency includes that queue, and all offered writes are drained.
// These observations are not CI latency/throughput SLOs.
func TestVacuumConcurrentWriteMeasurements(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		rows     int
		deletion string
		samples  int
	}{
		{"fully_deleted", 14000, "DELETE FROM payload", 1500},
		{"partially_deleted", 28000, "DELETE FROM payload WHERE rowid % 4 != 0", 1500},
		{"large_partially_deleted", 262144, "DELETE FROM payload WHERE rowid % 4 != 0", 5000},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if fixture.samples > 1500 && testing.Short() {
				t.Skip("large disposable-database experiment")
			}
			db := vacuumFixtureWithDelete(t, fixture.rows, true, fixture.deletion)
			db.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			samples := fixture.samples
			const cadence = 2 * time.Millisecond
			window := time.Duration(samples) * cadence
			var seq int
			var schema, dbPath string
			if err := db.QueryRowContext(ctx, "PRAGMA database_list").Scan(&seq, &schema, &dbPath); err != nil {
				t.Fatal(err)
			}
			reportSize := func(label string) {
				var pages, free, pageSize int64
				for _, q := range []struct {
					name  string
					value *int64
				}{{"page_count", &pages}, {"freelist_count", &free}, {"page_size", &pageSize}} {
					if err := db.QueryRowContext(ctx, "PRAGMA "+q.name).Scan(q.value); err != nil {
						t.Fatal(err)
					}
				}
				info, err := os.Stat(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				walBytes := int64(0)
				if wal, err := os.Stat(dbPath + "-wal"); err == nil {
					walBytes = wal.Size()
				}
				t.Logf("%s logical_bytes=%d free_bytes=%d main_file_bytes=%d wal_file_bytes=%d", label, pages*pageSize, free*pageSize, info.Size(), walBytes)
				if label == "before" && fixture.samples > 1500 && pages*pageSize < 1<<30 {
					t.Fatal("large fixture must exceed 1 GiB")
				}
			}
			reportSize("before")

			baseline, err := measureVacuumWrites(ctx, db, time.Now(), samples, cadence)
			if err != nil {
				t.Fatal(err)
			}

			start := time.Now()
			cleanupCtx, cancelCleanup := context.WithDeadline(ctx, start.Add(window))
			defer cancelCleanup()
			type writeResult struct {
				latencies []time.Duration
				err       error
			}
			finished := make(chan writeResult, 1)
			go func() {
				latencies, err := measureVacuumWrites(ctx, db, start, samples, cadence)
				finished <- writeResult{latencies, err}
			}()
			var reclaimed int64
			var batches, busy, maxPages int
			var maxBatch, maxVacuum, maxCheckpoint time.Duration
			var sizes []int
			// Run the actual scheduler, including bounded exponential backoff on pool
			// or SQLite contention. A busy attempt is an expected observation, not a
			// failure. The experiment window bounds even persistent contention.
			runVacuum(cleanupCtx, realVacuumTimer{time.NewTimer(0)}, vacuumInterval, func(pages int) vacuumResult {
				r := reclaimBatch(cleanupCtx, db, pages, nil)
				if cleanupCtx.Err() != nil {
					return r
				}
				if r.busy {
					busy++
				} else if r.err != nil {
					t.Errorf("batch: %v", r.err)
				} else if r.attempted && !r.progress() {
					t.Errorf("no progress: %+v", r)
				}
				if r.progress() {
					reclaimed += (r.before.pages - r.after.pages) * r.before.pageSize
					batches++
					maxBatch = max(maxBatch, r.duration)
					maxVacuum = max(maxVacuum, r.vacuumDuration)
					maxCheckpoint = max(maxCheckpoint, r.checkpointDuration)
					maxPages = max(maxPages, r.pages)
					sizes = append(sizes, r.pages)
				}
				return r
			}, func(int64) int64 { return 0 })
			elapsed := time.Since(start)
			writes := <-finished
			if writes.err != nil {
				t.Fatal(writes.err)
			}
			report := func(name string, v []time.Duration) {
				if len(v) != samples {
					t.Fatalf("%s completed %d/%d writes", name, len(v), samples)
				}
				sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
				t.Logf("%s arrival-to-completion latency n=%d p50=%s p95=%s p99=%s max=%s", name, len(v), v[len(v)/2], v[len(v)*95/100], v[len(v)*99/100], v[len(v)-1])
			}
			report("baseline", baseline)
			report("cleanup", writes.latencies)
			t.Logf("logical reclamation: batches=%d busy_attempts=%d bytes=%d elapsed=%s MiB/s=%.2f max_batch=%s max_vacuum=%s max_checkpoint=%s max_pages=%d sizes=%v workload_drained_after=%s", batches, busy, reclaimed, elapsed, float64(reclaimed)/(1<<20)/elapsed.Seconds(), maxBatch, maxVacuum, maxCheckpoint, maxPages, sizes, time.Since(start))
			reportSize("after")
			var liveRows int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM payload").Scan(&liveRows); err != nil {
				t.Fatal(err)
			}
			wantLive := 0
			if fixture.name != "fully_deleted" {
				wantLive = fixture.rows / 4
			}
			if liveRows != wantLive {
				t.Fatalf("live payload rows=%d, want %d", liveRows, wantLive)
			}
		})
	}
}

func TestVacuumWriteMeasurementsIncludeQueuedArrivals(t *testing.T) {
	db := vacuumFixture(t, 1, true)
	// All five arrivals are already due, modeling a worker stalled for 100ms.
	// Keep every request and include its queue wait in the reported latency.
	latencies, err := measureVacuumWrites(context.Background(), db, time.Now().Add(-100*time.Millisecond), 5, 2*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(latencies) != 5 {
		t.Fatalf("completed %d writes, want 5", len(latencies))
	}
	for _, latency := range latencies {
		if latency < 90*time.Millisecond {
			t.Fatalf("queue wait omitted: %s", latency)
		}
	}
}

func measureVacuumWrites(ctx context.Context, db *sql.DB, start time.Time, count int, cadence time.Duration) ([]time.Duration, error) {
	// Independent requests arrive even while previous writes are blocked. This
	// exercises admission priority with genuinely concurrent queued user writes,
	// not just a serial worker's catch-up queue or dropped ticker events.
	latencies := make([]time.Duration, count)
	var workers sync.WaitGroup
	var firstError sync.Once
	var writeErr error
	recordError := func(err error) { firstError.Do(func() { writeErr = err }) }
schedule:
	for i := 0; i < count; i++ {
		arrival := start.Add(time.Duration(i+1) * cadence)
		timer := time.NewTimer(max(time.Until(arrival), 0))
		select {
		case <-ctx.Done():
			timer.Stop()
			recordError(ctx.Err())
			break schedule
		case <-timer.C:
		}
		workers.Add(1)
		go func(index int, arrival time.Time) {
			defer workers.Done()
			conn, release, err := AcquireWriteConn(ctx, db)
			if err != nil {
				recordError(err)
				return
			}
			defer release()
			restore, err := BindSQLiteBusyTimeoutToContext(ctx, conn)
			if err != nil {
				recordError(err)
				return
			}
			_, err = conn.ExecContext(ctx, "INSERT INTO writes(body) VALUES ('write')")
			restore()
			release()
			if err != nil {
				recordError(err)
				return
			}
			latencies[index] = time.Since(arrival)
		}(i, arrival)
	}
	workers.Wait()
	return latencies, writeErr
}

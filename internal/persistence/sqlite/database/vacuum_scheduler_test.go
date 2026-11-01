package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
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
	if got := s.next(progress, vacuumInterval, func(int64) int64 { return 0 }); got != vacuumActiveInterval || s.busyBase != 0 {
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
		runVacuum(ctx, timer, vacuumInterval, func() vacuumResult { close(entered); <-release; return vacuumResult{busy: true} }, func(int64) int64 { return 0 })
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
		runVacuum(ctx, timer, vacuumInterval, func() vacuumResult {
			r := reclaimOnceWithHook(ctx, db, nil)
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
	advance(vacuumActiveInterval)
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
// scheduler. Each phase offers 500 writes/s for three seconds. Arrival times are
// fixed in advance: a slow write leaves a queue of due requests, never dropped
// ticker events. Latency includes that queue, and all offered writes are drained.
// These observations are not CI latency/throughput SLOs.
func TestVacuumConcurrentWriteMeasurements(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		rows     int
		deletion string
	}{
		{"fully_deleted", 14000, "DELETE FROM payload"},
		{"partially_deleted", 28000, "DELETE FROM payload WHERE rowid % 4 != 0"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			db := vacuumFixtureWithDelete(t, fixture.rows, true, fixture.deletion)
			db.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			const samples = 1500
			const cadence = 2 * time.Millisecond
			const window = samples * cadence
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
			var batches, busy int
			var maxBatch time.Duration
			// Run the actual scheduler, including bounded exponential backoff on pool
			// or SQLite contention. A busy attempt is an expected observation, not a
			// failure. The experiment window bounds even persistent contention.
			runVacuum(cleanupCtx, realVacuumTimer{time.NewTimer(0)}, vacuumInterval, func() vacuumResult {
				r := reclaimOnceWithHook(cleanupCtx, db, nil)
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
			t.Logf("logical reclamation: batches=%d busy_attempts=%d bytes=%d elapsed=%s MiB/s=%.2f max_batch=%s workload_drained_after=%s", batches, busy, reclaimed, elapsed, float64(reclaimed)/(1<<20)/elapsed.Seconds(), maxBatch, time.Since(start))
			var liveRows int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM payload").Scan(&liveRows); err != nil {
				t.Fatal(err)
			}
			wantLive := 0
			if fixture.name == "partially_deleted" {
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
	latencies := make([]time.Duration, 0, count)
	for i := 0; i < count; i++ {
		arrival := start.Add(time.Duration(i+1) * cadence)
		timer := time.NewTimer(max(time.Until(arrival), 0))
		select {
		case <-ctx.Done():
			timer.Stop()
			return latencies, ctx.Err()
		case <-timer.C:
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO writes(body) VALUES ('write')"); err != nil {
			return latencies, err
		}
		latencies = append(latencies, time.Since(arrival))
	}
	return latencies, nil
}

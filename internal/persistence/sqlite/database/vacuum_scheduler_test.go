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
		_, err = db.Exec("DELETE FROM payload")
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

// Disposable throughput experiment using the production single-connection writer
// pool and actual active delays. Timing results are observations, not CI SLOs.
func TestVacuumConcurrentWriteMeasurements(t *testing.T) {
	db := vacuumFixture(t, 14000, true)
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	write := func() time.Duration {
		start := time.Now()
		if _, err := db.ExecContext(ctx, "INSERT INTO writes(body) VALUES ('write')"); err != nil {
			t.Error(err)
		}
		return time.Since(start)
	}
	baseline := make([]time.Duration, 200)
	baselineTimer := time.NewTicker(2 * time.Millisecond)
	for i := range baseline {
		<-baselineTimer.C
		baseline[i] = write()
	}
	baselineTimer.Stop()
	stop, finished := make(chan struct{}), make(chan []time.Duration, 1)
	go func() {
		var latencies []time.Duration
		timer := time.NewTicker(2 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				finished <- latencies
				return
			case <-timer.C:
				latencies = append(latencies, write())
			}
		}
	}()
	start := time.Now()
	var reclaimed int64
	var batches int
	var maxBatch time.Duration
	for i := 0; i < 12; i++ {
		r := reclaimOnceWithHook(ctx, db, nil)
		if r.err != nil {
			t.Errorf("batch: %v", r.err)
			break
		}
		if !r.progress() {
			t.Errorf("no progress: %+v", r)
			break
		}
		reclaimed += (r.before.pages - r.after.pages) * r.before.pageSize
		batches++
		maxBatch = max(maxBatch, r.duration)
		if i < 11 {
			time.Sleep(vacuumActiveInterval)
		}
	}
	elapsed := time.Since(start)
	close(stop)
	latencies := <-finished
	report := func(name string, v []time.Duration) {
		if len(v) == 0 {
			t.Errorf("no %s samples", name)
			return
		}
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
		t.Logf("%s writes n=%d p50=%s p95=%s p99=%s max=%s", name, len(v), v[len(v)/2], v[len(v)*95/100], v[len(v)*99/100], v[len(v)-1])
	}
	report("baseline", baseline)
	report("cleanup", latencies)
	t.Logf("logical reclamation: batches=%d bytes=%d elapsed=%s MiB/s=%.2f max_batch=%s", batches, reclaimed, elapsed, float64(reclaimed)/(1<<20)/elapsed.Seconds(), maxBatch)
}

package database

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAdaptiveVacuumPages(t *testing.T) {
	progress := vacuumResult{attempted: true, before: vacuumStats{pages: 10000}, after: vacuumStats{pages: 9000}}
	for _, test := range []struct {
		name     string
		pages    int
		duration time.Duration
		want     int
	}{
		{"first growth", 1, time.Millisecond, 2},
		{"at most double", 100, time.Nanosecond, 200},
		{"proportional growth", 100, 20 * time.Millisecond, 125},
		{"target", 100, 25 * time.Millisecond, 100},
		{"slightly slow", 100, 26 * time.Millisecond, 96},
		{"slow", 512, 100 * time.Millisecond, 128},
		{"minutes", 512, 4 * time.Minute, 1},
		{"one slow page", 1, 30 * time.Second, 1},
		{"cap", 400, time.Millisecond, 512},
		{"at cap", 512, time.Millisecond, 512},
		{"zero duration", 1, 0, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := progress
			r.vacuumDuration = test.duration
			if got := nextVacuumPages(test.pages, r); got != test.want {
				t.Fatalf("pages=%d, want %d", got, test.want)
			}
		})
	}
	for _, r := range []vacuumResult{{busy: true}, {attempted: true, err: errors.New("failure")}, {attempted: true}, {checkpointDuration: time.Minute}} {
		if got := nextVacuumPages(8, r); got != 8 {
			t.Fatalf("must not grow without measured progress: %d", got)
		}
	}
}

func TestVacuumCheckpointCounters(t *testing.T) {
	for _, test := range []struct {
		c        vacuumCheckpoint
		complete bool
	}{
		{vacuumCheckpoint{0, 0, 0}, true},
		{vacuumCheckpoint{0, 123, 123}, true},
		{vacuumCheckpoint{0, 123, 12}, false}, // PASSIVE may return OK with work left.
		{vacuumCheckpoint{1, 123, 123}, false},
		{vacuumCheckpoint{1, -1, -1}, false},
		{vacuumCheckpoint{0, -1, -1}, false}, // Not a WAL database.
	} {
		if got := test.c.complete(); got != test.complete {
			t.Fatalf("checkpoint %+v: complete=%t", test.c, got)
		}
	}
}

func TestVacuumSkipsIncompleteCheckpoint(t *testing.T) {
	db := vacuumFixture(t, 6000, true)
	ctx := context.Background()
	reader, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer reader.ExecContext(ctx, "ROLLBACK")
	var count int
	if err := reader.QueryRowContext(ctx, "SELECT count(*) FROM writes").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO writes(body) VALUES ('new WAL frames after reader snapshot')"); err != nil {
		t.Fatal(err)
	}
	var before, after int
	if err := db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&before); err != nil {
		t.Fatal(err)
	}
	r := reclaimBatch(ctx, db, 512, nil)
	if r.err != nil || !r.busy || r.attempted || r.stage != "checkpoint-deferred" || r.checkpoint.busy != 0 || r.checkpoint.frames <= r.checkpoint.checkpointed {
		t.Fatalf("expected incomplete checkpoint without SQL error: %+v", r)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("vacuum ran after incomplete checkpoint: %d -> %d", before, after)
	}
	var timeout int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != sqliteBusyTimeoutMS {
		t.Fatalf("timeout not restored after checkpoint deferral: %d", timeout)
	}
	var schedule vacuumSchedule
	if got := schedule.next(r, vacuumInterval, func(int64) int64 { return 0 }); got != time.Second {
		t.Fatal(got)
	}
	if _, err := reader.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	r = reclaimBatch(ctx, db, 512, nil)
	if r.err != nil || !r.progress() || !r.checkpoint.complete() {
		t.Fatalf("did not resume after reader released: %+v", r)
	}
	// This also catches drivers that execute only the first yielded vacuum row.
	if r.before.free-r.after.free != 512 {
		t.Fatalf("incomplete vacuum execution: %+v", r)
	}
	assertVacuumPoolBusyTimeout(t, db)
}

func TestVacuumReleasesWriterBetweenAdaptiveBatches(t *testing.T) {
	db := vacuumFixture(t, 6000, true)
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithCancel(context.Background())
	timer := &testVacuumTimer{make(chan time.Time), make(chan time.Duration, 1), make(chan struct{})}
	results := make(chan vacuumResult, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runVacuum(ctx, timer, vacuumInterval, func(pages int) vacuumResult {
			r := reclaimBatch(ctx, db, pages, nil)
			results <- r
			return r
		}, func(int64) int64 { return 0 })
	}()
	defer func() { cancel(); <-done }()
	expectedPages := 1
	for i := 0; i < 4; i++ {
		select {
		case timer.ticks <- time.Now():
		case <-time.After(2 * time.Second):
			t.Fatal("scheduler stalled")
		}
		var r vacuumResult
		select {
		case r = <-results:
		case <-time.After(2 * time.Second):
			t.Fatal("batch stalled")
		}
		if r.err != nil || !r.progress() || r.pages != expectedPages {
			t.Fatalf("batch %d: %+v, want pages=%d", i, r, expectedPages)
		}
		expectedPages = nextVacuumPages(expectedPages, r)
		select {
		case delay := <-timer.resets:
			if delay != vacuumActiveInterval {
				t.Fatal(delay)
			}
		case <-time.After(time.Second):
			t.Fatal("no pause")
		}
		// Scheduler is paused. A real user write must obtain the only connection
		// and write lock, with the original busy timeout already restored.
		userCtx, userCancel := context.WithTimeout(ctx, time.Second)
		conn, err := db.Conn(userCtx)
		if err != nil {
			userCancel()
			t.Fatal(err)
		}
		var timeout int
		err = conn.QueryRowContext(userCtx, "PRAGMA busy_timeout").Scan(&timeout)
		if err == nil {
			_, err = conn.ExecContext(userCtx, "INSERT INTO writes(body) VALUES ('between batches')")
		}
		conn.Close()
		userCancel()
		if err != nil || timeout != sqliteBusyTimeoutMS {
			t.Fatalf("writer not released/restored: timeout=%d error=%v", timeout, err)
		}
	}
}

package database

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Unlike testVacuumTimer, Stop may be called when a trigger replaces an idle
// tick, as well as at shutdown. Ticks are explicitly driven, not wall-clock sleeps.
type triggerTimer struct {
	ticks  chan time.Time
	resets chan time.Duration
}

func (t *triggerTimer) channel() <-chan time.Time  { return t.ticks }
func (t *triggerTimer) Stop() bool                 { return true }
func (t *triggerTimer) Reset(d time.Duration) bool { t.resets <- d; return false }

func receiveTriggerTest[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not respond")
		var zero T
		return zero
	}
}
func requestTestCleanup(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func TestVacuumTriggerBudgetAndPendingRemeasurement(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint("pending=", pending), func(t *testing.T) {
			timer := &triggerTimer{make(chan time.Time, 1), make(chan time.Duration, 1)}
			requests := make(chan struct{}, 1)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			defer func() { cancel(); receiveTriggerTest(t, done) }()
			calls := make(chan int, 1)
			total, firstRun := int64(0), true
			go func() {
				defer close(done)
				runVacuum(ctx, timer, vacuumInterval, func(pages int) vacuumResult {
					calls <- pages
					// New deletions continually increase measured free space. The original
					// 3000-page budget must still end the first run, even while eligible.
					before := vacuumStats{pages: 20000, free: 7000, pageSize: 4096}
					if total == 0 {
						before.free = 3000
					}
					if firstRun && total+int64(pages) > 3000 {
						t.Errorf("expanded run budget: %d + %d", total, pages)
					}
					total += int64(pages)
					if firstRun && total == 3000 {
						firstRun = false
					}
					if total > 3000 {
						if pages != 1 {
							t.Errorf("new run did not restart at one page: %d", pages)
						}
						// Fresh snapshot is ineligible: this run must stop without vacuuming.
						return vacuumResult{beforeKnown: true, before: vacuumStats{pages: 20000, free: 0}}
					}
					if pending {
						for i := 0; i < 100; i++ {
							requestTestCleanup(requests)
						}
					}
					return vacuumResult{beforeKnown: true, afterKnown: true, attempted: true, before: before,
						after: vacuumStats{pages: before.pages - int64(pages), free: before.free - int64(pages), pageSize: 4096}, vacuumDuration: time.Millisecond}
				}, func(int64) int64 { return 0 }, requests)
			}()
			requestTestCleanup(requests)
			sum := 0
			for sum < 3000 {
				n := receiveTriggerTest(t, calls)
				sum += n
				d := receiveTriggerTest(t, timer.resets)
				if sum < 3000 {
					if d != 0 {
						t.Fatalf("active delay %s", d)
					}
					timer.ticks <- time.Now()
				} else if d != vacuumInterval {
					t.Fatalf("budget completion delay %s", d)
				}
			}
			if !pending {
				select {
				case <-calls:
					t.Fatal("budget expanded without a request")
				default:
				}
				timer.ticks <- time.Now() // Five-minute fallback starts a fresh run.
			}
			// A pending request runs without firing another timer, but must remeasure
			// eligibility rather than using the previous run's still-eligible after stats.
			if n := receiveTriggerTest(t, calls); n != 1 {
				t.Fatal(n)
			}
			if d := receiveTriggerTest(t, timer.resets); d != vacuumInterval {
				t.Fatal(d)
			}
			select {
			case n := <-calls:
				t.Fatalf("unrequested extra run %d", n)
			default:
			}
		})
	}
}

func TestVacuumTriggersPreserveBackoffAndShutdown(t *testing.T) {
	timer := &triggerTimer{make(chan time.Time, 1), make(chan time.Duration, 1)}
	requests := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	calls := make(chan int, 1)
	go func() {
		defer close(done)
		runVacuum(ctx, timer, vacuumInterval, func(pages int) vacuumResult {
			calls <- pages
			return vacuumResult{busy: true}
		}, func(int64) int64 { return 0 }, requests)
	}()
	defer func() { cancel(); receiveTriggerTest(t, done) }()
	requestTestCleanup(requests)
	for _, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		receiveTriggerTest(t, calls)
		if d := receiveTriggerTest(t, timer.resets); d != want {
			t.Fatalf("backoff %s want %s", d, want)
		}
		for i := 0; i < 1000; i++ {
			requestTestCleanup(requests)
		}
		select {
		case <-calls:
			t.Fatal("trigger bypassed backoff")
		case <-time.After(10 * time.Millisecond):
		}
		if want < 4*time.Second {
			timer.ticks <- time.Now()
		}
	}
	// Cancel with both a backoff timer and pending deletion request.
}

func TestVacuumSharedWorkerTimerAndTrigger(t *testing.T) {
	db := vacuumFixture(t, 6000, true)
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	done := startIncrementalVacuum(ctx, db, time.Millisecond, func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	defer func() { cancel(); receiveTriggerTest(t, done) }()
	receiveTriggerTest(t, entered) // Periodic wake owns the running batch.
	duplicate := startIncrementalVacuum(ctx, db, time.Nanosecond, func() { t.Error("duplicate worker") })
	if duplicate != done {
		t.Fatal("start created another worker")
	}
	for i := 0; i < 10000; i++ {
		RequestIncrementalVacuum(db)
	} // Nonblocking even in batch.
	vacuumWorkers.Lock()
	pending := len(vacuumWorkers.workers[db].requests)
	vacuumWorkers.Unlock()
	if pending != 1 {
		t.Fatalf("pending=%d", pending)
	}
	cancel() // Shutdown cancels the running batch even with pending requests.
	receiveTriggerTest(t, done)
	vacuumWorkers.Lock()
	_, registered := vacuumWorkers.workers[db]
	vacuumWorkers.Unlock()
	if registered {
		t.Fatal("worker still registered after shutdown")
	}
	RequestIncrementalVacuum(db) // Safe after shutdown.
	assertVacuumPoolBusyTimeout(t, db)
}

func TestVacuumDeletionTriggersWithRealContention(t *testing.T) {
	for _, lock := range []string{"pool", "writer"} {
		t.Run(lock, func(t *testing.T) {
			db := vacuumFixture(t, 6000, true)
			if lock == "pool" {
				db.SetMaxOpenConns(1)
			}
			ctx, cancel := context.WithCancel(context.Background())
			conn, err := db.Conn(ctx)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			defer conn.Close()
			if lock == "writer" {
				if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
					cancel()
					t.Fatal(err)
				}
				defer conn.ExecContext(context.Background(), "ROLLBACK")
			}
			timer := &triggerTimer{make(chan time.Time, 1), make(chan time.Duration, 1)}
			requests := make(chan struct{}, 1)
			results := make(chan vacuumResult, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				runVacuum(ctx, timer, vacuumInterval, func(pages int) vacuumResult {
					r := reclaimBatch(ctx, db, pages, nil)
					results <- r
					return r
				}, func(int64) int64 { return 0 }, requests)
			}()
			defer func() { cancel(); receiveTriggerTest(t, done) }()
			requestTestCleanup(requests)
			for _, delay := range []time.Duration{time.Second, 2 * time.Second} {
				r := receiveTriggerTest(t, results)
				if !r.busy {
					t.Fatalf("expected contention: %+v", r)
				}
				if d := receiveTriggerTest(t, timer.resets); d != delay {
					t.Fatalf("delay=%s", d)
				}
				for i := 0; i < 100; i++ {
					requestTestCleanup(requests)
				}
				select {
				case <-results:
					t.Fatal("deletion bypassed real contention backoff")
				case <-time.After(10 * time.Millisecond):
				}
				if delay == time.Second {
					timer.ticks <- time.Now()
				}
			}
			if lock == "writer" {
				if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
					t.Fatal(err)
				}
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			timer.ticks <- time.Now()
			if r := receiveTriggerTest(t, results); !r.progress() {
				t.Fatalf("no recovery: %+v", r)
			}
			if d := receiveTriggerTest(t, timer.resets); d != 0 {
				t.Fatal(d)
			}
			var timeout int
			if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil || timeout != sqliteBusyTimeoutMS {
				t.Fatalf("restored timeout=%d err=%v", timeout, err)
			}
		})
	}
}

func TestVacuumPendingRequestCannotBypassFailureCooldown(t *testing.T) {
	for _, result := range []vacuumResult{
		{stage: "no-progress", attempted: true},
		{stage: "error", err: fmt.Errorf("injected I/O failure")},
	} {
		t.Run(result.stage, func(t *testing.T) {
			timer := &triggerTimer{make(chan time.Time, 1), make(chan time.Duration, 1)}
			requests := make(chan struct{}, 1)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				runVacuum(ctx, timer, vacuumInterval, func(int) vacuumResult {
					requestTestCleanup(requests) // Deletion arrived while this batch ran.
					return result
				}, func(int64) int64 { return 0 }, requests)
			}()
			defer func() { cancel(); receiveTriggerTest(t, done) }()
			requestTestCleanup(requests)
			if d := receiveTriggerTest(t, timer.resets); d != vacuumInterval {
				t.Fatal(d)
			}
			select {
			case <-timer.resets:
				t.Fatal("pending request bypassed idle cooldown")
			case <-time.After(10 * time.Millisecond):
			}
			timer.ticks <- time.Now()
			if d := receiveTriggerTest(t, timer.resets); d != vacuumInterval {
				t.Fatal(d)
			}
		})
	}
}

func TestVacuumDeletionRequestReclaimsWithoutFallbackTick(t *testing.T) {
	db := vacuumFixture(t, 6000, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var before int64
	if err := db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&before); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := startIncrementalVacuum(ctx, db, time.Hour, nil)
	defer func() { cancel(); receiveTriggerTest(t, done) }()
	RequestIncrementalVacuum(db)
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		var after int64
		if err := db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after < before {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("trigger did not reclaim pages before fallback")
		case <-poll.C:
		}
	}
	cancel()
	receiveTriggerTest(t, done)
	assertVacuumPoolBusyTimeout(t, db)
}

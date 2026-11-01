package database

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Signals when admission actually enters its context-aware wait, so cancellation
// coverage cannot pass solely because the context was canceled before startup.
type observedAdmissionContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *observedAdmissionContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func waitForAdmittedUsers(ctx context.Context, g *writerAdmission, count int) error {
	for {
		g.mu.Lock()
		users, changed := g.users, g.changed
		g.mu.Unlock()
		if users == count {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// The real scheduler runs continuously with zero active delay. On each batch,
// application requests arrive WHILE maintenance owns the writer. Every such
// request must complete before the next batch, not merely eventually. We only
// synchronize arrivals inside the batch; there is no artificial scheduler pause.
func TestContinuousVacuumYieldsToQueuedWrites(t *testing.T) {
	db := vacuumFixture(t, 14000, true)
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const writesPerBatch = 8
	const batchCount = 12
	var completed atomic.Int32
	var workers sync.WaitGroup
	batches := 0
	runVacuum(ctx, realVacuumTimer{time.NewTimer(0)}, vacuumInterval, func(pages int) vacuumResult {
		r := reclaimBatch(ctx, db, pages, func() {
			if got := int(completed.Load()); got != batches*writesPerBatch {
				t.Errorf("cleanup jumped ahead of queued writes: completed=%d want=%d", got, batches*writesPerBatch)
				cancel()
			}
			for i := 0; i < writesPerBatch; i++ {
				workers.Add(1)
				go func() {
					defer workers.Done()
					conn, release, err := AcquireWriteConn(ctx, db)
					if err != nil {
						t.Error(err)
						return
					}
					defer release()
					var timeout int
					if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
						t.Error(err)
						return
					}
					if timeout != sqliteBusyTimeoutMS {
						t.Errorf("maintenance timeout leaked: %d", timeout)
					}
					if _, err := conn.ExecContext(ctx, "INSERT INTO writes(body) VALUES ('queued during vacuum')"); err != nil {
						t.Error(err)
						return
					}
					completed.Add(1)
				}()
			}
			if err := waitForAdmittedUsers(ctx, admissionFor(db), writesPerBatch); err != nil {
				t.Error(err)
			}
		})
		if r.err != nil || !r.progress() {
			t.Errorf("vacuum failed: %+v", r)
			cancel()
			return r
		}
		batches++
		if batches == batchCount {
			// Stop further batches only AFTER the final queued requests complete.
			// The current batch has already restored/closed its connection.
			workers.Wait()
			cancel()
		}
		return r
	}, func(int64) int64 { return 0 })
	workers.Wait()
	if batches != batchCount || completed.Load() != batchCount*writesPerBatch {
		t.Fatalf("batches=%d writes=%d", batches, completed.Load())
	}
}

func TestWriterAdmissionCancellation(t *testing.T) {
	db := vacuumFixture(t, 6000, true)
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	g := admissionFor(db)
	releaseMaintenance, err := g.acquire(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseMaintenance()
	userCtx, cancelUser := context.WithCancel(ctx)
	userDone := make(chan error, 1)
	go func() {
		_, release, err := AcquireWriteConn(userCtx, db)
		if release != nil {
			release()
		}
		userDone <- err
	}()
	waitCtx, cancelWait := context.WithTimeout(ctx, time.Second)
	defer cancelWait()
	if err := waitForAdmittedUsers(waitCtx, g, 1); err != nil {
		t.Fatal(err)
	}
	cancelUser()
	select {
	case err := <-userDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-waitCtx.Done():
		t.Fatal("user admission did not cancel")
	}
	if err := waitForAdmittedUsers(waitCtx, g, 0); err != nil {
		t.Fatal(err)
	}
	releaseMaintenance()

	// A user holding the only connection prevents maintenance admission. Cancel
	// while cleanup yields; no pool checkout or setting change may have occurred.
	conn, releaseUser, err := AcquireWriteConn(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseUser()
	maintCtx, cancelMaint := context.WithCancel(ctx)
	done := make(chan vacuumResult, 1)
	observed := &observedAdmissionContext{Context: maintCtx, waiting: make(chan struct{})}
	go func() { done <- reclaimBatch(observed, db, 1, nil) }()
	select {
	case <-observed.waiting:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not enter yield wait")
	}
	cancelMaint()
	select {
	case r := <-done:
		if !errors.Is(r.err, context.Canceled) || r.stage != "yield" {
			t.Fatalf("%+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("maintenance yield did not cancel")
	}
	var timeout int
	if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != sqliteBusyTimeoutMS {
		t.Fatal(timeout)
	}
	releaseUser()
	r := reclaimBatch(ctx, db, 1, nil)
	if r.err != nil || !r.progress() {
		t.Fatalf("admission leaked after cancellation: %+v", r)
	}
}

func TestWriterAdmissionCoversPoolWaitAndCheckoutFailure(t *testing.T) {
	db := vacuumFixture(t, 6000, true)
	db.SetMaxOpenConns(1)
	raw, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	userCtx, cancelUser := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, release, err := AcquireWriteConn(userCtx, db)
		if release != nil {
			release()
		}
		done <- err
	}()
	if err := waitForAdmittedUsers(ctx, admissionFor(db), 1); err != nil {
		t.Fatal(err)
	}
	cancelUser()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := waitForAdmittedUsers(ctx, admissionFor(db), 0); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	r := reclaimBatch(ctx, db, 1, nil)
	if r.err != nil || !r.progress() {
		t.Fatalf("failed checkout leaked priority: %+v", r)
	}
}

func TestWriterPriorityRegistrationLifetime(t *testing.T) {
	db := new(sql.DB) // Registry only; no connection or live database.
	unregister1 := RegisterWriterPriority(db)
	unregister2 := RegisterWriterPriority(db)
	g := admissionFor(db)
	unregister1()
	unregister1()
	if admissionFor(db) != g {
		t.Fatal("first owner removed shared admission")
	}
	unregister2()
	if admissionFor(db) != nil {
		t.Fatal("registration leaked")
	}
}

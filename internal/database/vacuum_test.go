package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestReclaimOnceSkipsLowFreelistAndReclaimsHighFreelist(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "vacuum.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reclaimOnce(ctx, db)

	if _, err := db.ExecContext(ctx, `CREATE TABLE vacuum_payload(id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatalf("create payload table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x < 4000)
		INSERT INTO vacuum_payload(body) SELECT printf('%.*c', 4096, 'x') FROM n`); err != nil {
		t.Fatalf("insert payload: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM vacuum_payload`); err != nil {
		t.Fatalf("delete payload: %v", err)
	}

	var freeBefore int
	if err := db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&freeBefore); err != nil {
		t.Fatalf("freelist before: %v", err)
	}
	if freeBefore < vacuumMinPages {
		t.Skipf("fixture produced only %d free pages; need at least %d to exercise reclaim path", freeBefore, vacuumMinPages)
	}
	reader, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire WAL reader: %v", err)
	}
	if _, err := reader.ExecContext(ctx, `BEGIN`); err != nil {
		_ = reader.Close()
		t.Fatalf("begin WAL reader: %v", err)
	}
	var heldCount int
	if err := reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM vacuum_payload`).Scan(&heldCount); err != nil {
		t.Fatalf("read held snapshot: %v", err)
	}

	start := make(chan struct{})
	vacuumDone := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		<-start
		reclaimOnce(ctx, db)
		close(vacuumDone)
	}()
	go func() {
		<-start
		_, err := db.ExecContext(ctx, `INSERT INTO vacuum_payload(body) VALUES ('concurrent writer')`)
		writerDone <- err
	}()
	close(start)
	<-vacuumDone
	if err := <-writerDone; err != nil {
		t.Fatalf("writer concurrent with incremental vacuum: %v", err)
	}

	var freeAfter int
	if err := db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&freeAfter); err != nil {
		t.Fatalf("freelist after: %v", err)
	}
	if freeAfter >= freeBefore {
		t.Fatalf("incremental vacuum did not reclaim pages: before=%d after=%d", freeBefore, freeAfter)
	}
	if _, err := reader.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	assertVacuumPoolBusyTimeout(t, db)
}

func TestStartIncrementalVacuumExitsWhenContextCancelled(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "vacuum-cancel.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := startIncrementalVacuum(ctx, db, time.Millisecond, nil)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("vacuum goroutine did not stop after cancellation")
	}
}

func TestIncrementalVacuumHeldWriterHonorsContextAndRestoresTimeout(t *testing.T) {
	for _, test := range []struct {
		name        string
		newContext  func() (context.Context, context.CancelFunc)
		cancelEarly bool
	}{
		{
			name: "cancellation only",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			cancelEarly: true,
		},
		{
			name: "deadline",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 150*time.Millisecond)
			},
		},
		{
			name: "early cancellation with deadline",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 10*time.Second)
			},
			cancelEarly: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			testIncrementalVacuumHeldWriterContext(t, test.newContext, test.cancelEarly)
		})
	}
}

func testIncrementalVacuumHeldWriterContext(t *testing.T, newContext func() (context.Context, context.CancelFunc), cancelEarly bool) {
	t.Helper()
	db, err := New(filepath.Join(t.TempDir(), "vacuum-held-writer.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE vacuum_cancel_payload(id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x < 4000)
		INSERT INTO vacuum_cancel_payload(body) SELECT printf('%.*c', 4096, 'x') FROM n`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM vacuum_cancel_payload`); err != nil {
		t.Fatal(err)
	}

	locker, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := locker.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		locker.Close()
		t.Fatal(err)
	}

	vacuumCtx, cancel := newContext()
	defer cancel()
	enteredWrite := make(chan struct{})
	var enteredOnce sync.Once
	done := startIncrementalVacuum(vacuumCtx, db, time.Millisecond, func() {
		enteredOnce.Do(func() { close(enteredWrite) })
	})
	select {
	case <-enteredWrite:
	case <-time.After(time.Second):
		cancel()
		_, _ = locker.ExecContext(ctx, `ROLLBACK`)
		_ = locker.Close()
		t.Fatal("vacuum did not reach the held writer lock wait")
	}

	var contextEndedAt time.Time
	if cancelEarly {
		contextEndedAt = time.Now()
		cancel()
	} else {
		<-vacuumCtx.Done()
		contextEndedAt = time.Now()
	}
	select {
	case <-done:
		if elapsed := time.Since(contextEndedAt); elapsed > 500*time.Millisecond {
			t.Fatalf("vacuum cancellation took %s", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("vacuum remained blocked in SQLite busy handler after cancellation")
	}
	if _, err := locker.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if err := locker.Close(); err != nil {
		t.Fatal(err)
	}

	assertVacuumPoolBusyTimeout(t, db)
}

func assertVacuumPoolBusyTimeout(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	connections := make([]*sql.Conn, 0, 2)
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	for i := 0; i < 2; i++ {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
	}
	for i, conn := range connections {
		var timeout int
		if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&timeout); err != nil {
			t.Fatal(err)
		}
		if timeout != sqliteBusyTimeoutMS {
			t.Fatalf("connection %d busy_timeout = %d, want %d", i, timeout, sqliteBusyTimeoutMS)
		}
	}
}

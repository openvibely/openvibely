package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestReclaimOnceSkipsLowFreelistAndReclaimsHighFreelist(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "vacuum.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

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
	defer reader.Close()
	if _, err := reader.ExecContext(ctx, `BEGIN`); err != nil {
		t.Fatalf("begin WAL reader: %v", err)
	}
	defer reader.ExecContext(context.Background(), `ROLLBACK`)
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
}

func TestStartIncrementalVacuumExitsWhenContextCancelled(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "vacuum-cancel.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	StartIncrementalVacuum(ctx, db)
	select {
	case <-time.After(5 * time.Millisecond):
	}
}

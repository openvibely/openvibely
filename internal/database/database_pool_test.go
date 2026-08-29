package database

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNew_FileBackedPoolAndPerConnectionSettings(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "pool.db") + "?cache=private")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	stats := db.Stats()
	if stats.MaxOpenConnections != 2 {
		t.Fatalf("MaxOpenConnections = %d, want 2", stats.MaxOpenConnections)
	}

	ctx := context.Background()
	conns := make([]*sql.Conn, 0, 2)
	for i := 0; i < 2; i++ {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("acquire connection %d: %v", i, err)
		}
		conns = append(conns, conn)
	}
	defer func() {
		for _, conn := range conns {
			_ = conn.Close()
		}
	}()

	for i, conn := range conns {
		var foreignKeys, busyTimeout int
		var journalMode string
		if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatalf("connection %d foreign_keys: %v", i, err)
		}
		if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
			t.Fatalf("connection %d busy_timeout: %v", i, err)
		}
		if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
			t.Fatalf("connection %d journal_mode: %v", i, err)
		}
		if foreignKeys != 1 || busyTimeout != 5000 || journalMode != "wal" {
			t.Fatalf("connection %d settings: foreign_keys=%d busy_timeout=%d journal_mode=%q", i, foreignKeys, busyTimeout, journalMode)
		}

		var parsed time.Time
		if err := conn.QueryRowContext(ctx, `SELECT created_at FROM projects ORDER BY created_at LIMIT 1`).Scan(&parsed); err != nil {
			t.Fatalf("connection %d UTC parse: %v", i, err)
		}
		if parsed.Location() != time.UTC {
			t.Fatalf("connection %d datetime location = %v, want UTC", i, parsed.Location())
		}
	}

	for _, conn := range conns {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	conns = nil
	if got := db.Stats().Idle; got != 2 {
		t.Fatalf("idle connections = %d, want 2", got)
	}
}

func TestNew_InMemoryPoolRemainsIsolated(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
}

func TestNew_FileBackedWALReaderDoesNotBlockWriter(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "wal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE pool_counter (id INTEGER PRIMARY KEY, value INTEGER NOT NULL); INSERT INTO pool_counter VALUES (1, 0)`); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	reader, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.ExecContext(ctx, `BEGIN`); err != nil {
		t.Fatal(err)
	}
	defer reader.ExecContext(context.Background(), `ROLLBACK`)
	var value int
	if err := reader.QueryRowContext(ctx, `SELECT value FROM pool_counter WHERE id=1`).Scan(&value); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE pool_counter SET value=value+1 WHERE id=1`); err != nil {
		t.Fatalf("writer blocked by WAL reader: %v", err)
	}
	if err := reader.QueryRowContext(ctx, `SELECT value FROM pool_counter WHERE id=1`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != 0 {
		t.Fatalf("held reader snapshot = %d, want 0", value)
	}
	if _, err := reader.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT value FROM pool_counter WHERE id=1`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != 1 {
		t.Fatalf("committed value = %d, want 1", value)
	}
}

func TestNew_ConcurrentWritersPreserveAtomicUpdates(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "writers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE pool_counter (id INTEGER PRIMARY KEY, value INTEGER NOT NULL); INSERT INTO pool_counter VALUES (1, 0)`); err != nil {
		t.Fatal(err)
	}

	const writers = 4
	const updates = 25
	start := make(chan struct{})
	errCh := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < updates; j++ {
				if _, err := db.Exec(`UPDATE pool_counter SET value=value+1 WHERE id=1`); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	var got int
	if err := db.QueryRow(`SELECT value FROM pool_counter WHERE id=1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != writers*updates {
		t.Fatalf("counter = %d, want %d", got, writers*updates)
	}
}

func TestNew_WriteLockUsesConfiguredBusyTimeout(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "deadline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE pool_counter (id INTEGER PRIMARY KEY, value INTEGER NOT NULL); INSERT INTO pool_counter VALUES (1, 0)`); err != nil {
		t.Fatal(err)
	}

	locker, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	if _, err := locker.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer locker.ExecContext(context.Background(), `ROLLBACK`)

	started := time.Now()
	_, err = db.Exec(`UPDATE pool_counter SET value=value+1 WHERE id=1`)
	if err == nil {
		t.Fatal("write unexpectedly succeeded while writer lock was held")
	}
	if elapsed := time.Since(started); elapsed < 4*time.Second || elapsed > 7*time.Second {
		t.Fatalf("write lock wait = %s, want the configured five-second timeout", elapsed)
	}
}

func TestNew_StartupStaysSerializedAndFailureClosesDatabase(t *testing.T) {
	sentinel := errors.New("injected startup failure")
	var opened *sql.DB
	db, err := newSQLiteDatabase(filepath.Join(t.TempDir(), "startup.db"), func(candidate *sql.DB) error {
		opened = candidate
		stats := candidate.Stats()
		if stats.MaxOpenConnections != 1 {
			t.Fatalf("startup MaxOpenConnections = %d, want 1", stats.MaxOpenConnections)
		}
		conn, err := candidate.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if candidate.Stats().OpenConnections != 1 {
			t.Fatalf("startup opened %d physical connections, want 1", candidate.Stats().OpenConnections)
		}
		return sentinel
	})
	if db != nil || !errors.Is(err, sentinel) {
		t.Fatalf("newSQLiteDatabase = %#v, %v; want nil, sentinel", db, err)
	}
	if opened == nil {
		t.Fatal("initializer did not receive database")
	}
	if err := opened.Ping(); err == nil || !strings.Contains(err.Error(), "database is closed") {
		t.Fatalf("Ping after startup failure = %v, want closed database error", err)
	}
}

func TestNew_MultipleReadersUseDistinctPhysicalConnections(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "readers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	conns := make([]*sql.Conn, 2)
	for i := range conns {
		conns[i], err = db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conns[i].Close()
	}
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	errs := make(chan error, 2)
	for _, conn := range conns {
		go func(conn *sql.Conn) {
			<-start
			if _, err := conn.ExecContext(context.Background(), `BEGIN`); err != nil {
				ready <- struct{}{}
				errs <- err
				return
			}
			var count int
			err := conn.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM tasks`).Scan(&count)
			ready <- struct{}{}
			<-release
			if _, rollbackErr := conn.ExecContext(context.Background(), `ROLLBACK`); err == nil {
				err = rollbackErr
			}
			errs <- err
		}(conn)
	}
	close(start)
	<-ready
	<-ready
	if db.Stats().InUse != 2 {
		t.Fatalf("simultaneous readers in use = %d, want 2", db.Stats().InUse)
	}
	close(release)
	for range conns {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestBindSQLiteBusyTimeoutToContextRestoresOnce(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "shared-busy-timeout.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	restore, err := BindSQLiteBusyTimeoutToContext(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	var bounded int
	if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&bounded); err != nil {
		t.Fatal(err)
	}
	if bounded != 50 {
		t.Fatalf("bounded busy_timeout = %d, want 50", bounded)
	}

	restore()
	restore()
	var restored int
	if err := conn.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if restored != sqliteBusyTimeoutMS {
		t.Fatalf("restored busy_timeout = %d, want %d", restored, sqliteBusyTimeoutMS)
	}
}

func TestNewReadWrite_DedicatesWriterAndQueryOnlyReaders(t *testing.T) {
	connections, err := NewReadWrite(filepath.Join(t.TempDir(), "split.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connections.Close()

	if got := connections.Writer.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("writer MaxOpenConnections = %d, want 1", got)
	}
	if got := connections.Reader.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("reader MaxOpenConnections = %d, want 1", got)
	}

	ctx := context.Background()
	readers := make([]*sql.Conn, 0, connections.Reader.Stats().MaxOpenConnections)
	for i := 0; i < connections.Reader.Stats().MaxOpenConnections; i++ {
		conn, err := connections.Reader.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		readers = append(readers, conn)
	}
	defer func() {
		for _, conn := range readers {
			_ = conn.Close()
		}
	}()
	for i, conn := range readers {
		var queryOnly, foreignKeys, busyTimeout int
		if err := conn.QueryRowContext(ctx, `PRAGMA query_only`).Scan(&queryOnly); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
			t.Fatal(err)
		}
		if queryOnly != 1 || foreignKeys != 1 || busyTimeout != 5000 {
			t.Fatalf("reader %d settings: query_only=%d foreign_keys=%d busy_timeout=%d", i, queryOnly, foreignKeys, busyTimeout)
		}
	}
	for _, conn := range readers {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	readers = nil

	if _, err := connections.Reader.ExecContext(ctx, `UPDATE projects SET name='blocked' WHERE id='default'`); err == nil {
		t.Fatal("query-only reader unexpectedly accepted a write")
	}
	if _, err := connections.Writer.ExecContext(ctx, `UPDATE projects SET name='writer' WHERE id='default'`); err != nil {
		t.Fatalf("dedicated writer update: %v", err)
	}
	var name string
	if err := connections.Reader.QueryRowContext(ctx, `SELECT name FROM projects WHERE id='default'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "writer" {
		t.Fatalf("reader observed project name %q, want writer", name)
	}
}

func TestConfigureSQLiteDSNPreservesCallerParameters(t *testing.T) {
	configured, inMemory, err := configureSQLiteDSN("file:test.db?cache=private&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(1)&_loc=Local")
	if err != nil {
		t.Fatal(err)
	}
	if inMemory {
		t.Fatal("file-backed DSN classified as in-memory")
	}
	_, rawQuery, found := strings.Cut(configured, "?")
	if !found {
		t.Fatalf("configured DSN has no query: %q", configured)
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("cache") != "private" || values.Get("_loc") != "UTC" {
		t.Fatalf("configured query = %#v", values)
	}
	joined := strings.Join(values["_pragma"], "|")
	for _, want := range []string{"synchronous(NORMAL)", "foreign_keys(1)", "busy_timeout(5000)"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("PRAGMAs %q do not contain %q", joined, want)
		}
	}
	if strings.Contains(joined, "busy_timeout(1)") {
		t.Fatalf("caller busy timeout was not replaced: %q", joined)
	}
}

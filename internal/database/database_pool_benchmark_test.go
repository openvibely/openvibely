package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkSQLiteConnectionPool compares disposable WAL databases only. Run with:
//
//	go test ./internal/database -run '^$' -bench '^BenchmarkSQLiteConnectionPool$' -benchtime=1000x -count=3
//
// The mixed case models status/history reads, atomic task claims, and execution
// output persistence. The held WAL reader makes WAL growth visible under writes.
func BenchmarkSQLiteConnectionPool(b *testing.B) {
	for _, poolSize := range []int{1, 2, 4, 8} {
		for _, active := range []int{1, 4, 10} {
			for _, workload := range []string{"read", "write", "mixed"} {
				name := fmt.Sprintf("pool=%d/active=%d/%s", poolSize, active, workload)
				b.Run(name, func(b *testing.B) {
					benchmarkSQLitePoolWorkload(b, poolSize, active, workload)
				})
			}
		}
	}
}

func benchmarkSQLitePoolWorkload(b *testing.B, poolSize, active int, workload string) {
	b.StopTimer()
	dbPath := filepath.Join(b.TempDir(), "pool-benchmark.db")
	db, err := New(dbPath)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(poolSize)
	db.SetMaxIdleConns(poolSize)
	seedSQLitePoolBenchmark(b, db)

	// A sustained independent reader models execution-history/SSE consumers and
	// makes WAL growth visible without consuming a slot from the measured pool.
	readerDB, err := sql.Open("sqlite", mustConfigureBenchmarkDSN(b, dbPath))
	if err != nil {
		b.Fatal(err)
	}
	readerDB.SetMaxOpenConns(1)
	readerDB.SetMaxIdleConns(1)
	defer readerDB.Close()
	reader, err := readerDB.Conn(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.ExecContext(context.Background(), `BEGIN`); err != nil {
		b.Fatal(err)
	}
	defer reader.ExecContext(context.Background(), `ROLLBACK`)
	var count int
	if err := reader.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM executions`).Scan(&count); err != nil {
		b.Fatal(err)
	}

	// Exclude lazy physical-connection creation from steady-state measurements.
	warm := make([]*sql.Conn, 0, poolSize)
	for i := 0; i < poolSize; i++ {
		conn, err := db.Conn(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		warm = append(warm, conn)
	}
	for _, conn := range warm {
		if err := conn.Close(); err != nil {
			b.Fatal(err)
		}
	}

	before := db.Stats()
	latencies := make([]time.Duration, b.N)
	var next atomic.Int64
	var busy, busySnapshot atomic.Int64
	payload := strings.Repeat("stream-output-", 256)
	started := time.Now()
	b.StartTimer()
	var wg sync.WaitGroup
	for worker := 0; worker < active; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= b.N {
					return
				}
				opStarted := time.Now()
				err := runSQLitePoolBenchmarkOperation(db, workload, i, payload)
				latencies[i] = time.Since(opStarted)
				if err != nil {
					message := strings.ToUpper(err.Error())
					if strings.Contains(message, "BUSY_SNAPSHOT") || strings.Contains(message, "SQLITE_BUSY_SNAPSHOT") {
						busySnapshot.Add(1)
					} else if strings.Contains(message, "BUSY") || strings.Contains(message, "LOCKED") {
						busy.Add(1)
					} else {
						b.Errorf("operation %d: %v", i, err)
					}
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(started)
	b.StopTimer()

	after := db.Stats()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	if len(latencies) > 0 {
		b.ReportMetric(float64(latencies[len(latencies)/2].Microseconds()), "p50-us")
		b.ReportMetric(float64(latencies[(len(latencies)-1)*95/100].Microseconds()), "p95-us")
	}
	b.ReportMetric(float64(b.N)/elapsed.Seconds(), "ops/s")
	b.ReportMetric(float64(after.WaitCount-before.WaitCount), "wait-count")
	b.ReportMetric(float64((after.WaitDuration - before.WaitDuration).Microseconds()), "wait-us")
	b.ReportMetric(float64(busy.Load()), "sqlite-busy")
	b.ReportMetric(float64(busySnapshot.Load()), "busy-snapshot")
	if info, err := os.Stat(dbPath + "-wal"); err == nil {
		b.ReportMetric(float64(info.Size()), "wal-bytes")
	}
}

func mustConfigureBenchmarkDSN(b *testing.B, dsn string) string {
	b.Helper()
	configured, _, err := configureSQLiteDSN(dsn)
	if err != nil {
		b.Fatal(err)
	}
	return configured
}

func seedSQLitePoolBenchmark(b *testing.B, db *sql.DB) {
	b.Helper()
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO projects(id, name) VALUES ('pool-bench', 'Pool benchmark')`); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 256; i++ {
		taskID := fmt.Sprintf("pool-task-%03d", i)
		execID := fmt.Sprintf("pool-exec-%03d", i)
		if _, err := tx.Exec(`INSERT INTO tasks(id, project_id, title, category, status) VALUES (?, 'pool-bench', ?, 'active', 'pending')`, taskID, "Pool task "+taskID); err != nil {
			b.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO executions(id, task_id, status, prompt_sent, output) VALUES (?, ?, 'running', 'benchmark prompt', 'initial output')`, execID, taskID); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

func runSQLitePoolBenchmarkOperation(db *sql.DB, workload string, i int, payload string) error {
	id := i % 256
	switch workload {
	case "read":
		var status, title, output string
		if err := db.QueryRow(`SELECT status, title FROM tasks WHERE id = ?`, fmt.Sprintf("pool-task-%03d", id)).Scan(&status, &title); err != nil {
			return err
		}
		return db.QueryRow(`SELECT output FROM executions WHERE task_id = ? ORDER BY started_at DESC LIMIT 1`, fmt.Sprintf("pool-task-%03d", id)).Scan(&output)
	case "write":
		_, err := db.Exec(`UPDATE executions SET output = ? WHERE id = ? AND status = 'running'`, payload, fmt.Sprintf("pool-exec-%03d", id))
		return err
	case "mixed":
		if i%5 != 0 {
			return runSQLitePoolBenchmarkOperation(db, "read", i, payload)
		}
		// Atomic conditional claim and reset represent scheduler/Automation claims
		// without introducing a deferred read-before-write snapshot.
		result, err := db.Exec(`UPDATE tasks SET status='running' WHERE id=? AND status='pending'`, fmt.Sprintf("pool-task-%03d", id))
		if err != nil {
			return err
		}
		claimed, _ := result.RowsAffected()
		if claimed == 1 {
			if _, err := db.Exec(`UPDATE executions SET output=? WHERE id=? AND status='running'`, payload, fmt.Sprintf("pool-exec-%03d", id)); err != nil {
				return err
			}
			_, err = db.Exec(`UPDATE tasks SET status='pending' WHERE id=? AND status='running'`, fmt.Sprintf("pool-task-%03d", id))
		}
		return err
	default:
		return fmt.Errorf("unknown workload %q", workload)
	}
}

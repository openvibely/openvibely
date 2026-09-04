package database_test

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

	"github.com/openvibely/openvibely/internal/database"
	"github.com/openvibely/openvibely/internal/repository"
)

// BenchmarkSQLiteConnectionPool compares disposable WAL databases only. Run with:
//
//	go test ./internal/database -run '^$' -bench '^BenchmarkSQLiteConnectionPool$' -benchtime=2s -count=3
//
// The mixed case models status/history reads, atomic task claims, and execution
// output persistence. The held WAL reader makes WAL growth visible under writes.
func BenchmarkSQLiteConnectionPool(b *testing.B) {
	for _, poolSize := range []int{1, 2, 4, 8} {
		for _, active := range []int{1, 4, 10} {
			for _, workload := range []string{"read", "write", "mixed"} {
				name := fmt.Sprintf("shared=%d/active=%d/%s", poolSize, active, workload)
				b.Run(name, func(b *testing.B) {
					benchmarkSQLitePoolWorkload(b, poolSize, active, workload)
				})
			}
		}
	}
	for _, readers := range []int{1, 2, 4} {
		for _, active := range []int{1, 4, 10} {
			for _, workload := range []string{"read", "write", "mixed"} {
				name := fmt.Sprintf("dedicated=1W+%dR/active=%d/%s", readers, active, workload)
				b.Run(name, func(b *testing.B) {
					benchmarkSQLiteDedicatedWorkload(b, readers, active, workload)
				})
			}
		}
	}
}

func benchmarkSQLitePoolWorkload(b *testing.B, poolSize, active int, workload string) {
	b.StopTimer()
	dbPath := filepath.Join(b.TempDir(), "pool-benchmark.db")
	db, err := database.New(dbPath)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(poolSize)
	db.SetMaxIdleConns(poolSize)
	benchmarkSQLitePoolWorkloadWithHandles(b, dbPath, db, db, active, workload)
}

func benchmarkSQLiteDedicatedWorkload(b *testing.B, readers, active int, workload string) {
	b.StopTimer()
	dbPath := filepath.Join(b.TempDir(), "pool-benchmark.db")
	connections, err := database.NewReadWrite(dbPath)
	if err != nil {
		b.Fatal(err)
	}
	defer connections.Close()
	connections.Reader.SetMaxOpenConns(readers)
	connections.Reader.SetMaxIdleConns(readers)
	unregister := repository.RegisterDedicatedWriter(connections.Reader, connections.Writer)
	defer unregister()
	benchmarkSQLitePoolWorkloadWithHandles(b, dbPath, connections.Reader, connections.Writer, active, workload)
}

func benchmarkSQLitePoolWorkloadWithHandles(b *testing.B, dbPath string, readDB, writeDB *sql.DB, active int, workload string) {
	seedSQLitePoolBenchmark(b, writeDB)
	taskRepo := repository.NewTaskRepo(readDB, nil)
	executionRepo := repository.NewExecutionRepo(readDB)
	automationRepo := repository.NewAutomationRepo(readDB)

	// A sustained independent reader models execution-history/SSE consumers and
	// makes WAL growth visible without consuming a slot from the measured pool.
	readerDB, err := database.New(dbPath)
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
	warm := make([]*sql.Conn, 0, readDB.Stats().MaxOpenConnections)
	for i := 0; i < readDB.Stats().MaxOpenConnections; i++ {
		conn, err := readDB.Conn(context.Background())
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

	walBaseline := int64(0)
	if info, err := os.Stat(dbPath + "-wal"); err == nil {
		walBaseline = info.Size()
	}

	beforeRead := readDB.Stats()
	beforeWrite := writeDB.Stats()
	latencies := make([]time.Duration, b.N)
	var next atomic.Int64
	var busy, busySnapshot, streamFlushes atomic.Int64
	payload := strings.Repeat("stream-output-", 256)
	streamStop := make(chan struct{})
	streamErrors := make(chan error, active)
	var streamWG sync.WaitGroup
	if workload == "mixed" {
		for worker := 0; worker < active; worker++ {
			worker := worker
			streamWG.Add(1)
			go func() {
				defer streamWG.Done()
				ticker := time.NewTicker(500 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-streamStop:
						return
					case <-ticker.C:
						id := worker % 256
						if err := executionRepo.UpdateOutput(context.Background(), fmt.Sprintf("pool-exec-%03d", id), payload); err != nil {
							streamErrors <- err
							return
						}
						streamFlushes.Add(1)
					}
				}
			}()
		}
	}
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
				err := runSQLitePoolBenchmarkOperation(writeDB, taskRepo, executionRepo, automationRepo, workload, i, payload)
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
	close(streamStop)
	streamWG.Wait()
	close(streamErrors)
	for err := range streamErrors {
		b.Errorf("stream flush: %v", err)
	}
	elapsed := time.Since(started)
	b.StopTimer()

	afterRead := readDB.Stats()
	afterWrite := writeDB.Stats()
	readWaitCount := afterRead.WaitCount - beforeRead.WaitCount
	readWaitDuration := afterRead.WaitDuration - beforeRead.WaitDuration
	writeWaitCount := afterWrite.WaitCount - beforeWrite.WaitCount
	writeWaitDuration := afterWrite.WaitDuration - beforeWrite.WaitDuration
	if readDB == writeDB {
		writeWaitCount = 0
		writeWaitDuration = 0
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	if len(latencies) > 0 {
		b.ReportMetric(float64(latencies[len(latencies)/2].Microseconds()), "p50-us")
		b.ReportMetric(float64(latencies[(len(latencies)-1)*95/100].Microseconds()), "p95-us")
	}
	b.ReportMetric(float64(b.N)/elapsed.Seconds(), "ops/s")
	b.ReportMetric(float64(readWaitCount+writeWaitCount), "wait-count")
	b.ReportMetric(float64((readWaitDuration + writeWaitDuration).Microseconds()), "wait-us")
	b.ReportMetric(float64(readWaitCount), "read-wait-count")
	b.ReportMetric(float64(readWaitDuration.Microseconds()), "read-wait-us")
	b.ReportMetric(float64(writeWaitCount), "write-wait-count")
	b.ReportMetric(float64(writeWaitDuration.Microseconds()), "write-wait-us")
	b.ReportMetric(float64(busy.Load()), "sqlite-busy")
	b.ReportMetric(float64(busySnapshot.Load()), "busy-snapshot")
	b.ReportMetric(float64(streamFlushes.Load()), "stream-flushes")
	if info, err := os.Stat(dbPath + "-wal"); err == nil {
		growth := info.Size() - walBaseline
		if growth < 0 {
			growth = 0
		}
		b.ReportMetric(float64(growth), "wal-growth-bytes")
	}
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
	if _, err := tx.Exec(`
		INSERT INTO automations(id, project_id, stable_key, name, lifecycle_state) VALUES ('pool-automation', 'pool-bench', 'pool-automation', 'Pool Automation', 'active');
		INSERT INTO automation_versions(id, project_id, automation_id, version, state, source, adapter_key) VALUES ('pool-version', 'pool-bench', 'pool-automation', 1, 'published', 'bootstrap', 'custom');
		INSERT INTO automation_nodes(id, project_id, automation_id, version_id, node_key, name, node_type, role) VALUES ('pool-trigger', 'pool-bench', 'pool-automation', 'pool-version', 'trigger', 'Trigger', 'trigger', 'trigger');
		UPDATE automations SET published_version_id='pool-version' WHERE id='pool-automation';
	`); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 256; i++ {
		taskID := fmt.Sprintf("pool-task-%03d", i)
		streamTaskID := fmt.Sprintf("stream-task-%03d", i)
		execID := fmt.Sprintf("pool-exec-%03d", i)
		invocationID := fmt.Sprintf("pool-invocation-%03d", i)
		dispatchID := fmt.Sprintf("pool-dispatch-%03d", i)
		if _, err := tx.Exec(`INSERT INTO tasks(id, project_id, title, category, status) VALUES (?, 'pool-bench', ?, 'active', 'pending')`, taskID, "Pool task "+taskID); err != nil {
			b.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO tasks(id, project_id, title, category, status) VALUES (?, 'pool-bench', ?, 'active', 'running')`, streamTaskID, "Stream task "+streamTaskID); err != nil {
			b.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO executions(id, task_id, status, prompt_sent, output) VALUES (?, ?, 'running', 'benchmark prompt', 'initial output')`, execID, streamTaskID); err != nil {
			b.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO automation_invocations(id, project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status) VALUES (?, 'pool-bench', 'pool-automation', 'pool-version', 'pool-trigger', 'task', ?, ?, 'claimed')`, invocationID, taskID, "occurrence-"+taskID); err != nil {
			b.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO automation_dispatch_outbox(id, invocation_id, task_id, status) VALUES (?, ?, ?, 'pending')`, dispatchID, invocationID, taskID); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

func runSQLitePoolBenchmarkOperation(writeDB *sql.DB, taskRepo *repository.TaskRepo, executionRepo *repository.ExecutionRepo, automationRepo *repository.AutomationRepo, workload string, i int, payload string) error {
	id := i % 256
	taskID := fmt.Sprintf("pool-task-%03d", id)
	switch workload {
	case "read":
		if _, err := taskRepo.GetByID(context.Background(), taskID); err != nil {
			return err
		}
		_, err := executionRepo.ListByTaskChronologicalLimit(context.Background(), fmt.Sprintf("stream-task-%03d", id), 20)
		return err
	case "write":
		return executionRepo.UpdateOutput(context.Background(), fmt.Sprintf("pool-exec-%03d", id), payload)
	case "mixed":
		if i%5 != 0 {
			return runSQLitePoolBenchmarkOperation(writeDB, taskRepo, executionRepo, automationRepo, "read", i, payload)
		}
		if i%10 == 0 {
			claimed, err := taskRepo.ClaimTask(context.Background(), taskID)
			if err != nil {
				return err
			}
			if claimed {
				_, err = writeDB.Exec(`UPDATE tasks SET status='pending' WHERE id=? AND status='running'`, taskID)
			}
			return err
		}
		dispatch, err := automationRepo.LeaseNextDispatch(context.Background(), fmt.Sprintf("benchmark-worker-%d", i), time.Now().UTC(), time.Minute)
		if err != nil || dispatch == nil {
			return err
		}
		_, err = writeDB.Exec(`UPDATE automation_dispatch_outbox SET status='pending', claimed_by='', claim_expires_at=NULL WHERE id=? AND status='processing'`, dispatch.ID)
		return err
	default:
		return fmt.Errorf("unknown workload %q", workload)
	}
}

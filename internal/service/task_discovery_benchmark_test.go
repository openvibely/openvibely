package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/database"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func BenchmarkExecuteListTasksTool(b *testing.B) {
	fixtures := []struct {
		name       string
		taskCount  int
		promptSize int
		configSize int
		input      json.RawMessage
	}{
		{name: "Default20_LargePayload", taskCount: 20, promptSize: 64 * 1024, configSize: 16 * 1024},
		{name: "Maximum50_LargePayload", taskCount: 50, promptSize: 64 * 1024, configSize: 16 * 1024, input: json.RawMessage(`{"limit":50}`)},
		{name: "Small20_Control", taskCount: 20, promptSize: 128, configSize: 128},
	}

	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) {
			db, taskRepo := newTaskDiscoveryBenchmarkRepo(b, fixture.taskCount, fixture.promptSize, fixture.configSize)
			defer db.Close()

			ctx := context.Background()
			var outputBytes int
			b.ReportAllocs()
			b.ResetTimer()
			b.StartTimer()
			for i := 0; i < b.N; i++ {
				out, err := ExecuteListTasksTool(ctx, taskRepo, "default", fixture.input)
				if err != nil {
					b.Fatalf("ExecuteListTasksTool: %v", err)
				}
				outputBytes = len(out)
			}
			b.StopTimer()
			b.ReportMetric(float64(outputBytes), "output_bytes/op")
			b.ReportMetric(float64(fixture.taskCount*(fixture.promptSize+2*fixture.configSize)), "fixture_payload_bytes/op")
		})
	}
}

func newTaskDiscoveryBenchmarkRepo(b *testing.B, taskCount, promptSize, configSize int) (*sql.DB, *repository.TaskRepo) {
	b.Helper()
	b.StopTimer()
	db, err := database.New(":memory:")
	if err != nil {
		b.Fatalf("create benchmark database: %v", err)
	}
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()
	prompt := strings.Repeat("p", promptSize)
	chainConfig := `{"payload":"` + strings.Repeat("c", configSize) + `"}`
	swarmConfig := `{"payload":"` + strings.Repeat("s", configSize) + `"}`
	for i := 0; i < taskCount; i++ {
		task := &models.Task{
			ProjectID:   "default",
			Title:       fmt.Sprintf("Discovery benchmark task %03d", i),
			Category:    models.CategoryActive,
			Priority:    (i % 4) + 1,
			Status:      models.StatusPending,
			Prompt:      prompt,
			ChainConfig: chainConfig,
			SwarmConfig: swarmConfig,
		}
		if err := taskRepo.Create(ctx, task); err != nil {
			db.Close()
			b.Fatalf("create benchmark task %d: %v", i, err)
		}
	}
	return db, taskRepo
}

type taskDiscoveryOrderPage struct {
	name  string
	input json.RawMessage
}

// BenchmarkExecuteListTasksToolOrderScaling exercises the production discovery
// service against realistic project sizes. The task payloads stay deliberately
// small so this benchmark isolates page ordering rather than the full-row
// projection work covered by the compact-projection benchmark above.
func BenchmarkExecuteListTasksToolOrderScaling(b *testing.B) {
	for _, taskCount := range []int{100, 1000, 5000, 10000} {
		for _, page := range []taskDiscoveryOrderPage{
			{name: "Default20_SmallPayload", input: nil},
			{name: "Maximum50_SmallPayload", input: json.RawMessage(`{"limit":50}`)},
		} {
			taskCount := taskCount
			page := page
			b.Run(fmt.Sprintf("%d_tasks/%s", taskCount, page.name), func(b *testing.B) {
				db, taskRepo := newTaskDiscoveryOrderBenchmarkRepo(b, taskCount, 128, 128)
				defer db.Close()
				for _, variant := range []struct {
					name    string
					indexed bool
				}{
					{name: "CurrentBaseline", indexed: false},
					{name: "OrderCoveringIndex", indexed: true},
				} {
					variant := variant
					b.Run(variant.name, func(b *testing.B) {
						setTaskDiscoveryOrderIndex(b, db, variant.indexed)
						ctx := context.Background()
						var outputBytes int

						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							out, err := ExecuteListTasksTool(ctx, taskRepo, "default", page.input)
							if err != nil {
								b.Fatalf("ExecuteListTasksTool: %v", err)
							}
							outputBytes = len(out)
						}
						b.StopTimer()
						b.ReportMetric(float64(outputBytes), "output_bytes/op")
						b.ReportMetric(float64(taskCount*(128+2*128)), "fixture_payload_bytes/op")
					})
				}
			})
		}
	}
}

func newTaskDiscoveryOrderBenchmarkRepo(tb testing.TB, taskCount, promptSize, configSize int) (*sql.DB, *repository.TaskRepo) {
	tb.Helper()
	db := testutil.NewTestDB(tb)
	taskRepo := repository.NewTaskRepo(db, nil)
	prompt := strings.Repeat("p", promptSize)
	chainConfig := `{"payload":"` + strings.Repeat("c", configSize) + `"}`
	swarmConfig := `{"payload":"` + strings.Repeat("s", configSize) + `"}`
	_, err := db.ExecContext(context.Background(), `
		WITH RECURSIVE seq(n) AS (
			SELECT 1
			UNION ALL
			SELECT n + 1 FROM seq WHERE n < ?
		)
		INSERT INTO tasks
			(id, project_id, title, category, priority, status, prompt, chain_config, swarm_config, updated_at)
		SELECT
			'task-discovery-benchmark-' || printf('%05d', n),
			'default',
			'Discovery benchmark task ' || printf('%05d', n),
			CASE WHEN n % 20 = 0 THEN 'chat' ELSE 'active' END,
			(n % 4) + 1,
			'pending',
			?, ?, ?,
			datetime('2026-01-01 00:00:00', '+' || n || ' seconds')
		FROM seq`, taskCount, prompt, chainConfig, swarmConfig)
	if err != nil {
		tb.Fatalf("seed %d-task discovery benchmark: %v", taskCount, err)
	}
	return db, taskRepo
}

const (
	taskDiscoveryOrderIndexName       = "idx_tasks_discovery_order"
	taskDiscoveryPairedSamples        = 7
	taskDiscoveryTimingRuns           = 3
	taskDiscoveryAllocationRuns       = 3
	taskDiscoveryMaxPage20Improvement = 0.30
	taskDiscoveryMaxControlRegression = 1.10

	// The discovery index must add no more than 20% to the production UpdateStatus
	// path and must stay below 2 MiB for the 10,000-task fixture.
	taskDiscoveryWriteOperations                 = 40
	taskDiscoveryMaxWriteLatencyRegression       = 1.20
	taskDiscoveryMaxIndexStorageBytes      int64 = 2 * 1024 * 1024
)

type taskDiscoveryPairedMetrics struct {
	wallNs      []float64
	allocsPerOp []float64
	outputBytes []int
}

func (m *taskDiscoveryPairedMetrics) add(sample taskDiscoveryPairedMetrics) {
	m.wallNs = append(m.wallNs, sample.wallNs...)
	m.allocsPerOp = append(m.allocsPerOp, sample.allocsPerOp...)
	m.outputBytes = append(m.outputBytes, sample.outputBytes...)
}

func medianTaskDiscoveryMetric(values []float64) float64 {
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	return ordered[len(ordered)/2]
}

func (m taskDiscoveryPairedMetrics) medianWallNs() float64 {
	return medianTaskDiscoveryMetric(m.wallNs)
}

func (m taskDiscoveryPairedMetrics) medianAllocsPerOp() float64 {
	return medianTaskDiscoveryMetric(m.allocsPerOp)
}

func (m taskDiscoveryPairedMetrics) medianOutputBytes() int {
	ordered := append([]int(nil), m.outputBytes...)
	sort.Ints(ordered)
	return ordered[len(ordered)/2]
}

func setTaskDiscoveryOrderIndex(tb testing.TB, db *sql.DB, enabled bool) {
	tb.Helper()
	query := `DROP INDEX IF EXISTS ` + taskDiscoveryOrderIndexName
	if enabled {
		query = `CREATE INDEX IF NOT EXISTS ` + taskDiscoveryOrderIndexName + `
			ON tasks(project_id, updated_at DESC, id ASC)
			WHERE category != 'chat'`
	}
	if _, err := db.ExecContext(context.Background(), query); err != nil {
		tb.Fatalf("set task discovery order index enabled=%v: %v", enabled, err)
	}
}

func measureTaskDiscoveryOrderSample(tb testing.TB, taskRepo *repository.TaskRepo, input json.RawMessage, want string) taskDiscoveryPairedMetrics {
	tb.Helper()
	outputs := make([]string, taskDiscoveryTimingRuns)
	outputBytes := 0
	wallStarted := time.Now()
	for i := 0; i < taskDiscoveryTimingRuns; i++ {
		out, err := ExecuteListTasksTool(context.Background(), taskRepo, "default", input)
		if err != nil {
			tb.Fatalf("timed ExecuteListTasksTool: %v", err)
		}
		outputs[i] = out
		outputBytes = len(out)
	}
	wallNs := float64(time.Since(wallStarted).Nanoseconds()) / float64(taskDiscoveryTimingRuns)
	for _, out := range outputs {
		if out != want {
			tb.Fatalf("discovery output changed during measurement:\nwant %s\n got %s", want, out)
		}
	}

	var allocationOutput string
	var allocationErr error
	allocsPerOp := testing.AllocsPerRun(taskDiscoveryAllocationRuns, func() {
		allocationOutput, allocationErr = ExecuteListTasksTool(context.Background(), taskRepo, "default", input)
	})
	if allocationErr != nil {
		tb.Fatalf("allocation ExecuteListTasksTool: %v", allocationErr)
	}
	if allocationOutput != want {
		tb.Fatalf("discovery allocation output changed:\nwant %s\n got %s", want, allocationOutput)
	}

	return taskDiscoveryPairedMetrics{
		wallNs:      []float64{wallNs},
		allocsPerOp: []float64{allocsPerOp},
		outputBytes: []int{outputBytes},
	}
}

func measureTaskDiscoveryOrderPair(tb testing.TB, taskCount int, input json.RawMessage) (taskDiscoveryPairedMetrics, taskDiscoveryPairedMetrics) {
	tb.Helper()
	db, taskRepo := newTaskDiscoveryOrderBenchmarkRepo(tb, taskCount, 128, 128)
	defer db.Close()

	want, err := ExecuteListTasksTool(context.Background(), taskRepo, "default", input)
	if err != nil {
		tb.Fatalf("warm ExecuteListTasksTool: %v", err)
	}
	var baseline, candidate taskDiscoveryPairedMetrics
	for sample := 0; sample < taskDiscoveryPairedSamples; sample++ {
		if sample%2 == 0 {
			setTaskDiscoveryOrderIndex(tb, db, false)
			baseline.add(measureTaskDiscoveryOrderSample(tb, taskRepo, input, want))
			setTaskDiscoveryOrderIndex(tb, db, true)
			candidate.add(measureTaskDiscoveryOrderSample(tb, taskRepo, input, want))
			continue
		}
		setTaskDiscoveryOrderIndex(tb, db, true)
		candidate.add(measureTaskDiscoveryOrderSample(tb, taskRepo, input, want))
		setTaskDiscoveryOrderIndex(tb, db, false)
		baseline.add(measureTaskDiscoveryOrderSample(tb, taskRepo, input, want))
	}
	setTaskDiscoveryOrderIndex(tb, db, true)
	return baseline, candidate
}

func TestTaskDiscoveryOrderIndexMeetsPairedLatencyThresholds(t *testing.T) {
	for _, taskCount := range []int{100, 1000, 5000, 10000} {
		for _, page := range []taskDiscoveryOrderPage{
			{name: "Default20", input: nil},
			{name: "Maximum50", input: json.RawMessage(`{"limit":50}`)},
		} {
			taskCount := taskCount
			page := page
			t.Run(fmt.Sprintf("%d_tasks/%s", taskCount, page.name), func(t *testing.T) {
				baseline, candidate := measureTaskDiscoveryOrderPair(t, taskCount, page.input)
				baselineNs := baseline.medianWallNs()
				candidateNs := candidate.medianWallNs()
				t.Logf("paired median ns/op: baseline=%.0f candidate=%.0f; allocs/op: baseline=%.1f candidate=%.1f; output_bytes: baseline=%d candidate=%d", baselineNs, candidateNs, baseline.medianAllocsPerOp(), candidate.medianAllocsPerOp(), baseline.medianOutputBytes(), candidate.medianOutputBytes())

				if baseline.medianOutputBytes() != candidate.medianOutputBytes() {
					t.Fatalf("output bytes changed: baseline=%d candidate=%d", baseline.medianOutputBytes(), candidate.medianOutputBytes())
				}
				if candidate.medianAllocsPerOp() > baseline.medianAllocsPerOp()*taskDiscoveryMaxControlRegression {
					t.Fatalf("candidate allocations regressed by more than 10%%: baseline=%.1f candidate=%.1f", baseline.medianAllocsPerOp(), candidate.medianAllocsPerOp())
				}
				if taskCount == 10000 && page.name == "Default20" && candidateNs > baselineNs*taskDiscoveryMaxPage20Improvement {
					t.Fatalf("10,000-task default page latency did not improve by at least 70%%: baseline=%.0f ns/op candidate=%.0f ns/op", baselineNs, candidateNs)
				}
				if taskCount == 100 && candidateNs > baselineNs*taskDiscoveryMaxControlRegression {
					t.Fatalf("100-task control page latency regressed by more than 10%%: baseline=%.0f ns/op candidate=%.0f ns/op", baselineNs, candidateNs)
				}
			})
		}
	}
}

func measureTaskDiscoveryWriteSample(tb testing.TB, taskRepo *repository.TaskRepo, taskID string) float64 {
	tb.Helper()
	started := time.Now()
	for i := 0; i < taskDiscoveryWriteOperations; i++ {
		status := models.StatusPending
		if i%2 == 1 {
			status = models.StatusRunning
		}
		if err := taskRepo.UpdateStatus(context.Background(), taskID, status); err != nil {
			tb.Fatalf("UpdateStatus write-budget sample: %v", err)
		}
	}
	return float64(time.Since(started).Nanoseconds()) / float64(taskDiscoveryWriteOperations)
}

func TestTaskDiscoveryOrderIndexWriteBudget(t *testing.T) {
	db, taskRepo := newTaskDiscoveryOrderBenchmarkRepo(t, 10000, 128, 128)
	const taskID = "task-discovery-benchmark-00001"
	setTaskDiscoveryOrderIndex(t, db, false)
	if err := taskRepo.UpdateStatus(context.Background(), taskID, models.StatusPending); err != nil {
		t.Fatalf("warm task write target: %v", err)
	}

	var baseline, candidate taskDiscoveryPairedMetrics
	for sample := 0; sample < taskDiscoveryPairedSamples; sample++ {
		if sample%2 == 0 {
			setTaskDiscoveryOrderIndex(t, db, false)
			baseline.wallNs = append(baseline.wallNs, measureTaskDiscoveryWriteSample(t, taskRepo, taskID))
			setTaskDiscoveryOrderIndex(t, db, true)
			candidate.wallNs = append(candidate.wallNs, measureTaskDiscoveryWriteSample(t, taskRepo, taskID))
			continue
		}
		setTaskDiscoveryOrderIndex(t, db, true)
		candidate.wallNs = append(candidate.wallNs, measureTaskDiscoveryWriteSample(t, taskRepo, taskID))
		setTaskDiscoveryOrderIndex(t, db, false)
		baseline.wallNs = append(baseline.wallNs, measureTaskDiscoveryWriteSample(t, taskRepo, taskID))
	}
	setTaskDiscoveryOrderIndex(t, db, true)

	baselineNs := baseline.medianWallNs()
	candidateNs := candidate.medianWallNs()
	indexBytes := taskDiscoveryOrderIndexBytes(t, db)
	t.Logf("paired write median ns/op: baseline=%.0f candidate=%.0f; discovery index storage bytes=%d", baselineNs, candidateNs, indexBytes)
	if candidateNs > baselineNs*taskDiscoveryMaxWriteLatencyRegression {
		t.Fatalf("discovery index write latency regressed by more than 20%%: baseline=%.0f ns/op candidate=%.0f ns/op", baselineNs, candidateNs)
	}
	if indexBytes <= 0 || indexBytes > taskDiscoveryMaxIndexStorageBytes {
		t.Fatalf("discovery index storage = %d bytes, want 1..%d bytes for the 10,000-task fixture", indexBytes, taskDiscoveryMaxIndexStorageBytes)
	}
}

// BenchmarkTaskDiscoveryIndexWriteBudget records the write-time and storage
// cost of the partial order index on the same 10,000-task workload used for
// the read benchmark. UpdateStatus exercises the repository path that mutates
// status and updated_at.
func BenchmarkTaskDiscoveryIndexWriteBudget(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		name := "WithoutDiscoveryOrderIndex"
		if indexed {
			name = "WithDiscoveryOrderIndex"
		}
		indexed := indexed
		b.Run(name, func(b *testing.B) {
			db, taskRepo := newTaskDiscoveryOrderBenchmarkRepo(b, 10000, 128, 128)
			setTaskDiscoveryOrderIndex(b, db, indexed)
			ctx := context.Background()
			const taskID = "task-discovery-benchmark-00001"
			if _, err := taskRepo.GetByID(ctx, taskID); err != nil {
				b.Fatalf("warm task write target: %v", err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				status := models.StatusPending
				if i%2 == 0 {
					status = models.StatusRunning
				}
				if err := taskRepo.UpdateStatus(ctx, taskID, status); err != nil {
					b.Fatalf("UpdateStatus: %v", err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(taskDiscoveryOrderIndexBytes(b, db)), "discovery_index_storage_bytes")
		})
	}
}

func taskDiscoveryOrderIndexBytes(tb testing.TB, db *sql.DB) int64 {
	tb.Helper()
	var bytes sql.NullInt64
	if err := db.QueryRowContext(context.Background(), `SELECT COALESCE(SUM(pgsize), 0) FROM dbstat WHERE name = ?`, taskDiscoveryOrderIndexName).Scan(&bytes); err != nil {
		tb.Fatalf("measure task discovery index storage: %v", err)
	}
	if !bytes.Valid {
		return 0
	}
	return bytes.Int64
}

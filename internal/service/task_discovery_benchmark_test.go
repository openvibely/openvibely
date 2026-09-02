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

	// The discovery index must add no more than 20% to substantive task-write
	// paths, no more than 5µs to lightweight metadata setters, and must stay
	// below 2 MiB for the 10,000-task fixture.
	// Use a long measured phase so scheduler and coverage overhead do not dominate
	// the small per-update index cost.
	taskDiscoveryWriteOperations           = 1000
	taskDiscoveryMaxWriteLatencyRegression = 1.20
	// Lightweight metadata setters have a sub-10µs unindexed baseline, so a
	// relative ratio overstates their small fixed index cost. Keep their explicit
	// absolute overhead budget at 5µs while the substantive task-write paths below
	// retain the 20% relative gate.
	taskDiscoveryMaxLightweightWriteOverheadNs       = 5_000
	taskDiscoveryMaxIndexStorageBytes          int64 = 2 * 1024 * 1024
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

type taskDiscoveryWritePath struct {
	name  string
	setup func(testing.TB, *sql.DB, *repository.TaskRepo) func(context.Context, int) error
	clean func(testing.TB, *sql.DB)
}

const (
	taskDiscoveryWriteTargetID     = "task-discovery-benchmark-00001"
	taskDiscoveryWriteNeighborID   = "task-discovery-benchmark-00002"
	taskDiscoveryCreateTitlePrefix = "Discovery write-budget create "
	taskDiscoveryReorderOtherOrder = -1000000
)

func taskDiscoveryWriteTarget(tb testing.TB, taskRepo *repository.TaskRepo) *models.Task {
	tb.Helper()
	task, err := taskRepo.GetByID(context.Background(), taskDiscoveryWriteTargetID)
	if err != nil {
		tb.Fatalf("load task discovery write target: %v", err)
	}
	if task == nil {
		tb.Fatalf("task discovery write target %q not found", taskDiscoveryWriteTargetID)
	}
	return task
}

func taskDiscoveryWritePaths() []taskDiscoveryWritePath {
	return []taskDiscoveryWritePath{
		{
			name: "Create",
			setup: func(tb testing.TB, _ *sql.DB, taskRepo *repository.TaskRepo) func(context.Context, int) error {
				tb.Helper()
				return func(ctx context.Context, i int) error {
					category := models.CategoryActive
					if i%2 == 1 {
						category = models.CategoryChat
					}
					return taskRepo.Create(ctx, &models.Task{
						ProjectID: "default",
						Title:     fmt.Sprintf("%s%06d", taskDiscoveryCreateTitlePrefix, i),
						Category:  category,
						Status:    models.StatusPending,
						Prompt:    "p",
					})
				}
			},
			clean: func(tb testing.TB, db *sql.DB) {
				tb.Helper()
				if _, err := db.ExecContext(context.Background(), `DELETE FROM tasks WHERE project_id = ? AND title LIKE ?`, "default", taskDiscoveryCreateTitlePrefix+"%"); err != nil {
					tb.Fatalf("clean task discovery write fixtures: %v", err)
				}
			},
		},
		{
			name: "Update",
			setup: func(tb testing.TB, _ *sql.DB, taskRepo *repository.TaskRepo) func(context.Context, int) error {
				tb.Helper()
				task := taskDiscoveryWriteTarget(tb, taskRepo)
				return func(ctx context.Context, i int) error {
					switch i % 3 {
					case 0:
						task.Category = models.CategoryActive
					case 1:
						task.Category = models.CategoryBacklog
					default:
						task.Category = models.CategoryChat
					}
					task.Priority = (i % 4) + 1
					return taskRepo.Update(ctx, task)
				}
			},
		},
		{
			name: "UpdateCategory",
			setup: func(tb testing.TB, _ *sql.DB, taskRepo *repository.TaskRepo) func(context.Context, int) error {
				tb.Helper()
				targetID := taskDiscoveryWriteTarget(tb, taskRepo).ID
				return func(ctx context.Context, i int) error {
					var category models.TaskCategory
					switch i % 3 {
					case 0:
						category = models.CategoryActive
					case 1:
						category = models.CategoryBacklog
					default:
						category = models.CategoryChat
					}
					return taskRepo.UpdateCategory(ctx, targetID, category)
				}
			},
		},
		{
			name: "ReorderTask",
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) func(context.Context, int) error {
				tb.Helper()
				ctx := context.Background()
				if _, err := db.ExecContext(ctx, `UPDATE tasks SET display_order = ? WHERE project_id = ?`, taskDiscoveryReorderOtherOrder, "default"); err != nil {
					tb.Fatalf("set task discovery reorder filler order: %v", err)
				}
				if _, err := db.ExecContext(ctx, `UPDATE tasks SET display_order = -1 WHERE id = ?`, taskDiscoveryWriteTargetID); err != nil {
					tb.Fatalf("set task discovery reorder target order: %v", err)
				}
				if _, err := db.ExecContext(ctx, `UPDATE tasks SET display_order = 0 WHERE id = ?`, taskDiscoveryWriteNeighborID); err != nil {
					tb.Fatalf("set task discovery reorder neighbor order: %v", err)
				}
				targetID := taskDiscoveryWriteTarget(tb, taskRepo).ID
				return func(ctx context.Context, i int) error {
					newPosition := -1
					if i%2 == 0 {
						newPosition = 0
					}
					return taskRepo.ReorderTask(ctx, targetID, newPosition)
				}
			},
		},
		{
			name: "UpdateStatus",
			setup: func(tb testing.TB, _ *sql.DB, taskRepo *repository.TaskRepo) func(context.Context, int) error {
				tb.Helper()
				targetID := taskDiscoveryWriteTarget(tb, taskRepo).ID
				return func(ctx context.Context, i int) error {
					status := models.StatusPending
					if i%2 == 1 {
						status = models.StatusRunning
					}
					return taskRepo.UpdateStatus(ctx, targetID, status)
				}
			},
		},
		{
			name: "UpdateSwarmFields",
			setup: func(tb testing.TB, _ *sql.DB, taskRepo *repository.TaskRepo) func(context.Context, int) error {
				tb.Helper()
				targetID := taskDiscoveryWriteTarget(tb, taskRepo).ID
				return func(ctx context.Context, i int) error {
					role := models.SwarmRoleNone
					status := "idle"
					if i%2 == 1 {
						role = models.SwarmRoleWorker
						status = "running"
					}
					return taskRepo.UpdateSwarmFields(ctx, targetID, role, status, fmt.Sprintf(`{"phase":%d}`, i%2), i%2)
				}
			},
		},
		{
			name: "UpdateAgentID",
			setup: func(tb testing.TB, _ *sql.DB, taskRepo *repository.TaskRepo) func(context.Context, int) error {
				tb.Helper()
				targetID := taskDiscoveryWriteTarget(tb, taskRepo).ID
				return func(ctx context.Context, _ int) error {
					return taskRepo.UpdateAgentID(ctx, targetID, "")
				}
			},
		},
		{
			name: "UpdateWorktreeInfo",
			setup: func(tb testing.TB, _ *sql.DB, taskRepo *repository.TaskRepo) func(context.Context, int) error {
				tb.Helper()
				targetID := taskDiscoveryWriteTarget(tb, taskRepo).ID
				return func(ctx context.Context, i int) error {
					return taskRepo.UpdateWorktreeInfo(ctx, targetID, fmt.Sprintf("/tmp/discovery-worktree-%d", i%2), fmt.Sprintf("task/discovery-%d", i%2))
				}
			},
		},
		{
			name: "UpdateMergeStatus",
			setup: func(tb testing.TB, _ *sql.DB, taskRepo *repository.TaskRepo) func(context.Context, int) error {
				tb.Helper()
				targetID := taskDiscoveryWriteTarget(tb, taskRepo).ID
				return func(ctx context.Context, i int) error {
					status := models.MergeStatusPending
					if i%2 == 1 {
						status = models.MergeStatusFailed
					}
					return taskRepo.UpdateMergeStatus(ctx, targetID, status)
				}
			},
		},
		{
			name: "UpdateAutoMerge",
			setup: func(tb testing.TB, _ *sql.DB, taskRepo *repository.TaskRepo) func(context.Context, int) error {
				tb.Helper()
				targetID := taskDiscoveryWriteTarget(tb, taskRepo).ID
				return func(ctx context.Context, i int) error {
					return taskRepo.UpdateAutoMerge(ctx, targetID, i%2 == 1, "main")
				}
			},
		},
		{
			name: "UpdateLineage",
			setup: func(tb testing.TB, _ *sql.DB, taskRepo *repository.TaskRepo) func(context.Context, int) error {
				tb.Helper()
				targetID := taskDiscoveryWriteTarget(tb, taskRepo).ID
				return func(ctx context.Context, i int) error {
					return taskRepo.UpdateLineage(ctx, targetID, "main", strings.Repeat(fmt.Sprintf("%x", i%16), 40), i%2)
				}
			},
		},
		{
			name: "UpdateTelegramOrigin",
			setup: func(tb testing.TB, _ *sql.DB, taskRepo *repository.TaskRepo) func(context.Context, int) error {
				tb.Helper()
				targetID := taskDiscoveryWriteTarget(tb, taskRepo).ID
				return func(ctx context.Context, i int) error {
					return taskRepo.UpdateTelegramOrigin(ctx, targetID, int64(i%2))
				}
			},
		},
	}
}

func taskDiscoveryWriteUsesRelativeBudget(name string) bool {
	switch name {
	case "Create", "Update", "UpdateCategory", "ReorderTask", "UpdateStatus":
		return true
	default:
		return false
	}
}

func measureTaskDiscoveryWritePathSample(tb testing.TB, path taskDiscoveryWritePath, db *sql.DB, taskRepo *repository.TaskRepo, indexed bool) float64 {
	tb.Helper()
	setTaskDiscoveryOrderIndex(tb, db, indexed)
	write := path.setup(tb, db, taskRepo)
	started := time.Now()
	for i := 0; i < taskDiscoveryWriteOperations; i++ {
		if err := write(context.Background(), i); err != nil {
			tb.Fatalf("%s write-budget sample: %v", path.name, err)
		}
	}
	elapsed := time.Since(started)
	if path.clean != nil {
		path.clean(tb, db)
	}
	return float64(elapsed.Nanoseconds()) / float64(taskDiscoveryWriteOperations)
}

func TestTaskDiscoveryOrderIndexWriteBudget(t *testing.T) {
	db, taskRepo := newTaskDiscoveryOrderBenchmarkRepo(t, 10000, 128, 128)
	defer db.Close()

	for _, path := range taskDiscoveryWritePaths() {
		path := path
		t.Run(path.name, func(t *testing.T) {
			var baseline, candidate taskDiscoveryPairedMetrics
			for sample := 0; sample < taskDiscoveryPairedSamples; sample++ {
				if sample%2 == 0 {
					baseline.wallNs = append(baseline.wallNs, measureTaskDiscoveryWritePathSample(t, path, db, taskRepo, false))
					candidate.wallNs = append(candidate.wallNs, measureTaskDiscoveryWritePathSample(t, path, db, taskRepo, true))
					continue
				}
				candidate.wallNs = append(candidate.wallNs, measureTaskDiscoveryWritePathSample(t, path, db, taskRepo, true))
				baseline.wallNs = append(baseline.wallNs, measureTaskDiscoveryWritePathSample(t, path, db, taskRepo, false))
			}

			baselineNs := baseline.medianWallNs()
			candidateNs := candidate.medianWallNs()
			t.Logf("paired %s median ns/op: baseline=%.0f candidate=%.0f overhead=%.1f%% delta=%.0f ns", path.name, baselineNs, candidateNs, (candidateNs/baselineNs-1)*100, candidateNs-baselineNs)
			if taskDiscoveryWriteUsesRelativeBudget(path.name) {
				if candidateNs > baselineNs*taskDiscoveryMaxWriteLatencyRegression {
					t.Fatalf("%s indexed write latency regressed by more than 20%%: baseline=%.0f ns/op candidate=%.0f ns/op", path.name, baselineNs, candidateNs)
				}
			} else if candidateNs > baselineNs+float64(taskDiscoveryMaxLightweightWriteOverheadNs) {
				t.Fatalf("%s indexed write latency added more than %d ns: baseline=%.0f ns/op candidate=%.0f ns/op", path.name, taskDiscoveryMaxLightweightWriteOverheadNs, baselineNs, candidateNs)
			}
		})
	}

	setTaskDiscoveryOrderIndex(t, db, true)
	indexBytes := taskDiscoveryOrderIndexBytes(t, db)
	t.Logf("discovery index storage bytes on 10,000-task fixture: %d", indexBytes)
	if indexBytes <= 0 || indexBytes > taskDiscoveryMaxIndexStorageBytes {
		t.Fatalf("discovery index storage = %d bytes, want 1..%d bytes for the 10,000-task fixture", indexBytes, taskDiscoveryMaxIndexStorageBytes)
	}
}

// BenchmarkTaskDiscoveryIndexWriteBudget records the write-time and storage
// cost of the partial order index across every distinct task mutation shape in
// the production repository. UpdateStatus alone is not representative because
// Create, full updates, category membership changes, bulk reorder updates, and
// metadata setters can all maintain the same indexed updated_at key.
func BenchmarkTaskDiscoveryIndexWriteBudget(b *testing.B) {
	for _, path := range taskDiscoveryWritePaths() {
		path := path
		for _, indexed := range []bool{false, true} {
			name := "WithoutDiscoveryOrderIndex"
			if indexed {
				name = "WithDiscoveryOrderIndex"
			}
			b.Run(path.name+"/"+name, func(b *testing.B) {
				db, taskRepo := newTaskDiscoveryOrderBenchmarkRepo(b, 10000, 128, 128)
				defer db.Close()
				setTaskDiscoveryOrderIndex(b, db, indexed)
				write := path.setup(b, db, taskRepo)
				ctx := context.Background()

				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := write(ctx, i); err != nil {
						b.Fatalf("%s %s: %v", path.name, name, err)
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(taskDiscoveryOrderIndexBytes(b, db)), "discovery_index_storage_bytes")
			})
		}
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

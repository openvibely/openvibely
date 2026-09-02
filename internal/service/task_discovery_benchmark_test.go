package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
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

	// The discovery index write budget has an explicit check for every writer.
	// Substantive task transitions use the strict 20% relative cap. Lightweight
	// metadata setters have a small fixed index-maintenance cost relative to their
	// sub-10µs baseline, so each uses a 5µs absolute cap instead of an unstable
	// ratio. Bulk paths report and aggregate affected rows rather than counting
	// one multi-row statement as one row of work.
	// Use a long measured phase so scheduler and coverage overhead do not dominate
	// the small per-update index cost.
	taskDiscoveryWriteOperations                        = 1000
	taskDiscoveryWritePathCount                         = 52
	taskDiscoveryMaxWriteLatencyRegression              = 1.20
	taskDiscoveryMaxFixedPerAffectedRowOverheadNs       = 5_000
	taskDiscoveryMaxIndexStorageBytes             int64 = 2 * 1024 * 1024
)

func taskDiscoveryTimingAssertionsDisabled() bool {
	coverProfile := flag.Lookup("test.coverprofile")
	return coverProfile != nil && coverProfile.Value.String() != ""
}

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
	timingAssertionsDisabled := taskDiscoveryTimingAssertionsDisabled()
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
				if timingAssertionsDisabled {
					t.Log("discovery calls, output-byte, and allocation assertions executed; timing thresholds disabled under coverage instrumentation")
					return
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

// The indexed-writer inventory below covers every direct TaskRepo task INSERT,
// UPDATE, and DELETE shape, including single-row, multi-row, and bulk paths.
// It also invokes the external task writers that use a distinct task SQL shape:
// AlertRepo.CreateImplementationTask, ExecutionRepo direct-follow-up and restart
// recovery, and ThreadInputRepo task-thread/chat promotion. Automation task
// writers are exercised through their public repository entry points below,
// including SaveCurrentGraph, lifecycle transitions, dispatch admission and
// completion, cancellation/failure, and chained-task activation. AgentRepo and
// LLMConfigRepo task nullifiers update only agent columns and do not write
// project_id, category, or updated_at, so they cannot affect this index and are
// explicitly out of scope.
type taskDiscoveryWriteRun struct {
	write   func(context.Context, int) error
	cleanup func(testing.TB)
}

type taskDiscoveryWritePath struct {
	name         string
	operations   int
	affectedRows int
	setup        func(testing.TB, *sql.DB, *repository.TaskRepo) taskDiscoveryWriteRun
}

const (
	taskDiscoveryWriteTargetID        = "task-discovery-benchmark-00001"
	taskDiscoveryWriteNeighborID      = "task-discovery-benchmark-00002"
	taskDiscoveryCreateTitlePrefix    = "Discovery write-budget create "
	taskDiscoveryReorderOtherOrder    = -1000000
	taskDiscoveryWritePoolPrefix      = "task-discovery-write-"
	taskDiscoveryWritePoolSize        = taskDiscoveryWriteOperations
	taskDiscoveryWriteScheduleID      = "task-discovery-write-schedule"
	taskDiscoveryWriteAlertPrefix     = "task-discovery-write-alert-"
	taskDiscoveryWriteAlertTaskPrefix = "Discovery write-budget alert task "
	taskDiscoveryBulkWriteOperations  = 1
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

func resetTaskDiscoveryWriteTarget(tb testing.TB, db *sql.DB) {
	tb.Helper()
	if _, err := db.ExecContext(context.Background(), `UPDATE tasks
		SET category = 'active', status = 'pending', display_order = 0,
			parent_task_id = NULL, swarm_role = '', swarm_status = '', swarm_sequence = 0,
			worktree_path = '', worktree_branch = '', auto_merge = 0,
			merge_status = '', base_branch = '', base_commit_sha = '', lineage_depth = 0,
			completed_at = NULL, updated_at = '2020-01-01 00:00:00'
		WHERE id = ?`, taskDiscoveryWriteTargetID); err != nil {
		tb.Fatalf("reset task discovery write target: %v", err)
	}
}

func taskDiscoveryWriteOperationCount(tb testing.TB) int {
	if benchmark, ok := tb.(*testing.B); ok && benchmark.N > taskDiscoveryWritePoolSize {
		return benchmark.N
	}
	return taskDiscoveryWritePoolSize
}

func taskDiscoveryDefaultAgentConfigID(tb testing.TB, db *sql.DB) string {
	tb.Helper()
	var id string
	if err := db.QueryRowContext(context.Background(), `SELECT id FROM agent_configs WHERE is_default = 1 LIMIT 1`).Scan(&id); err != nil {
		tb.Fatalf("load task discovery default agent config: %v", err)
	}
	return id
}

func seedTaskDiscoveryWritePool(tb testing.TB, db *sql.DB, prefix string, category models.TaskCategory, status models.TaskStatus, updatedAt, parentTaskID string, count int) []string {
	tb.Helper()
	if count < 1 {
		count = 1
	}
	if updatedAt == "" {
		updatedAt = "2020-01-01 00:00:00"
	}
	if _, err := db.ExecContext(context.Background(), `
		WITH RECURSIVE seq(n) AS (
			SELECT 1
			UNION ALL
			SELECT n + 1 FROM seq WHERE n < ?
		)
		INSERT INTO tasks
			(id, project_id, title, category, priority, status, prompt, parent_task_id, chain_config, swarm_config, updated_at)
		SELECT
			? || printf('%05d', n), 'default', ? || printf('%05d', n), ?, (n % 4) + 1, ?, 'p', NULLIF(?, ''), '{}', '{}', ?
		FROM seq`, count, prefix, prefix, category, status, parentTaskID, updatedAt); err != nil {
		tb.Fatalf("seed task discovery write pool %q: %v", prefix, err)
	}
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s%05d", prefix, i+1)
	}
	return ids
}

func seedTaskDiscoveryQueuedInputs(tb testing.TB, db *sql.DB, prefix string, count int) []string {
	tb.Helper()
	if count < 1 {
		count = 1
	}
	if _, err := db.ExecContext(context.Background(), `
		WITH RECURSIVE seq(n) AS (
			SELECT 1
			UNION ALL
			SELECT n + 1 FROM seq WHERE n < ?
		)
		INSERT INTO thread_inputs
			(id, scope, project_id, task_id, input_mode, input_status, content, queue_position)
		SELECT
			? || 'input-' || printf('%05d', n), 'task_thread', 'default',
			? || printf('%05d', n), 'queued', 'pending', 'p', n
		FROM seq`, count, prefix, prefix); err != nil {
		tb.Fatalf("seed task discovery queued inputs %q: %v", prefix, err)
	}
	inputIDs := make([]string, count)
	for i := range inputIDs {
		inputIDs[i] = fmt.Sprintf("%sinput-%05d", prefix, i+1)
	}
	return inputIDs
}

func seedTaskDiscoveryChatInputs(tb testing.TB, db *sql.DB, prefix string, count int) []string {
	tb.Helper()
	if count < 1 {
		count = 1
	}
	if _, err := db.ExecContext(context.Background(), `
		WITH RECURSIVE seq(n) AS (
			SELECT 1
			UNION ALL
			SELECT n + 1 FROM seq WHERE n < ?
		)
		INSERT INTO thread_inputs
			(id, scope, project_id, input_mode, input_status, content, queue_position)
		SELECT
			? || 'input-' || printf('%05d', n), 'chat', 'default',
			'queued', 'pending', 'p', n
		FROM seq`, count, prefix); err != nil {
		tb.Fatalf("seed task discovery chat inputs %q: %v", prefix, err)
	}
	inputIDs := make([]string, count)
	for i := range inputIDs {
		inputIDs[i] = fmt.Sprintf("%sinput-%05d", prefix, i+1)
	}
	return inputIDs
}

func seedTaskDiscoveryRunningExecutions(tb testing.TB, db *sql.DB, prefix string, count int) {
	tb.Helper()
	seedTaskDiscoveryWritePool(tb, db, prefix, models.CategoryActive, models.StatusQueued, "2020-01-01 00:00:00", "", count)
	if _, err := db.ExecContext(context.Background(), `
		WITH RECURSIVE seq(n) AS (
			SELECT 1
			UNION ALL
			SELECT n + 1 FROM seq WHERE n < ?
		)
		INSERT INTO executions (id, task_id, status, prompt_sent)
		SELECT ? || 'execution-' || printf('%05d', n), ? || printf('%05d', n), 'running', 'p'
		FROM seq`, count, prefix, prefix); err != nil {
		tb.Fatalf("seed task discovery running executions %q: %v", prefix, err)
	}
}

func seedTaskDiscoveryAlertPool(tb testing.TB, db *sql.DB, prefix string, count int) []string {
	tb.Helper()
	if count < 1 {
		count = 1
	}
	if _, err := db.ExecContext(context.Background(), `
		WITH RECURSIVE seq(n) AS (
			SELECT 1
			UNION ALL
			SELECT n + 1 FROM seq WHERE n < ?
		)
		INSERT INTO alerts
			(id, project_id, title, message, body, decision_state, processing_state, claimant, claimed_at, claim_expires_at, updated_at)
		SELECT
			? || printf('%05d', n), 'default', ? || printf('%05d', n), 'm', 'm',
			'approved', 'claimed', 'task-discovery-write-budget', '2020-01-01 00:00:00', '2099-01-01 00:00:00', '2020-01-01 00:00:00'
		FROM seq`, count, prefix, prefix); err != nil {
		tb.Fatalf("seed task discovery alerts %q: %v", prefix, err)
	}
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s%05d", prefix, i+1)
	}
	return ids
}

func cleanupTaskDiscoveryWritePool(tb testing.TB, db *sql.DB, prefix string) {
	tb.Helper()
	if _, err := db.ExecContext(context.Background(), `DELETE FROM tasks WHERE id LIKE ?`, prefix+"%"); err != nil {
		tb.Fatalf("clean task discovery write pool %q: %v", prefix, err)
	}
}

func cleanupTaskDiscoveryInputs(tb testing.TB, db *sql.DB, prefix string) {
	tb.Helper()
	if _, err := db.ExecContext(context.Background(), `DELETE FROM thread_inputs WHERE id LIKE ?`, prefix+"%"); err != nil {
		tb.Fatalf("clean task discovery inputs %q: %v", prefix, err)
	}
}

func taskDiscoveryWritePoolRun(tb testing.TB, db *sql.DB, prefix string, category models.TaskCategory, status models.TaskStatus, updatedAt, parentTaskID string, operation func(context.Context, string, int) error) taskDiscoveryWriteRun {
	tb.Helper()
	count := taskDiscoveryWriteOperationCount(tb)
	ids := seedTaskDiscoveryWritePool(tb, db, prefix, category, status, updatedAt, parentTaskID, count)
	return taskDiscoveryWriteRun{
		write: func(ctx context.Context, i int) error {
			return operation(ctx, ids[i%len(ids)], i)
		},
		cleanup: func(tb testing.TB) {
			cleanupTaskDiscoveryWritePool(tb, db, prefix)
		},
	}
}

func taskDiscoveryTargetWritePath(name string, operation func(context.Context, *repository.TaskRepo, string, int) error) taskDiscoveryWritePath {
	return taskDiscoveryWritePath{
		name:         name,
		affectedRows: 1,
		setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
			resetTaskDiscoveryWriteTarget(tb, db)
			targetID := taskDiscoveryWriteTarget(tb, taskRepo).ID
			return taskDiscoveryWriteRun{
				write: func(ctx context.Context, i int) error {
					return operation(ctx, taskRepo, targetID, i)
				},
			}
		},
	}
}

func taskDiscoveryBulkWritePath(name, prefix string, category models.TaskCategory, status models.TaskStatus, operation func(context.Context, *repository.TaskRepo) error) taskDiscoveryWritePath {
	return taskDiscoveryWritePath{
		name:         name,
		operations:   taskDiscoveryBulkWriteOperations,
		affectedRows: taskDiscoveryWritePoolSize,
		setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
			seedTaskDiscoveryWritePool(tb, db, prefix, category, status, "2020-01-01 00:00:00", "", taskDiscoveryWritePoolSize)
			return taskDiscoveryWriteRun{
				write: func(ctx context.Context, _ int) error {
					return operation(ctx, taskRepo)
				},
				cleanup: func(tb testing.TB) {
					cleanupTaskDiscoveryWritePool(tb, db, prefix)
				},
			}
		},
	}
}

type taskDiscoveryAutomationFixture struct {
	prefix        string
	automationID  string
	versionID     string
	triggerNodeID string
	taskIDs       []string
	schedule      models.Schedule
}

func seedTaskDiscoveryAutomationFixture(tb testing.TB, db *sql.DB, prefix string, category models.TaskCategory, status models.TaskStatus, count int) taskDiscoveryAutomationFixture {
	tb.Helper()
	ctx := context.Background()
	fixture := taskDiscoveryAutomationFixture{
		prefix:       prefix,
		automationID: prefix + "automation",
		taskIDs:      seedTaskDiscoveryWritePool(tb, db, prefix+"task-", category, status, "2020-01-01 00:00:00", "", count),
	}
	if len(fixture.taskIDs) == 0 {
		tb.Fatalf("task discovery Automation fixture has no tasks")
	}
	due := time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
	fixture.schedule = models.Schedule{
		TaskID:         fixture.taskIDs[0],
		RunAt:          due,
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
		NextRun:        &due,
	}
	if err := repository.NewScheduleRepo(db).Create(ctx, &fixture.schedule); err != nil {
		tb.Fatalf("create task discovery Automation schedule: %v", err)
	}
	definition, _, err := repository.NewAutomationRepo(db).PublishRegistered(ctx, models.AutomationRegisteredPublication{
		ProjectID:      "default",
		StableKey:      prefix + "stable",
		Name:           "Task discovery write-budget Automation " + prefix,
		AutomationType: "scheduled",
		AdapterKey:     "custom",
		CreatedVia:     "test",
		Nodes:          []models.AutomationNodeSpec{{Key: "trigger", Name: "Trigger", Type: models.AutomationNodeTrigger, Role: "trigger"}},
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "trigger", ResourceType: "schedule", ResourceID: fixture.schedule.ID, Relation: "owned"},
			{NodeKey: "trigger", ResourceType: "task", ResourceID: fixture.taskIDs[0], Relation: "owned"},
		},
	})
	if err != nil {
		tb.Fatalf("publish task discovery Automation fixture: %v", err)
	}
	if definition == nil || len(definition.Nodes) != 1 || definition.Version.ID == "" {
		tb.Fatalf("published task discovery Automation fixture is incomplete: %#v", definition)
	}
	fixture.versionID = definition.Version.ID
	fixture.automationID = definition.Automation.ID
	fixture.triggerNodeID = definition.Nodes[0].ID
	for _, taskID := range fixture.taskIDs[1:] {
		if _, err := db.ExecContext(ctx, `INSERT INTO automation_definition_resources
			(project_id, automation_id, version_id, node_id, resource_type, resource_id, relation)
			VALUES ('default', ?, ?, ?, 'task', ?, 'owned')`, fixture.automationID, fixture.versionID, fixture.triggerNodeID, taskID); err != nil {
			tb.Fatalf("bind task discovery Automation task %q: %v", taskID, err)
		}
	}
	return fixture
}

func cleanupTaskDiscoveryAutomationFixture(tb testing.TB, db *sql.DB, fixture taskDiscoveryAutomationFixture) {
	tb.Helper()
	ctx := context.Background()
	if fixture.schedule.ID != "" {
		if _, err := db.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?`, fixture.schedule.ID); err != nil {
			tb.Fatalf("clean task discovery Automation schedule: %v", err)
		}
	}
	if fixture.automationID != "" {
		if _, err := db.ExecContext(ctx, `DELETE FROM automations WHERE id = ? AND project_id = 'default'`, fixture.automationID); err != nil {
			tb.Fatalf("clean task discovery Automation %q: %v", fixture.automationID, err)
		}
	}
	cleanupTaskDiscoveryWritePool(tb, db, fixture.prefix+"task-")
}

func cleanupTaskDiscoveryAutomationByPrefix(tb testing.TB, db *sql.DB, prefix, automationID string) {
	tb.Helper()
	ctx := context.Background()
	if automationID != "" {
		if _, err := db.ExecContext(ctx, `DELETE FROM automations WHERE id = ? AND project_id = 'default'`, automationID); err != nil {
			tb.Fatalf("clean task discovery Automation %q: %v", automationID, err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM tasks WHERE project_id = 'default' AND title LIKE ?`, prefix+"%"); err != nil {
		tb.Fatalf("clean task discovery Automation tasks %q: %v", prefix, err)
	}
}

func prepareTaskDiscoveryAutomationDispatch(tb testing.TB, db *sql.DB, prefix string) (taskDiscoveryAutomationFixture, models.AutomationDispatch) {
	tb.Helper()
	ctx := context.Background()
	fixture := seedTaskDiscoveryAutomationFixture(tb, db, prefix, models.CategoryScheduled, models.StatusPending, 1)
	automationRepo := repository.NewAutomationRepo(db)
	invocations, dispatches, err := automationRepo.ClaimManualAutomationRun(ctx, "default", fixture.automationID, time.Date(2020, time.January, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
		tb.Fatalf("seed task discovery Automation dispatch: %v", err)
	}
	if len(invocations) != 1 || len(dispatches) != 1 {
		cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
		tb.Fatalf("seed task discovery Automation dispatch returned invocations=%d dispatches=%d", len(invocations), len(dispatches))
	}
	return fixture, dispatches[0]
}

func leaseTaskDiscoveryAutomationDispatch(tb testing.TB, db *sql.DB, dispatch models.AutomationDispatch, claimant string) models.AutomationDispatch {
	tb.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `UPDATE automation_dispatch_outbox
		SET status = 'processing', claimed_by = ?, claim_expires_at = ?
		WHERE id = ? AND status = 'pending'`, claimant, time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC), dispatch.ID); err != nil {
		tb.Fatalf("lease task discovery Automation dispatch %q: %v", dispatch.ID, err)
	}
	dispatch.Status = "processing"
	dispatch.ClaimedBy = claimant
	return dispatch
}

func taskDiscoveryAutomationCandidate(name string) models.AutomationDraftCandidate {
	return models.AutomationDraftCandidate{
		SchemaVersion:  1,
		Name:           name,
		AutomationType: "custom",
		AdapterKey:     "custom",
		Nodes: []models.AutomationDraftNode{{
			Key: "task", Name: "Task", Type: models.AutomationNodeAgentTask, Role: "task",
			Config: map[string]any{"category": string(models.CategoryActive)},
		}},
	}
}

type taskDiscoveryAutomationChainFixture struct {
	automationID string
	prefix       string
	parent       models.Task
	child        models.Task
	event        repository.AutomationProjectionEvent
}

func seedTaskDiscoveryAutomationChainFixture(tb testing.TB, db *sql.DB, prefix string) taskDiscoveryAutomationChainFixture {
	tb.Helper()
	ctx := context.Background()
	automationRepo := repository.NewAutomationRepo(db)
	candidate := models.AutomationDraftCandidate{
		SchemaVersion:  1,
		Name:           "Task discovery chained Automation",
		AutomationType: "custom",
		AdapterKey:     "custom",
		Nodes: []models.AutomationDraftNode{
			{Key: "parent", Name: "Parent", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"category": string(models.CategoryActive)}},
			{Key: "child", Name: "Child", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"category": string(models.CategoryActive)}},
		},
		Edges: []models.AutomationDraftEdge{{Key: "parent-child", From: "parent", To: "child"}},
	}
	automationID := prefix + "automation"
	definition, _, err := automationRepo.SaveCurrentGraph(ctx, repository.AutomationSaveWrite{
		ProjectID: "default", AutomationID: automationID, GraphID: prefix + "graph", Candidate: candidate,
		Tasks: []repository.AutomationSaveTask{
			{NodeKey: "parent", Title: prefix + "parent", Prompt: "parent", Category: models.CategoryActive, Priority: 2, ApplyTopology: true,
				ChildNodeKey: "child", ChildTitle: prefix + "child", ChildPromptPrefix: "child: ", ChildCategory: models.CategoryActive},
			{NodeKey: "child", Title: prefix + "child", Prompt: "child", Category: models.CategoryActive, Priority: 2, ApplyTopology: true, ParentNodeKey: "parent"},
		},
	})
	if err != nil {
		tb.Fatalf("seed task discovery Automation chain: %v", err)
	}
	if definition == nil || len(definition.Nodes) != 2 || len(definition.Edges) != 1 {
		tb.Fatalf("task discovery Automation chain definition is incomplete: %#v", definition)
	}
	nodeKeys := make(map[string]string, len(definition.Nodes))
	for _, node := range definition.Nodes {
		nodeKeys[node.NodeKey] = node.ID
	}
	taskIDs := make(map[string]string, 2)
	for _, resource := range definition.Resources {
		if resource.ResourceType == "task" {
			for key, nodeID := range nodeKeys {
				if resource.NodeID == nodeID {
					taskIDs[key] = resource.ResourceID
				}
			}
		}
	}
	if taskIDs["parent"] == "" || taskIDs["child"] == "" {
		tb.Fatalf("task discovery Automation chain task resources are incomplete: %#v", definition.Resources)
	}
	parent, err := repository.NewTaskRepo(db, nil).GetByID(ctx, taskIDs["parent"])
	if err != nil || parent == nil {
		tb.Fatalf("load task discovery Automation chain parent: %v", err)
	}
	child, err := repository.NewTaskRepo(db, nil).GetByID(ctx, taskIDs["child"])
	if err != nil || child == nil {
		tb.Fatalf("load task discovery Automation chain child: %v", err)
	}
	return taskDiscoveryAutomationChainFixture{
		automationID: definition.Automation.ID,
		prefix:       prefix,
		parent:       *parent,
		child:        *child,
		event: repository.AutomationProjectionEvent{
			Context:        models.AutomationContext{ProjectID: "default"},
			Binding:        models.AutomationBinding{AutomationID: definition.Automation.ID, VersionID: definition.Version.ID, NodeID: nodeKeys["child"]},
			WorkItemKey:    prefix + "work",
			WorkItemKind:   "task",
			WorkItemTitle:  prefix + "work",
			WorkItemStatus: models.AutomationWorkItemActive,
			ActivityKey:    prefix + "activity",
			ActivityType:   "task",
			ActivityStatus: models.AutomationActivityRunning,
			Resources:      []models.AutomationActivityResource{{ResourceType: "task", ResourceID: child.ID, Relation: "child"}},
			EventKey:       prefix + "event",
			FromNodeID:     nodeKeys["parent"],
			ToNodeID:       nodeKeys["child"],
			EdgeID:         definition.Edges[0].ID,
			Transition:     models.AutomationTransitionEntered,
			MetadataJSON:   "{}",
		},
	}
}

func cleanupTaskDiscoveryAutomationChainFixture(tb testing.TB, db *sql.DB, fixture taskDiscoveryAutomationChainFixture) {
	tb.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM automations WHERE id = ? AND project_id = 'default'`, fixture.automationID); err != nil {
		tb.Fatalf("clean task discovery Automation chain %q: %v", fixture.automationID, err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM tasks WHERE project_id = 'default' AND title LIKE ?`, fixture.prefix+"%"); err != nil {
		tb.Fatalf("clean task discovery Automation chain tasks %q: %v", fixture.prefix, err)
	}
}

func seedTaskDiscoveryAutomationResumeFixture(tb testing.TB, db *sql.DB, prefix string) taskDiscoveryAutomationFixture {
	tb.Helper()
	ctx := context.Background()
	fixture := seedTaskDiscoveryAutomationFixture(tb, db, prefix, models.CategoryBacklog, models.StatusPending, 1)
	candidate := models.AutomationDraftCandidate{
		SchemaVersion:  1,
		Name:           "Task discovery resumable Automation",
		AutomationType: "custom",
		AdapterKey:     "custom",
		Nodes: []models.AutomationDraftNode{{
			Key: "trigger", Name: "Task", Type: models.AutomationNodeAgentTask, Role: "task",
			Config: map[string]any{"category": string(models.CategoryActive)},
		}},
	}
	candidateJSON, err := json.Marshal(candidate)
	if err != nil {
		cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
		tb.Fatalf("encode task discovery Automation resume candidate: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_graph_metadata
		(version_id, project_id, automation_id, candidate_json)
		VALUES (?, 'default', ?, ?)`, fixture.versionID, fixture.automationID, string(candidateJSON)); err != nil {
		cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
		tb.Fatalf("seed task discovery Automation resume metadata: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE automations SET lifecycle_state = 'paused' WHERE id = ? AND project_id = 'default'`, fixture.automationID); err != nil {
		cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
		tb.Fatalf("pause task discovery Automation resume fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE schedules SET enabled = 0 WHERE id = ?`, fixture.schedule.ID); err != nil {
		cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
		tb.Fatalf("disable task discovery Automation resume schedule: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE automation_trigger_owners SET ownership_state = 'paused' WHERE schedule_id = ?`, fixture.schedule.ID); err != nil {
		cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
		tb.Fatalf("pause task discovery Automation resume owner: %v", err)
	}
	return fixture
}

func taskDiscoveryWritePaths() []taskDiscoveryWritePath {
	return []taskDiscoveryWritePath{
		{
			name: "Create",
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, i int) error {
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
					},
					cleanup: func(tb testing.TB) {
						tb.Helper()
						if _, err := db.ExecContext(context.Background(), `DELETE FROM tasks WHERE project_id = ? AND title LIKE ?`, "default", taskDiscoveryCreateTitlePrefix+"%"); err != nil {
							tb.Fatalf("clean task discovery create fixtures: %v", err)
						}
					},
				}
			},
		},
		{
			name: "Update",
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				resetTaskDiscoveryWriteTarget(tb, db)
				task := taskDiscoveryWriteTarget(tb, taskRepo)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, i int) error {
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
					},
				}
			},
		},
		taskDiscoveryTargetWritePath("UpdateCategory", func(ctx context.Context, taskRepo *repository.TaskRepo, id string, i int) error {
			var category models.TaskCategory
			switch i % 3 {
			case 0:
				category = models.CategoryActive
			case 1:
				category = models.CategoryBacklog
			default:
				category = models.CategoryChat
			}
			return taskRepo.UpdateCategory(ctx, id, category)
		}),
		taskDiscoveryWritePath{
			name:         "ReorderTask",
			affectedRows: 2,
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				resetTaskDiscoveryWriteTarget(tb, db)
				ctx := context.Background()
				if _, err := db.ExecContext(ctx, `UPDATE tasks SET display_order = ? WHERE project_id = ?`, taskDiscoveryReorderOtherOrder, "default"); err != nil {
					tb.Fatalf("set task discovery reorder filler order: %v", err)
				}
				if _, err := db.ExecContext(ctx, `UPDATE tasks SET display_order = 0 WHERE id = ?`, taskDiscoveryWriteTargetID); err != nil {
					tb.Fatalf("set task discovery reorder target order: %v", err)
				}
				if _, err := db.ExecContext(ctx, `UPDATE tasks SET display_order = 1 WHERE id = ?`, taskDiscoveryWriteNeighborID); err != nil {
					tb.Fatalf("set task discovery reorder neighbor order: %v", err)
				}
				targetID := taskDiscoveryWriteTarget(tb, taskRepo).ID
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, i int) error {
						newPosition := 0
						if i%2 == 0 {
							newPosition = 1
						}
						return taskRepo.ReorderTask(ctx, targetID, newPosition)
					},
				}
			},
		},
		taskDiscoveryTargetWritePath("UpdateStatus", func(ctx context.Context, taskRepo *repository.TaskRepo, id string, i int) error {
			status := models.StatusPending
			if i%2 == 1 {
				status = models.StatusRunning
			}
			return taskRepo.UpdateStatus(ctx, id, status)
		}),
		taskDiscoveryTargetWritePath("UpdateSwarmFields", func(ctx context.Context, taskRepo *repository.TaskRepo, id string, i int) error {
			role := models.SwarmRoleNone
			status := "idle"
			if i%2 == 1 {
				role = models.SwarmRoleWorker
				status = "running"
			}
			return taskRepo.UpdateSwarmFields(ctx, id, role, status, fmt.Sprintf(`{"phase":%d}`, i%2), i%2)
		}),
		taskDiscoveryTargetWritePath("UpdateAgentID", func(ctx context.Context, taskRepo *repository.TaskRepo, id string, _ int) error {
			return taskRepo.UpdateAgentID(ctx, id, "")
		}),
		taskDiscoveryTargetWritePath("UpdateWorktreeInfo", func(ctx context.Context, taskRepo *repository.TaskRepo, id string, i int) error {
			return taskRepo.UpdateWorktreeInfo(ctx, id, fmt.Sprintf("/tmp/discovery-worktree-%d", i%2), fmt.Sprintf("task/discovery-%d", i%2))
		}),
		taskDiscoveryTargetWritePath("UpdateMergeStatus", func(ctx context.Context, taskRepo *repository.TaskRepo, id string, i int) error {
			status := models.MergeStatusPending
			if i%2 == 1 {
				status = models.MergeStatusFailed
			}
			return taskRepo.UpdateMergeStatus(ctx, id, status)
		}),
		taskDiscoveryTargetWritePath("UpdateAutoMerge", func(ctx context.Context, taskRepo *repository.TaskRepo, id string, i int) error {
			return taskRepo.UpdateAutoMerge(ctx, id, i%2 == 1, "main")
		}),
		taskDiscoveryTargetWritePath("UpdateLineage", func(ctx context.Context, taskRepo *repository.TaskRepo, id string, i int) error {
			return taskRepo.UpdateLineage(ctx, id, "main", strings.Repeat(fmt.Sprintf("%x", i%16), 40), i%2)
		}),
		taskDiscoveryTargetWritePath("UpdateTelegramOrigin", func(ctx context.Context, taskRepo *repository.TaskRepo, id string, i int) error {
			return taskRepo.UpdateTelegramOrigin(ctx, id, int64(i%2))
		}),
		taskDiscoveryTargetWritePath("UpdateSlackOrigin", func(ctx context.Context, taskRepo *repository.TaskRepo, id string, _ int) error {
			return taskRepo.UpdateSlackOrigin(ctx, id)
		}),
		taskDiscoveryTargetWritePath("UpdateEmailOrigin", func(ctx context.Context, taskRepo *repository.TaskRepo, id string, _ int) error {
			return taskRepo.UpdateEmailOrigin(ctx, id)
		}),
		taskDiscoveryTargetWritePath("UpdateDiscordOrigin", func(ctx context.Context, taskRepo *repository.TaskRepo, id string, _ int) error {
			return taskRepo.UpdateDiscordOrigin(ctx, id)
		}),
		taskDiscoveryTargetWritePath("UpdateXOrigin", func(ctx context.Context, taskRepo *repository.TaskRepo, id string, _ int) error {
			return taskRepo.UpdateXOrigin(ctx, id)
		}),
		taskDiscoveryTargetWritePath("UpdateAgentDefinition", func(ctx context.Context, taskRepo *repository.TaskRepo, id string, _ int) error {
			return taskRepo.UpdateAgentDefinition(ctx, id, nil)
		}),
		taskDiscoveryTargetWritePath("ClearWorktreeInfo", func(ctx context.Context, taskRepo *repository.TaskRepo, id string, _ int) error {
			return taskRepo.ClearWorktreeInfo(ctx, id)
		}),
		taskDiscoveryTargetWritePath("SetPendingIfNotRunningOrQueued", func(ctx context.Context, taskRepo *repository.TaskRepo, id string, _ int) error {
			_, err := taskRepo.SetPendingIfNotRunningOrQueued(ctx, id)
			return err
		}),
		{
			name: "SetPendingIfNotRunningOrQueuedForEnabledSchedule",
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				resetTaskDiscoveryWriteTarget(tb, db)
				ctx := context.Background()
				if _, err := db.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?`, taskDiscoveryWriteScheduleID); err != nil {
					tb.Fatalf("remove task discovery write schedule: %v", err)
				}
				if _, err := db.ExecContext(ctx, `INSERT INTO schedules (id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run)
					VALUES (?, ?, '2020-01-01 00:00:00', 'daily', 1, 1, '2020-01-01 00:00:00')`, taskDiscoveryWriteScheduleID, taskDiscoveryWriteTargetID); err != nil {
					tb.Fatalf("create task discovery write schedule: %v", err)
				}
				targetID := taskDiscoveryWriteTarget(tb, taskRepo).ID
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						_, err := taskRepo.SetPendingIfNotRunningOrQueuedForEnabledSchedule(ctx, targetID, taskDiscoveryWriteScheduleID)
						return err
					},
					cleanup: func(tb testing.TB) {
						tb.Helper()
						if _, err := db.ExecContext(context.Background(), `DELETE FROM schedules WHERE id = ?`, taskDiscoveryWriteScheduleID); err != nil {
							tb.Fatalf("clean task discovery write schedule: %v", err)
						}
					},
				}
			},
		},
		{
			name:         "AutomationSaveCurrentGraph",
			operations:   1,
			affectedRows: 2,
			setup: func(tb testing.TB, db *sql.DB, _ *repository.TaskRepo) taskDiscoveryWriteRun {
				prefix := taskDiscoveryWritePoolPrefix + "automation-save-"
				automationID := prefix + "automation"
				candidate := taskDiscoveryAutomationCandidate("Task discovery saved graph")
				automationRepo := repository.NewAutomationRepo(db)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						_, _, err := automationRepo.SaveCurrentGraph(ctx, repository.AutomationSaveWrite{
							ProjectID: "default", AutomationID: automationID, GraphID: prefix + "graph", Candidate: candidate,
							Tasks: []repository.AutomationSaveTask{{
								NodeKey: "task", Title: prefix + "task", Prompt: "p", Category: models.CategoryActive, Priority: 2,
							}},
						})
						return err
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryAutomationByPrefix(tb, db, prefix, automationID)
					},
				}
			},
		},
		{
			name:         "AutomationResume",
			operations:   1,
			affectedRows: 1,
			setup: func(tb testing.TB, db *sql.DB, _ *repository.TaskRepo) taskDiscoveryWriteRun {
				prefix := taskDiscoveryWritePoolPrefix + "automation-resume-"
				fixture := seedTaskDiscoveryAutomationResumeFixture(tb, db, prefix)
				automationRepo := repository.NewAutomationRepo(db)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						_, err := automationRepo.ResumeAutomation(ctx, "default", fixture.automationID)
						return err
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					},
				}
			},
		},
		{
			name: "AutomationSetLifecyclePaused", operations: 1,
			affectedRows: taskDiscoveryWritePoolSize,
			setup: func(tb testing.TB, db *sql.DB, _ *repository.TaskRepo) taskDiscoveryWriteRun {
				prefix := taskDiscoveryWritePoolPrefix + "automation-lifecycle-"
				fixture := seedTaskDiscoveryAutomationFixture(tb, db, prefix, models.CategoryActive, models.StatusPending, taskDiscoveryWritePoolSize)
				automationRepo := repository.NewAutomationRepo(db)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						return automationRepo.SetAutomationLifecycle(ctx, "default", fixture.automationID, models.AutomationPaused)
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					},
				}
			},
		},
		{
			name:         "AutomationClaimManualRun",
			operations:   1,
			affectedRows: 1,
			setup: func(tb testing.TB, db *sql.DB, _ *repository.TaskRepo) taskDiscoveryWriteRun {
				prefix := taskDiscoveryWritePoolPrefix + "automation-manual-"
				fixture := seedTaskDiscoveryAutomationFixture(tb, db, prefix, models.CategoryScheduled, models.StatusPending, 1)
				automationRepo := repository.NewAutomationRepo(db)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						_, _, err := automationRepo.ClaimManualAutomationRun(ctx, "default", fixture.automationID, time.Date(2020, time.January, 2, 0, 0, 0, 0, time.UTC))
						return err
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					},
				}
			},
		},
		{
			name:         "AutomationClaimScheduledOccurrence",
			operations:   1,
			affectedRows: 1,
			setup: func(tb testing.TB, db *sql.DB, _ *repository.TaskRepo) taskDiscoveryWriteRun {
				prefix := taskDiscoveryWritePoolPrefix + "automation-scheduled-"
				fixture := seedTaskDiscoveryAutomationFixture(tb, db, prefix, models.CategoryScheduled, models.StatusPending, 1)
				automationRepo := repository.NewAutomationRepo(db)
				nextRun := fixture.schedule.RunAt.Add(24 * time.Hour)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						_, _, err := automationRepo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Date(2020, time.January, 2, 0, 0, 0, 0, time.UTC), &nextRun)
						return err
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					},
				}
			},
		},
		{
			name:         "AutomationClaimDispatch",
			operations:   1,
			affectedRows: 1,
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				prefix := taskDiscoveryWritePoolPrefix + "automation-claim-dispatch-"
				fixture, dispatch := prepareTaskDiscoveryAutomationDispatch(tb, db, prefix)
				claimant := "task-discovery-claim"
				leaseTaskDiscoveryAutomationDispatch(tb, db, dispatch, claimant)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						_, err := taskRepo.ClaimAutomationDispatch(ctx, dispatch.ID, claimant)
						return err
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					},
				}
			},
		},
		{
			name:         "AutomationClaimQueuedDispatch",
			operations:   1,
			affectedRows: 1,
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				prefix := taskDiscoveryWritePoolPrefix + "automation-queued-dispatch-"
				fixture, dispatch := prepareTaskDiscoveryAutomationDispatch(tb, db, prefix)
				automationRepo := repository.NewAutomationRepo(db)
				now := time.Date(2020, time.January, 2, 0, 0, 0, 0, time.UTC)
				if _, err := db.ExecContext(context.Background(), `UPDATE automation_dispatch_outbox
						SET next_attempt_at = ? WHERE id = ?`, now, dispatch.ID); err != nil {
					cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					tb.Fatalf("prepare task discovery queued Automation dispatch: %v", err)
				}
				leased, err := automationRepo.LeaseNextDispatch(context.Background(), "task-discovery-queued", now, time.Minute)
				if err != nil {
					cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					tb.Fatalf("lease task discovery queued Automation dispatch: %v", err)
				}
				if leased == nil || leased.ID != dispatch.ID {
					cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					tb.Fatalf("leased task discovery queued Automation dispatch = %#v, want %q", leased, dispatch.ID)
				}
				if err := automationRepo.MarkDispatchQueued(context.Background(), dispatch.ID, "task-discovery-queued"); err != nil {
					cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					tb.Fatalf("queue task discovery Automation dispatch: %v", err)
				}
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						_, err := taskRepo.ClaimQueuedAutomationDispatch(ctx, dispatch.ID)
						return err
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					},
				}
			},
		},
		{
			name: "AutomationCancelDispatch", operations: 1,
			affectedRows: 1,
			setup: func(tb testing.TB, db *sql.DB, _ *repository.TaskRepo) taskDiscoveryWriteRun {
				prefix := taskDiscoveryWritePoolPrefix + "automation-cancel-dispatch-"
				fixture, dispatch := prepareTaskDiscoveryAutomationDispatch(tb, db, prefix)
				if _, err := db.ExecContext(context.Background(), `UPDATE tasks SET category = 'active', status = 'running' WHERE id = ?`, dispatch.TaskID); err != nil {
					tb.Fatalf("prepare task discovery Automation cancellation task: %v", err)
				}
				if _, err := db.ExecContext(context.Background(), `INSERT INTO executions (id, task_id, status, prompt_sent, dispatch_id)
						VALUES (?, ?, 'running', 'p', ?)`, prefix+"execution", dispatch.TaskID, dispatch.ID); err != nil {
					tb.Fatalf("prepare task discovery Automation cancellation execution: %v", err)
				}
				if _, err := db.ExecContext(context.Background(), `UPDATE automation_dispatch_outbox
						SET status = 'processing', execution_id = ?, claimed_by = 'task-discovery-cancel', claim_expires_at = ?
						WHERE id = ?`, prefix+"execution", time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC), dispatch.ID); err != nil {
					tb.Fatalf("prepare task discovery Automation cancellation dispatch: %v", err)
				}
				automationRepo := repository.NewAutomationRepo(db)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						return automationRepo.CancelDispatchesForTask(ctx, dispatch.TaskID, "task discovery write budget")
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					},
				}
			},
		},
		{
			name:         "AutomationCancelPendingDispatch",
			operations:   1,
			affectedRows: 1,
			setup: func(tb testing.TB, db *sql.DB, _ *repository.TaskRepo) taskDiscoveryWriteRun {
				prefix := taskDiscoveryWritePoolPrefix + "automation-cancel-pending-"
				fixture, dispatch := prepareTaskDiscoveryAutomationDispatch(tb, db, prefix)
				if _, err := db.ExecContext(context.Background(), `UPDATE tasks
						SET category = 'active', status = 'failed' WHERE id = ?`, dispatch.TaskID); err != nil {
					cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					tb.Fatalf("prepare task discovery pending Automation cancellation task: %v", err)
				}
				automationRepo := repository.NewAutomationRepo(db)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						return automationRepo.CancelDispatchesForTask(ctx, dispatch.TaskID, "task discovery write budget")
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					},
				}
			},
		},
		{
			name: "AutomationFailDispatch", operations: 1,
			affectedRows: 1,
			setup: func(tb testing.TB, db *sql.DB, _ *repository.TaskRepo) taskDiscoveryWriteRun {
				prefix := taskDiscoveryWritePoolPrefix + "automation-fail-dispatch-"
				fixture, dispatch := prepareTaskDiscoveryAutomationDispatch(tb, db, prefix)
				claimant := "task-discovery-fail"
				leaseTaskDiscoveryAutomationDispatch(tb, db, dispatch, claimant)
				if _, err := db.ExecContext(context.Background(), `UPDATE automation_dispatch_outbox SET attempts = 1 WHERE id = ?`, dispatch.ID); err != nil {
					tb.Fatalf("prepare task discovery Automation failure attempts: %v", err)
				}
				automationRepo := repository.NewAutomationRepo(db)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						return automationRepo.FailDispatch(ctx, dispatch.ID, claimant, "task discovery write budget", 1, time.Date(2020, time.January, 2, 0, 0, 0, 0, time.UTC))
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					},
				}
			},
		},
		{
			name:         "AutomationCompleteDispatch",
			operations:   1,
			affectedRows: 1,
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				prefix := taskDiscoveryWritePoolPrefix + "automation-complete-dispatch-"
				fixture, dispatch := prepareTaskDiscoveryAutomationDispatch(tb, db, prefix)
				claimant := "task-discovery-complete"
				leaseTaskDiscoveryAutomationDispatch(tb, db, dispatch, claimant)
				execution, err := taskRepo.ClaimAutomationDispatch(context.Background(), dispatch.ID, claimant)
				if err != nil {
					cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					tb.Fatalf("prepare task discovery Automation completion execution: %v", err)
				}
				automationRepo := repository.NewAutomationRepo(db)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						return automationRepo.CompleteDispatch(ctx, dispatch.ID, execution.ID, models.ExecCompleted, "")
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					},
				}
			},
		},
		{
			name:         "AutomationCompleteFailedDispatch",
			operations:   1,
			affectedRows: 1,
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				prefix := taskDiscoveryWritePoolPrefix + "automation-complete-failed-"
				fixture, dispatch := prepareTaskDiscoveryAutomationDispatch(tb, db, prefix)
				claimant := "task-discovery-complete-failed"
				leaseTaskDiscoveryAutomationDispatch(tb, db, dispatch, claimant)
				execution, err := taskRepo.ClaimAutomationDispatch(context.Background(), dispatch.ID, claimant)
				if err != nil {
					cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					tb.Fatalf("prepare task discovery failed Automation completion execution: %v", err)
				}
				automationRepo := repository.NewAutomationRepo(db)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						return automationRepo.CompleteDispatch(ctx, dispatch.ID, execution.ID, models.ExecFailed, "task discovery write budget")
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryAutomationFixture(tb, db, fixture)
					},
				}
			},
		},
		{
			name: "AutomationActivateChainedTask", operations: 1,
			affectedRows: 1,
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				prefix := taskDiscoveryWritePoolPrefix + "automation-chain-"
				fixture := seedTaskDiscoveryAutomationChainFixture(tb, db, prefix)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						_, _, _, err := taskRepo.ActivateAutomationChainedTask(ctx, fixture.parent, &fixture.child, fixture.event)
						return err
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryAutomationChainFixture(tb, db, fixture)
					},
				}
			},
		},
		{
			name: "ClaimTask",
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				return taskDiscoveryWritePoolRun(tb, db, taskDiscoveryWritePoolPrefix+"claim-task-", models.CategoryActive, models.StatusPending, "2020-01-01 00:00:00", "", func(ctx context.Context, id string, _ int) error {
					_, err := taskRepo.ClaimTask(ctx, id)
					return err
				})
			},
		},
		{
			name: "ClaimTaskForDispatch",
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				return taskDiscoveryWritePoolRun(tb, db, taskDiscoveryWritePoolPrefix+"claim-dispatch-", models.CategoryActive, models.StatusPending, "2020-01-01 00:00:00", "", func(ctx context.Context, id string, _ int) error {
					_, _, err := taskRepo.ClaimTaskForDispatch(ctx, id)
					return err
				})
			},
		},
		{
			name: "ReclaimStaleQueuedTask",
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				return taskDiscoveryWritePoolRun(tb, db, taskDiscoveryWritePoolPrefix+"reclaim-", models.CategoryActive, models.StatusQueued, "2020-01-01 00:00:00", "", func(ctx context.Context, id string, _ int) error {
					_, err := taskRepo.ReclaimStaleQueuedTask(ctx, id, time.Hour)
					return err
				})
			},
		},
		{
			name: "ExecutionCreateDirectTaskFollowup",
			setup: func(tb testing.TB, db *sql.DB, _ *repository.TaskRepo) taskDiscoveryWriteRun {
				executionRepo := repository.NewExecutionRepo(db)
				return taskDiscoveryWritePoolRun(tb, db, taskDiscoveryWritePoolPrefix+"execution-followup-", models.CategoryActive, models.StatusCompleted, "2020-01-01 00:00:00", "", func(ctx context.Context, id string, _ int) error {
					_, err := executionRepo.CreateDirectTaskFollowupOrQueue(ctx, &models.Execution{TaskID: id, PromptSent: "p"}, &models.ThreadInput{})
					return err
				})
			},
		},
		{
			name: "ThreadInputClaimQueuedForTaskExecution",
			setup: func(tb testing.TB, db *sql.DB, _ *repository.TaskRepo) taskDiscoveryWriteRun {
				prefix := taskDiscoveryWritePoolPrefix + "thread-task-"
				count := taskDiscoveryWriteOperationCount(tb)
				ids := seedTaskDiscoveryWritePool(tb, db, prefix, models.CategoryActive, models.StatusCompleted, "2020-01-01 00:00:00", "", count)
				inputIDs := seedTaskDiscoveryQueuedInputs(tb, db, prefix, count)
				agentConfigID := taskDiscoveryDefaultAgentConfigID(tb, db)
				threadRepo := repository.NewThreadInputRepo(db)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, i int) error {
						exec := &models.Execution{TaskID: ids[i%len(ids)], AgentConfigID: agentConfigID, PromptSent: "p"}
						return threadRepo.ClaimQueuedForTaskExecution(ctx, inputIDs[i%len(inputIDs)], exec)
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryWritePool(tb, db, prefix)
						cleanupTaskDiscoveryInputs(tb, db, prefix)
					},
				}
			},
		},
		{
			name: "ThreadInputClaimQueuedForChatExecution",
			setup: func(tb testing.TB, db *sql.DB, _ *repository.TaskRepo) taskDiscoveryWriteRun {
				prefix := taskDiscoveryWritePoolPrefix + "thread-chat-"
				count := taskDiscoveryWriteOperationCount(tb)
				inputIDs := seedTaskDiscoveryChatInputs(tb, db, prefix, count)
				agentConfigID := taskDiscoveryDefaultAgentConfigID(tb, db)
				threadRepo := repository.NewThreadInputRepo(db)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, i int) error {
						task := &models.Task{
							ProjectID: "default",
							Title:     fmt.Sprintf("%s%06d", "Discovery write-budget chat task ", i),
							Category:  models.CategoryChat,
							Status:    models.StatusRunning,
							Prompt:    "p",
							AgentID:   &agentConfigID,
						}
						exec := &models.Execution{AgentConfigID: agentConfigID, Status: models.ExecRunning, PromptSent: "p"}
						return threadRepo.ClaimQueuedForChatExecution(ctx, inputIDs[i%len(inputIDs)], task, exec, nil, nil, nil)
					},
					cleanup: func(tb testing.TB) {
						tb.Helper()
						if _, err := db.ExecContext(context.Background(), `DELETE FROM tasks WHERE project_id = ? AND title LIKE ?`, "default", "Discovery write-budget chat task %"); err != nil {
							tb.Fatalf("clean task discovery chat tasks: %v", err)
						}
						cleanupTaskDiscoveryInputs(tb, db, prefix)
					},
				}
			},
		},
		{
			name: "AlertCreateImplementationTask",
			setup: func(tb testing.TB, db *sql.DB, _ *repository.TaskRepo) taskDiscoveryWriteRun {
				count := taskDiscoveryWriteOperationCount(tb)
				alertIDs := seedTaskDiscoveryAlertPool(tb, db, taskDiscoveryWriteAlertPrefix, count)
				alertRepo := repository.NewAlertRepo(db)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, i int) error {
						_, err := alertRepo.CreateImplementationTask(ctx, "default", alertIDs[i%len(alertIDs)], "task-discovery-write-budget", models.AlertImplementationTaskInput{
							Title:    fmt.Sprintf("%s%06d", taskDiscoveryWriteAlertTaskPrefix, i),
							Prompt:   "p",
							Priority: 2,
						})
						return err
					},
					cleanup: func(tb testing.TB) {
						tb.Helper()
						if _, err := db.ExecContext(context.Background(), `DELETE FROM tasks WHERE project_id = ? AND title LIKE ?`, "default", taskDiscoveryWriteAlertTaskPrefix+"%"); err != nil {
							tb.Fatalf("clean task discovery alert tasks: %v", err)
						}
						if _, err := db.ExecContext(context.Background(), `DELETE FROM alerts WHERE id LIKE ?`, taskDiscoveryWriteAlertPrefix+"%"); err != nil {
							tb.Fatalf("clean task discovery alerts: %v", err)
						}
					},
				}
			},
		},
		taskDiscoveryBulkWritePath("ResetOrphanedRunning", taskDiscoveryWritePoolPrefix+"reset-running-", models.CategoryActive, models.StatusRunning, func(ctx context.Context, taskRepo *repository.TaskRepo) error {
			_, err := taskRepo.ResetOrphanedRunning(ctx)
			return err
		}),
		taskDiscoveryBulkWritePath("MoveCompletedActiveToCompleted", taskDiscoveryWritePoolPrefix+"move-completed-", models.CategoryActive, models.StatusCompleted, func(ctx context.Context, taskRepo *repository.TaskRepo) error {
			_, err := taskRepo.MoveCompletedActiveToCompleted(ctx)
			return err
		}),
		taskDiscoveryBulkWritePath("ActivateAllBacklog", taskDiscoveryWritePoolPrefix+"activate-backlog-", models.CategoryBacklog, models.StatusPending, func(ctx context.Context, taskRepo *repository.TaskRepo) error {
			_, err := taskRepo.ActivateAllBacklog(ctx, "default")
			return err
		}),
		{
			name:         "ExecutionRecoverPreRestartRunningTaskExecutions",
			operations:   taskDiscoveryBulkWriteOperations,
			affectedRows: taskDiscoveryWritePoolSize,
			setup: func(tb testing.TB, db *sql.DB, _ *repository.TaskRepo) taskDiscoveryWriteRun {
				prefix := taskDiscoveryWritePoolPrefix + "execution-recovery-"
				seedTaskDiscoveryRunningExecutions(tb, db, prefix, taskDiscoveryWritePoolSize)
				executionRepo := repository.NewExecutionRepo(db)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						_, err := executionRepo.RecoverPreRestartRunningTaskExecutions(ctx)
						return err
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryWritePool(tb, db, prefix)
					},
				}
			},
		},
		{
			name: "Delete",
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				return taskDiscoveryWritePoolRun(tb, db, taskDiscoveryWritePoolPrefix+"delete-", models.CategoryActive, models.StatusPending, "2020-01-01 00:00:00", "", func(ctx context.Context, id string, _ int) error {
					return taskRepo.Delete(ctx, id)
				})
			},
		},
		{
			name: "DeleteWithCleanupManifest",
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				return taskDiscoveryWritePoolRun(tb, db, taskDiscoveryWritePoolPrefix+"delete-manifest-", models.CategoryActive, models.StatusPending, "2020-01-01 00:00:00", "", func(ctx context.Context, id string, _ int) error {
					_, _, err := taskRepo.DeleteWithCleanupManifest(ctx, id, nil)
					return err
				})
			},
		},
		{
			name: "DeleteWithCleanupManifestIfCategory",
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				return taskDiscoveryWritePoolRun(tb, db, taskDiscoveryWritePoolPrefix+"delete-manifest-category-", models.CategoryActive, models.StatusPending, "2020-01-01 00:00:00", "", func(ctx context.Context, id string, _ int) error {
					_, _, err := taskRepo.DeleteWithCleanupManifestIfCategory(ctx, id, "default", models.CategoryActive, nil)
					return err
				})
			},
		},
		{
			name:         "DeleteWithCleanupManifestTerminalizesSwarmChildren",
			operations:   taskDiscoveryBulkWriteOperations,
			affectedRows: taskDiscoveryWritePoolSize + 1,
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				parentPrefix := taskDiscoveryWritePoolPrefix + "delete-swarm-parent-"
				parentIDs := seedTaskDiscoveryWritePool(tb, db, parentPrefix, models.CategoryActive, models.StatusPending, "2020-01-01 00:00:00", "", 1)
				if _, err := db.ExecContext(context.Background(), `UPDATE tasks SET swarm_role = 'swarm_parent' WHERE id = ?`, parentIDs[0]); err != nil {
					tb.Fatalf("set task discovery deletion parent role: %v", err)
				}
				childPrefix := taskDiscoveryWritePoolPrefix + "delete-swarm-child-"
				seedTaskDiscoveryWritePool(tb, db, childPrefix, models.CategoryActive, models.StatusPending, "2020-01-01 00:00:00", parentIDs[0], taskDiscoveryWritePoolSize)
				if _, err := db.ExecContext(context.Background(), `UPDATE tasks SET swarm_role = 'worker' WHERE id LIKE ?`, childPrefix+"%"); err != nil {
					tb.Fatalf("set task discovery deletion child roles: %v", err)
				}
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						_, _, err := taskRepo.DeleteWithCleanupManifest(ctx, parentIDs[0], nil)
						return err
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryWritePool(tb, db, childPrefix)
						cleanupTaskDiscoveryWritePool(tb, db, parentPrefix)
					},
				}
			},
		},
		{
			name:         "DeleteBlockedChildrenByParent",
			operations:   taskDiscoveryBulkWriteOperations,
			affectedRows: taskDiscoveryWritePoolSize,
			setup: func(tb testing.TB, db *sql.DB, taskRepo *repository.TaskRepo) taskDiscoveryWriteRun {
				parentPrefix := taskDiscoveryWritePoolPrefix + "delete-blocked-parent-"
				parentIDs := seedTaskDiscoveryWritePool(tb, db, parentPrefix, models.CategoryActive, models.StatusPending, "2020-01-01 00:00:00", "", 1)
				childPrefix := taskDiscoveryWritePoolPrefix + "delete-blocked-child-"
				seedTaskDiscoveryWritePool(tb, db, childPrefix, models.CategoryBacklog, models.StatusBlocked, "2020-01-01 00:00:00", parentIDs[0], taskDiscoveryWritePoolSize)
				return taskDiscoveryWriteRun{
					write: func(ctx context.Context, _ int) error {
						return taskRepo.DeleteBlockedChildrenByParent(ctx, parentIDs[0])
					},
					cleanup: func(tb testing.TB) {
						cleanupTaskDiscoveryWritePool(tb, db, childPrefix)
						cleanupTaskDiscoveryWritePool(tb, db, parentPrefix)
					},
				}
			},
		},
		taskDiscoveryBulkWritePath("DeleteAllCompleted", taskDiscoveryWritePoolPrefix+"delete-completed-", models.CategoryCompleted, models.StatusCompleted, func(ctx context.Context, taskRepo *repository.TaskRepo) error {
			_, err := taskRepo.DeleteAllCompleted(ctx, "default")
			return err
		}),
		taskDiscoveryBulkWritePath("DeleteAllBacklog", taskDiscoveryWritePoolPrefix+"delete-backlog-", models.CategoryBacklog, models.StatusPending, func(ctx context.Context, taskRepo *repository.TaskRepo) error {
			_, err := taskRepo.DeleteAllBacklog(ctx, "default")
			return err
		}),
		taskDiscoveryBulkWritePath("DeleteAllChat", taskDiscoveryWritePoolPrefix+"delete-chat-", models.CategoryChat, models.StatusCompleted, func(ctx context.Context, taskRepo *repository.TaskRepo) error {
			_, err := taskRepo.DeleteAllChat(ctx, "default")
			return err
		}),
	}
}

func TestTaskDiscoveryWritePathInventory(t *testing.T) {
	paths := taskDiscoveryWritePaths()
	if len(paths) != taskDiscoveryWritePathCount {
		t.Fatalf("task discovery writer path count = %d, want %d", len(paths), taskDiscoveryWritePathCount)
	}
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path.name) == "" {
			t.Fatal("task discovery writer path has an empty name")
		}
		if _, ok := seen[path.name]; ok {
			t.Fatalf("task discovery writer path %q is duplicated", path.name)
		}
		seen[path.name] = struct{}{}
	}
}

func taskDiscoveryWritePathUsesRelativeBudget(path taskDiscoveryWritePath) bool {
	if path.affectedRows >= taskDiscoveryWritePoolSize {
		return false
	}
	switch path.name {
	case "UpdateSwarmFields", "UpdateAgentID", "UpdateWorktreeInfo", "UpdateMergeStatus", "UpdateAutoMerge", "UpdateLineage",
		"UpdateTelegramOrigin", "UpdateSlackOrigin", "UpdateEmailOrigin", "UpdateDiscordOrigin", "UpdateXOrigin",
		"UpdateAgentDefinition", "ClearWorktreeInfo":
		return false
	default:
		return true
	}
}

func taskDiscoveryWritePathAffectedRows(path taskDiscoveryWritePath) int {
	if path.affectedRows > 0 {
		return path.affectedRows
	}
	return 1
}

func taskDiscoveryWriteMeasurementOperations(path taskDiscoveryWritePath) int {
	if taskDiscoveryTimingAssertionsDisabled() {
		return 1
	}
	operations := path.operations
	if operations <= 0 {
		operations = taskDiscoveryWriteOperations
	}
	return operations
}

func taskDiscoveryWriteMeasurementSamples() int {
	if taskDiscoveryTimingAssertionsDisabled() {
		return 1
	}
	return taskDiscoveryPairedSamples
}

func measureTaskDiscoveryWritePathSample(tb testing.TB, path taskDiscoveryWritePath, db *sql.DB, taskRepo *repository.TaskRepo, indexed bool) float64 {
	tb.Helper()
	setTaskDiscoveryOrderIndex(tb, db, indexed)
	run := path.setup(tb, db, taskRepo)
	operations := taskDiscoveryWriteMeasurementOperations(path)
	started := time.Now()
	for i := 0; i < operations; i++ {
		if err := run.write(context.Background(), i); err != nil {
			tb.Fatalf("%s write-budget sample: %v", path.name, err)
		}
	}
	elapsed := time.Since(started)
	if run.cleanup != nil {
		run.cleanup(tb)
	}
	return float64(elapsed.Nanoseconds()) / float64(operations)
}

func TestTaskDiscoveryOrderIndexWriteBudget(t *testing.T) {
	timingAssertionsDisabled := taskDiscoveryTimingAssertionsDisabled()
	db, taskRepo := newTaskDiscoveryOrderBenchmarkRepo(t, 10000, 128, 128)
	defer db.Close()

	var aggregateBaselineNs, aggregateCandidateNs float64
	var aggregateOperations, aggregateAffectedRows int
	for _, path := range taskDiscoveryWritePaths() {
		path := path
		t.Run(path.name, func(t *testing.T) {
			var baseline, candidate taskDiscoveryPairedMetrics
			for sample := 0; sample < taskDiscoveryWriteMeasurementSamples(); sample++ {
				if sample%2 == 0 {
					baseline.wallNs = append(baseline.wallNs, measureTaskDiscoveryWritePathSample(t, path, db, taskRepo, false))
					candidate.wallNs = append(candidate.wallNs, measureTaskDiscoveryWritePathSample(t, path, db, taskRepo, true))
					continue
				}
				candidate.wallNs = append(candidate.wallNs, measureTaskDiscoveryWritePathSample(t, path, db, taskRepo, true))
				baseline.wallNs = append(baseline.wallNs, measureTaskDiscoveryWritePathSample(t, path, db, taskRepo, false))
			}
			if timingAssertionsDisabled {
				t.Log("writer path exercised through baseline and indexed production calls; timing budget disabled under coverage instrumentation")
				return
			}

			baselineNs := baseline.medianWallNs()
			candidateNs := candidate.medianWallNs()
			operations := path.operations
			if operations <= 0 {
				operations = taskDiscoveryWriteOperations
			}
			affectedRows := taskDiscoveryWritePathAffectedRows(path)
			workUnits := operations * affectedRows
			aggregateBaselineNs += (baselineNs / float64(affectedRows)) * float64(workUnits)
			aggregateCandidateNs += (candidateNs / float64(affectedRows)) * float64(workUnits)
			aggregateOperations += operations
			aggregateAffectedRows += workUnits
			t.Logf("paired %s median ns/op: baseline=%.0f candidate=%.0f overhead=%.1f%% delta=%.0f ns; operations=%d affected_rows=%d", path.name, baselineNs, candidateNs, (candidateNs/baselineNs-1)*100, candidateNs-baselineNs, operations, workUnits)
			baselinePerAffectedRowNs := baselineNs / float64(affectedRows)
			candidatePerAffectedRowNs := candidateNs / float64(affectedRows)
			if taskDiscoveryWritePathUsesRelativeBudget(path) {
				if candidatePerAffectedRowNs > baselinePerAffectedRowNs*taskDiscoveryMaxWriteLatencyRegression {
					t.Fatalf("%s indexed write latency regressed by more than 20%%: baseline=%.0f candidate=%.0f ns/op across %d affected rows", path.name, baselineNs, candidateNs, workUnits)
				}
			} else if candidatePerAffectedRowNs-baselinePerAffectedRowNs > taskDiscoveryMaxFixedPerAffectedRowOverheadNs {
				t.Fatalf("%s indexed write latency exceeded the lightweight absolute budget: baseline=%.0f candidate=%.0f ns/op, delta=%.0f ns/affected-row across %d affected rows", path.name, baselineNs, candidateNs, candidatePerAffectedRowNs-baselinePerAffectedRowNs, workUnits)
			}
		})
	}

	if !timingAssertionsDisabled {
		baselinePerAffectedRowNs := aggregateBaselineNs / float64(aggregateAffectedRows)
		candidatePerAffectedRowNs := aggregateCandidateNs / float64(aggregateAffectedRows)
		if candidatePerAffectedRowNs > baselinePerAffectedRowNs*taskDiscoveryMaxWriteLatencyRegression {
			t.Fatalf("complete indexed task-writer workload regressed by more than 20%%: baseline=%.0f candidate=%.0f ns/affected-row across %d operations and %d affected rows", baselinePerAffectedRowNs, candidatePerAffectedRowNs, aggregateOperations, aggregateAffectedRows)
		}
		t.Logf("complete indexed task-writer workload: baseline=%.0f candidate=%.0f ns/affected-row overhead=%.1f%% across %d operations and %d affected rows", baselinePerAffectedRowNs, candidatePerAffectedRowNs, (candidatePerAffectedRowNs/baselinePerAffectedRowNs-1)*100, aggregateOperations, aggregateAffectedRows)
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
// the production repository. The paired TestTaskDiscoveryOrderIndexWriteBudget
// applies the authoritative per-path budget (relative or fixed per affected row)
// to the complete inventory and the weighted aggregate cap; this benchmark
// exposes each path's ns/op and index storage for diagnosis.
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
				run := path.setup(b, db, taskRepo)
				ctx := context.Background()

				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := run.write(ctx, i); err != nil {
						b.Fatalf("%s %s: %v", path.name, name, err)
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(taskDiscoveryOrderIndexBytes(b, db)), "discovery_index_storage_bytes")
				if run.cleanup != nil {
					run.cleanup(b)
				}
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

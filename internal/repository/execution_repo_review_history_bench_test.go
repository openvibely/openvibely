package repository

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/database"
	"github.com/openvibely/openvibely/internal/models"
)

const (
	reviewFollowupHistoryBenchmarkRows    = 200
	reviewFollowupHistoryBenchmarkSamples = 5
	reviewFollowupHistoryPromptBytes      = 4 * 1024
	reviewFollowupHistoryOutputBytes      = 64 * 1024
)

type reviewFollowupHistoryFixture struct {
	connections *database.Connections
	repo        *ExecutionRepo
	taskID      string
	currentID   string
}

type reviewFollowupHistoryMetrics struct {
	latency               time.Duration
	allocatedBytes        uint64
	allocations           uint64
	historyRows           int
	historicPayloadRows   int
	historicPayloadBytes  int
	concurrentReaderWait  time.Duration
	concurrentReaderWaits int64
}

func TestExecutionRepo_ReviewFollowupHistoryWindowProductionCost(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping production-topology review follow-up history measurements in short mode")
	}

	fixture := newReviewFollowupHistoryFixture(t)
	boundedQuery, boundedArgs := taskExecutionPageSQL(fixture.taskID, "", taskThreadHistoryLimitForBenchmark)
	plan := explainExecutionQueryPlan(t, fixture.connections.Reader, boundedQuery, boundedArgs...)
	if !strings.Contains(plan, "idx_executions_task_started_at") {
		t.Fatalf("bounded review follow-up plan = %s, want idx_executions_task_started_at", plan)
	}
	if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("bounded review follow-up plan = %s, want no temporary ORDER BY sort", plan)
	}

	full := fixture.measure(t, fixture.repo.ListByTaskChronological)
	bounded := fixture.measure(t, func(ctx context.Context, taskID string) ([]models.Execution, error) {
		return fixture.repo.ListByTaskChronologicalLimit(ctx, taskID, taskThreadHistoryLimitForBenchmark)
	})

	if full.historyRows != reviewFollowupHistoryBenchmarkRows {
		t.Fatalf("all-history rows = %d, want %d", full.historyRows, reviewFollowupHistoryBenchmarkRows)
	}
	if bounded.historyRows > taskThreadHistoryLimitForBenchmark {
		t.Fatalf("bounded review history rows = %d, want at most %d", bounded.historyRows, taskThreadHistoryLimitForBenchmark)
	}
	if bounded.historicPayloadRows*100 > full.historicPayloadRows*11 {
		t.Fatalf("bounded historic payload rows = %d/%d, want at least 89%% reduction", bounded.historicPayloadRows, full.historicPayloadRows)
	}
	if bounded.historicPayloadBytes*100 > full.historicPayloadBytes*11 {
		t.Fatalf("bounded historic payload bytes = %d/%d, want at least 89%% reduction", bounded.historicPayloadBytes, full.historicPayloadBytes)
	}

	filtered, err := fixture.repo.ListByTaskChronologicalLimit(context.Background(), fixture.taskID, taskThreadHistoryLimitForBenchmark)
	if err != nil {
		t.Fatalf("load bounded review history for semantic check: %v", err)
	}
	filtered = filterReviewFollowupHistory(filtered, fixture.currentID)
	if len(filtered) != 14 {
		t.Fatalf("filtered bounded review history rows = %d, want 14 after current/running exclusion and context boundary", len(filtered))
	}
	for i, execution := range filtered {
		wantID := fmt.Sprintf("review-followup-history-%03d", i+185)
		if execution.ID != wantID {
			t.Fatalf("filtered row %d id = %q, want %q", i, execution.ID, wantID)
		}
		if execution.Status == models.ExecRunning || execution.ID == fixture.currentID {
			t.Fatalf("filtered history retained non-replayable execution %+v", execution)
		}
	}

	t.Logf("review follow-up history median: all-history=%s/%d B/%d allocs/%d rows/%d payload rows/%d payload bytes/%s concurrent reader wait (%d waits); bounded=%s/%d B/%d allocs/%d rows/%d payload rows/%d payload bytes/%s concurrent reader wait (%d waits); plan=%s",
		full.latency, full.allocatedBytes, full.allocations, full.historyRows, full.historicPayloadRows, full.historicPayloadBytes, full.concurrentReaderWait, full.concurrentReaderWaits,
		bounded.latency, bounded.allocatedBytes, bounded.allocations, bounded.historyRows, bounded.historicPayloadRows, bounded.historicPayloadBytes, bounded.concurrentReaderWait, bounded.concurrentReaderWaits,
		plan,
	)
}

func BenchmarkExecutionRepoReviewFollowupHistoryHydration(b *testing.B) {
	fixture := newReviewFollowupHistoryFixture(b)
	benchmarks := []struct {
		name string
		load func(context.Context, string) ([]models.Execution, error)
	}{
		{name: "all_history", load: fixture.repo.ListByTaskChronological},
		{name: "bounded_recent_context", load: func(ctx context.Context, taskID string) ([]models.Execution, error) {
			return fixture.repo.ListByTaskChronologicalLimit(ctx, taskID, taskThreadHistoryLimitForBenchmark)
		}},
	}

	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			history, err := benchmark.load(context.Background(), fixture.taskID)
			if err != nil {
				b.Fatalf("warm review follow-up history: %v", err)
			}
			if len(history) > taskThreadHistoryLimitForBenchmark && benchmark.name == "bounded_recent_context" {
				b.Fatalf("bounded review history rows = %d, want at most %d", len(history), taskThreadHistoryLimitForBenchmark)
			}
			payloadRows, payloadBytes := reviewFollowupHistoryPayload(history)
			wait, waits := fixture.measureConcurrentReader(b, benchmark.load)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := benchmark.load(context.Background(), fixture.taskID); err != nil {
					b.Fatalf("load review follow-up history: %v", err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(len(history)), "history_rows/op")
			b.ReportMetric(float64(payloadRows), "historic_payload_rows/op")
			b.ReportMetric(float64(payloadBytes), "historic_payload_bytes/op")
			b.ReportMetric(float64(wait.Nanoseconds()), "concurrent_reader_wait_ns")
			b.ReportMetric(float64(waits), "concurrent_reader_waits")
		})
	}
}

const taskThreadHistoryLimitForBenchmark = 21

func newReviewFollowupHistoryFixture(tb testing.TB) *reviewFollowupHistoryFixture {
	tb.Helper()
	connections, err := database.NewReadWrite(filepath.Join(tb.TempDir(), "review-followup-history.db"))
	if err != nil {
		tb.Fatalf("open production-topology database: %v", err)
	}
	tb.Cleanup(func() {
		if err := connections.Close(); err != nil {
			tb.Errorf("close production-topology database: %v", err)
		}
	})
	if connections.Reader == connections.Writer || connections.Reader.Stats().MaxOpenConnections != 1 || connections.Writer.Stats().MaxOpenConnections != 1 {
		tb.Fatalf("review follow-up fixture must use production 1W + 1R topology: reader=%p writer=%p reader_max=%d writer_max=%d",
			connections.Reader, connections.Writer, connections.Reader.Stats().MaxOpenConnections, connections.Writer.Stats().MaxOpenConnections)
	}
	unregister := RegisterDedicatedWriter(connections.Reader, connections.Writer)
	tb.Cleanup(unregister)

	ctx := context.Background()
	task := &models.Task{
		ProjectID: "default",
		Title:     "Production Review Follow-up History",
		Category:  models.CategoryCompleted,
		Priority:  2,
		Status:    models.StatusCompleted,
		Prompt:    "review follow-up benchmark",
	}
	if err := NewTaskRepo(connections.Reader, nil).Create(ctx, task); err != nil {
		tb.Fatalf("create review follow-up task: %v", err)
	}

	prompt := strings.Repeat("p", reviewFollowupHistoryPromptBytes)
	output := strings.Repeat("o", reviewFollowupHistoryOutputBytes)
	errorMessage := strings.Repeat("review-error-", 64)
	statement, err := connections.Writer.PrepareContext(ctx, `INSERT INTO executions
		(id, task_id, status, prompt_sent, output, error_message, starts_new_context, started_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tb.Fatalf("prepare review follow-up fixture insert: %v", err)
	}
	defer statement.Close()
	baseTime := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= reviewFollowupHistoryBenchmarkRows; i++ {
		status := models.ExecCompleted
		var completedAt any = baseTime.Add(time.Duration(i) * time.Second)
		if i == reviewFollowupHistoryBenchmarkRows-1 {
			status = models.ExecRunning
			completedAt = nil
		}
		if i == reviewFollowupHistoryBenchmarkRows {
			status = models.ExecQueued
			completedAt = nil
		}
		if _, err := statement.ExecContext(ctx,
			fmt.Sprintf("review-followup-history-%03d", i), task.ID, status, prompt, output, errorMessage, i == 185,
			baseTime.Add(time.Duration(i)*time.Second), completedAt); err != nil {
			tb.Fatalf("insert review follow-up execution %d: %v", i, err)
		}
	}

	return &reviewFollowupHistoryFixture{
		connections: connections,
		repo:        NewExecutionRepo(connections.Reader),
		taskID:      task.ID,
		currentID:   fmt.Sprintf("review-followup-history-%03d", reviewFollowupHistoryBenchmarkRows),
	}
}

func (fixture *reviewFollowupHistoryFixture) measure(t *testing.T, load func(context.Context, string) ([]models.Execution, error)) reviewFollowupHistoryMetrics {
	t.Helper()
	for range 2 {
		if _, err := load(context.Background(), fixture.taskID); err != nil {
			t.Fatalf("warm review follow-up history: %v", err)
		}
	}

	latencies := make([]time.Duration, 0, reviewFollowupHistoryBenchmarkSamples)
	allocatedBytes := make([]uint64, 0, reviewFollowupHistoryBenchmarkSamples)
	allocations := make([]uint64, 0, reviewFollowupHistoryBenchmarkSamples)
	concurrentWaits := make([]time.Duration, 0, reviewFollowupHistoryBenchmarkSamples)
	concurrentWaitCounts := make([]int64, 0, reviewFollowupHistoryBenchmarkSamples)
	var historyRows, payloadRows, payloadBytes int
	for range reviewFollowupHistoryBenchmarkSamples {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		startedAt := time.Now()
		history, err := load(context.Background(), fixture.taskID)
		if err != nil {
			t.Fatalf("load review follow-up history: %v", err)
		}
		latencies = append(latencies, time.Since(startedAt))
		runtime.ReadMemStats(&after)
		allocatedBytes = append(allocatedBytes, after.TotalAlloc-before.TotalAlloc)
		allocations = append(allocations, after.Mallocs-before.Mallocs)
		historyRows = len(history)
		payloadRows, payloadBytes = reviewFollowupHistoryPayload(history)
		wait, waitCount := fixture.measureConcurrentReader(t, load)
		concurrentWaits = append(concurrentWaits, wait)
		concurrentWaitCounts = append(concurrentWaitCounts, waitCount)
	}
	slices.Sort(latencies)
	slices.Sort(allocatedBytes)
	slices.Sort(allocations)
	slices.Sort(concurrentWaits)
	slices.Sort(concurrentWaitCounts)
	middle := reviewFollowupHistoryBenchmarkSamples / 2
	return reviewFollowupHistoryMetrics{
		latency:               latencies[middle],
		allocatedBytes:        allocatedBytes[middle],
		allocations:           allocations[middle],
		historyRows:           historyRows,
		historicPayloadRows:   payloadRows,
		historicPayloadBytes:  payloadBytes,
		concurrentReaderWait:  concurrentWaits[middle],
		concurrentReaderWaits: concurrentWaitCounts[middle],
	}
}

func (fixture *reviewFollowupHistoryFixture) measureConcurrentReader(tb testing.TB, load func(context.Context, string) ([]models.Execution, error)) (time.Duration, int64) {
	tb.Helper()
	loadStarted := make(chan struct{})
	loadDone := make(chan error, 1)
	go func() {
		close(loadStarted)
		_, err := load(context.Background(), fixture.taskID)
		loadDone <- err
	}()
	<-loadStarted

	deadline := time.Now().Add(time.Second)
	for fixture.connections.Reader.Stats().InUse == 0 && time.Now().Before(deadline) {
		runtime.Gosched()
	}

	const concurrentReaders = 8
	ready := make(chan struct{}, concurrentReaders)
	start := make(chan struct{})
	readErrors := make(chan error, concurrentReaders)
	for range concurrentReaders {
		go func() {
			ready <- struct{}{}
			<-start
			var projectID string
			readErrors <- fixture.connections.Reader.QueryRowContext(context.Background(), `SELECT id FROM projects ORDER BY id LIMIT 1`).Scan(&projectID)
		}()
	}
	for range concurrentReaders {
		<-ready
	}
	before := fixture.connections.Reader.Stats()
	close(start)
	for range concurrentReaders {
		if err := <-readErrors; err != nil {
			tb.Fatalf("concurrent lightweight project read: %v", err)
		}
	}
	if err := <-loadDone; err != nil {
		tb.Fatalf("review follow-up history with concurrent reads: %v", err)
	}
	after := fixture.connections.Reader.Stats()
	return after.WaitDuration - before.WaitDuration, after.WaitCount - before.WaitCount
}

func reviewFollowupHistoryPayload(history []models.Execution) (int, int) {
	rows := 0
	bytes := 0
	for _, execution := range history {
		payloadBytes := len(execution.PromptSent) + len(execution.Output) + len(execution.ErrorMessage)
		if payloadBytes == 0 {
			continue
		}
		rows++
		bytes += payloadBytes
	}
	return rows, bytes
}

func filterReviewFollowupHistory(history []models.Execution, currentID string) []models.Execution {
	filtered := make([]models.Execution, 0, len(history))
	for _, execution := range history {
		if execution.ID == currentID || execution.Status == models.ExecRunning {
			continue
		}
		if execution.StartsNewContext {
			filtered = filtered[:0]
		}
		filtered = append(filtered, execution)
	}
	return filtered
}

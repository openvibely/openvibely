package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func BenchmarkTaskRepo_ListWithSchedulesByProject(b *testing.B) {
	fixtures := []struct {
		name       string
		taskCount  int
		promptSize int
	}{
		{name: "Small20x512B", taskCount: 20, promptSize: 512},
		{name: "Large300x32KiB", taskCount: 300, promptSize: 32 * 1024},
		{name: "Scale20k", taskCount: 20000, promptSize: 512},
		{name: "Scale250k", taskCount: 250000, promptSize: 512},
	}

	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) {
			db := newScheduleCalendarBenchmarkDB(b, fixture.taskCount, fixture.promptSize)
			defer db.Close()
			repo := NewTaskRepo(db, nil)

			b.Run("CompactOrderedBaseline", func(b *testing.B) {
				benchmarkListWithSchedulesCalendarQuery(b, repo, scheduleCalendarQuery+" ORDER BY s.next_run ASC", "default")
			})
			b.Run("CompactUnordered", func(b *testing.B) {
				benchmarkListWithSchedulesCalendarQuery(b, repo, scheduleCalendarQuery, "default")
			})
		})
	}
}

func TestCalendarQuery_NoTempBTreeSort(t *testing.T) {
	if strings.Contains(strings.ToUpper(scheduleCalendarQuery), "ORDER BY") {
		t.Fatalf("production calendar query must not globally sort rows: %s", scheduleCalendarQuery)
	}

	db := testutil.NewTestDB(t)
	defer db.Close()

	ctx := context.Background()
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+scheduleCalendarQuery, "default")
	if err != nil {
		t.Fatalf("failed to explain query plan: %v", err)
	}
	defer rows.Close()

	var id, parent, notused int
	var detail string
	for rows.Next() {
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("failed to scan query plan row: %v", err)
		}
		if strings.Contains(detail, "USE TEMP B-TREE FOR ORDER BY") {
			t.Errorf("Query plan contains redundant sorting: %s", detail)
		}
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("error iterating query plan rows: %v", err)
	}
}

func benchmarkListWithSchedulesCalendarQuery(b *testing.B, repo *TaskRepo, query, projectID string) {
	b.Helper()
	ctx := context.Background()
	var unboundedPayloadBytes int64

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tasks, err := repo.listWithSchedulesByProjectQuery(ctx, query, projectID)
		if err != nil {
			b.Fatalf("list schedule calendar tasks: %v", err)
		}
		unboundedPayloadBytes = scheduleCalendarUnboundedPayloadBytes(tasks)
	}
	b.StopTimer()
	b.ReportMetric(float64(unboundedPayloadBytes), "unbounded_payload_bytes/op")
}

func scheduleCalendarUnboundedPayloadBytes(tasks []TaskWithSchedule) int64 {
	var total int64
	for _, task := range tasks {
		total += int64(len(task.Task.Prompt) + len(task.Task.ChainConfig) + len(task.Task.SwarmConfig))
	}
	return total
}

func newScheduleCalendarBenchmarkDB(b *testing.B, taskCount, promptSize int) *sql.DB {
	b.Helper()
	db := testutil.NewTestDB(b)
	taskRepo := NewTaskRepo(db, nil)
	scheduleRepo := NewScheduleRepo(db)
	ctx := context.Background()
	prompt := strings.Repeat("p", promptSize)
	chainConfig := `{"payload":"` + strings.Repeat("c", promptSize) + `"}`
	swarmConfig := `{"payload":"` + strings.Repeat("s", promptSize) + `"}`
	baseRunAt := time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC)

	for i := 0; i < taskCount; i++ {
		task := &models.Task{
			ProjectID:   "default",
			Title:       fmt.Sprintf("Benchmark schedule task %03d", i),
			Category:    models.CategoryScheduled,
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
		schedule := &models.Schedule{
			TaskID:         task.ID,
			RunAt:          baseRunAt.Add(time.Duration(i) * time.Minute),
			RepeatType:     models.RepeatOnce,
			RepeatInterval: 1,
			Enabled:        i%5 != 0,
		}
		if err := scheduleRepo.Create(ctx, schedule); err != nil {
			db.Close()
			b.Fatalf("create benchmark schedule %d: %v", i, err)
		}
	}
	return db
}

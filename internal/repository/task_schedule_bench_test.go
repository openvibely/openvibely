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
	}

	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) {
			db := newScheduleCalendarBenchmarkDB(b, fixture.taskCount, fixture.promptSize)
			defer db.Close()
			repo := NewTaskRepo(db, nil)

			b.Run("FullRowBaseline", func(b *testing.B) {
				benchmarkListWithSchedulesFullRow(b, db, "default")
			})
			b.Run("CalendarProjection", func(b *testing.B) {
				benchmarkListWithSchedulesCalendarProjection(b, repo, "default")
			})
		})
	}
}

func benchmarkListWithSchedulesCalendarProjection(b *testing.B, repo *TaskRepo, projectID string) {
	b.Helper()
	ctx := context.Background()
	var unboundedPayloadBytes int64

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tasks, err := repo.ListWithSchedulesByProject(ctx, projectID)
		if err != nil {
			b.Fatalf("list schedule calendar tasks: %v", err)
		}
		unboundedPayloadBytes = scheduleCalendarUnboundedPayloadBytes(tasks)
	}
	b.StopTimer()
	b.ReportMetric(float64(unboundedPayloadBytes), "unbounded_payload_bytes/op")
}

func benchmarkListWithSchedulesFullRow(b *testing.B, db *sql.DB, projectID string) {
	b.Helper()
	ctx := context.Background()
	var unboundedPayloadBytes int64

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := db.QueryContext(ctx,
			`SELECT t.`+strings.ReplaceAll(taskSelectColumns, ", ", ", t.")+`,
			 s.id, s.task_id, s.run_at, s.repeat_type, s.repeat_interval, s.enabled, s.clear_context_on_start, s.next_run, s.last_run, s.created_at, s.updated_at,
			 COALESCE(automation_node.name, '')
			 FROM tasks t
			 LEFT JOIN schedules s ON t.id = s.task_id
			 LEFT JOIN automation_trigger_owners automation_owner ON automation_owner.schedule_id = s.id AND automation_owner.project_id = t.project_id
			 LEFT JOIN automation_nodes automation_node ON automation_node.id = automation_owner.node_id
				AND automation_node.version_id = automation_owner.version_id
				AND automation_node.automation_id = automation_owner.automation_id
				AND automation_node.project_id = automation_owner.project_id
			 WHERE t.project_id = ? AND (t.category = 'scheduled' OR s.id IS NOT NULL)
			 ORDER BY s.next_run ASC`, projectID)
		if err != nil {
			b.Fatalf("query full schedule rows: %v", err)
		}

		var tasks []TaskWithSchedule
		for rows.Next() {
			var tws TaskWithSchedule
			var schedID, schedTaskID sql.NullString
			var schedRunAt, schedCreatedAt, schedUpdatedAt sql.NullTime
			var schedRepeatType, schedRepeatInterval, schedEnabled, schedClearContext sql.NullString
			var schedNextRun, schedLastRun sql.NullTime
			if err := rows.Scan(&tws.Task.ID, &tws.Task.ProjectID, &tws.Task.Title, &tws.Task.Category,
				&tws.Task.Priority, &tws.Task.Status, &tws.Task.Prompt, &tws.Task.AgentID, &tws.Task.AgentDefinitionID, &tws.Task.Tag, &tws.Task.DisplayOrder, &tws.Task.ParentTaskID, &tws.Task.ChainConfig, &tws.Task.SwarmRole, &tws.Task.SwarmStatus, &tws.Task.SwarmConfig, &tws.Task.SwarmSequence, &tws.Task.WorktreePath, &tws.Task.WorktreeBranch, &tws.Task.AutoMerge, &tws.Task.MergeTargetBranch, &tws.Task.MergeStatus, &tws.Task.BaseBranch, &tws.Task.BaseCommitSHA, &tws.Task.LineageDepth, &tws.Task.CreatedVia, &tws.Task.TelegramChatID, &tws.Task.CreatedAt, &tws.Task.UpdatedAt, &tws.Task.CompletedAt,
				&schedID, &schedTaskID, &schedRunAt, &schedRepeatType, &schedRepeatInterval, &schedEnabled, &schedClearContext, &schedNextRun, &schedLastRun, &schedCreatedAt, &schedUpdatedAt,
				&tws.AutomationScheduleName); err != nil {
				rows.Close()
				b.Fatalf("scan full schedule row: %v", err)
			}
			if schedID.Valid {
				tws.Schedule = &models.Schedule{ID: schedID.String, TaskID: schedTaskID.String, RunAt: schedRunAt.Time, RepeatType: models.RepeatType(schedRepeatType.String), Enabled: schedEnabled.String == "1" || schedEnabled.String == "true", ClearContextOnStart: schedClearContext.String == "1" || schedClearContext.String == "true", CreatedAt: schedCreatedAt.Time, UpdatedAt: schedUpdatedAt.Time}
				fmt.Sscanf(schedRepeatInterval.String, "%d", &tws.Schedule.RepeatInterval)
				if schedNextRun.Valid {
					tws.Schedule.NextRun = &schedNextRun.Time
				}
				if schedLastRun.Valid {
					tws.Schedule.LastRun = &schedLastRun.Time
				}
			}
			tasks = append(tasks, tws)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			b.Fatalf("iterate full schedule rows: %v", err)
		}
		rows.Close()
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

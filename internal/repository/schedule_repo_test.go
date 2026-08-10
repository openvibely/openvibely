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

func createTestTask(t *testing.T, taskRepo *TaskRepo) *models.Task {
	t.Helper()
	task := &models.Task{
		ProjectID: "default",
		Title:     "Scheduled Task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("creating test task: %v", err)
	}
	return task
}

func TestScheduleRepo_RejectsOversizedIntervalOnCreateAndUpdate(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewScheduleRepo(db)
	ctx := context.Background()
	task := createTestTask(t, taskRepo)

	oversized := &models.Schedule{
		TaskID: task.ID, RunAt: time.Now().UTC(), RepeatType: models.RepeatSeconds,
		RepeatInterval: 366, Enabled: true,
	}
	if err := repo.Create(ctx, oversized); err == nil {
		t.Fatal("expected oversized interval creation to fail")
	}
	schedules, err := repo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 0 {
		t.Fatalf("expected no persisted oversized schedule, got %d", len(schedules))
	}

	valid := &models.Schedule{
		TaskID: task.ID, RunAt: time.Now().UTC(), RepeatType: models.RepeatDaily,
		RepeatInterval: 1, Enabled: true,
	}
	if err := repo.Create(ctx, valid); err != nil {
		t.Fatal(err)
	}
	valid.RepeatInterval = 366
	if err := repo.Update(ctx, valid); err == nil {
		t.Fatal("expected oversized interval update to fail")
	}
	persisted, err := repo.GetByID(ctx, valid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.RepeatInterval != 1 {
		t.Fatalf("oversized update was persisted: interval=%d", persisted.RepeatInterval)
	}
}

func TestScheduleRepo_CreateAndGetByID(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewScheduleRepo(db)
	ctx := context.Background()

	task := createTestTask(t, taskRepo)
	runAt := time.Now().Add(1 * time.Hour).Truncate(time.Second)

	sched := &models.Schedule{
		TaskID:              task.ID,
		RunAt:               runAt,
		RepeatType:          models.RepeatOnce,
		RepeatInterval:      1,
		Enabled:             true,
		ClearContextOnStart: true,
	}

	if err := repo.Create(ctx, sched); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sched.ID == "" {
		t.Fatal("expected ID to be set")
	}

	got, err := repo.GetByID(ctx, sched.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected schedule, got nil")
	}
	if got.TaskID != task.ID {
		t.Errorf("expected TaskID=%s, got %s", task.ID, got.TaskID)
	}
	if got.RepeatType != models.RepeatOnce {
		t.Errorf("expected RepeatType=once, got %q", got.RepeatType)
	}
	if !got.ClearContextOnStart {
		t.Error("expected ClearContextOnStart to round-trip")
	}
}

func TestScheduleRepo_UpdateClearContextOnStartPreservesTimingState(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewScheduleRepo(db)
	ctx := context.Background()

	task := createTestTask(t, taskRepo)
	runAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	staleNextRun := runAt.Add(time.Hour)
	schedule := &models.Schedule{
		TaskID:              task.ID,
		RunAt:               runAt,
		RepeatType:          models.RepeatHours,
		RepeatInterval:      3,
		Enabled:             true,
		ClearContextOnStart: false,
		NextRun:             &staleNextRun,
	}
	if err := repo.Create(ctx, schedule); err != nil {
		t.Fatalf("Create: %v", err)
	}

	lastRun := time.Now().UTC().Truncate(time.Second)
	advancedNextRun := lastRun.Add(3 * time.Hour)
	if err := repo.MarkRan(ctx, schedule.ID, lastRun, &advancedNextRun); err != nil {
		t.Fatalf("MarkRan: %v", err)
	}
	if err := repo.UpdateClearContextOnStart(ctx, schedule.ID, task.ID, true); err != nil {
		t.Fatalf("UpdateClearContextOnStart: %v", err)
	}

	got, err := repo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.ClearContextOnStart {
		t.Fatal("expected ClearContextOnStart to update")
	}
	if !got.RunAt.Equal(runAt) || got.RepeatType != models.RepeatHours || got.RepeatInterval != 3 || !got.Enabled {
		t.Fatalf("context-only update changed schedule configuration: %#v", got)
	}
	if got.LastRun == nil || !got.LastRun.Equal(lastRun) {
		t.Fatalf("LastRun changed: got %v, want %v", got.LastRun, lastRun)
	}
	if got.NextRun == nil || !got.NextRun.Equal(advancedNextRun) {
		t.Fatalf("NextRun changed: got %v, want %v", got.NextRun, advancedNextRun)
	}
}

func TestScheduleRepo_UpdateClearContextOnStartRequiresTaskOwnership(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewScheduleRepo(db)
	ctx := context.Background()

	originalTask := createTestTask(t, taskRepo)
	otherTask := &models.Task{
		ProjectID: "default", Title: "New schedule owner", Category: models.CategoryScheduled,
		Status: models.StatusPending, Prompt: "other",
	}
	if err := taskRepo.Create(ctx, otherTask); err != nil {
		t.Fatalf("creating other task: %v", err)
	}
	runAt := time.Now().UTC().Add(time.Hour)
	schedule := &models.Schedule{
		TaskID: originalTask.ID, RunAt: runAt, RepeatType: models.RepeatDaily,
		RepeatInterval: 1, Enabled: true, ClearContextOnStart: true,
	}
	if err := repo.Create(ctx, schedule); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE schedules SET task_id = ? WHERE id = ?`, otherTask.ID, schedule.ID); err != nil {
		t.Fatalf("reassign schedule: %v", err)
	}

	if err := repo.UpdateClearContextOnStart(ctx, schedule.ID, originalTask.ID, false); err != nil {
		t.Fatalf("UpdateClearContextOnStart: %v", err)
	}
	got, err := repo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.TaskID != otherTask.ID {
		t.Fatalf("schedule owner changed: got %s, want %s", got.TaskID, otherTask.ID)
	}
	if !got.ClearContextOnStart {
		t.Fatal("stale owner must not update schedule context policy")
	}
}

func TestScheduleRepo_ListDue(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewScheduleRepo(db)
	ctx := context.Background()

	task := createTestTask(t, taskRepo)
	now := time.Now().UTC()

	// Schedule in the past (due)
	pastSched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now.Add(-1 * time.Hour),
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}
	repo.Create(ctx, pastSched)

	// Schedule in the future (not due)
	task2 := &models.Task{ProjectID: "default", Title: "Future", Category: models.CategoryScheduled, Status: models.StatusPending, Prompt: "p"}
	taskRepo.Create(ctx, task2)
	futureSched := &models.Schedule{
		TaskID:         task2.ID,
		RunAt:          now.Add(1 * time.Hour),
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}
	repo.Create(ctx, futureSched)

	due, err := repo.ListDue(ctx, now)
	if err != nil {
		t.Fatalf("ListDue: %v", err)
	}
	if len(due) != 1 {
		t.Errorf("expected 1 due schedule, got %d", len(due))
	}
	if len(due) > 0 && due[0].ID != pastSched.ID {
		t.Errorf("expected due schedule ID=%s, got %s", pastSched.ID, due[0].ID)
	}
}

func TestScheduleRepo_ListDue_DisabledNotReturned(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewScheduleRepo(db)
	ctx := context.Background()

	task := createTestTask(t, taskRepo)
	now := time.Now().UTC()

	// Disabled schedule in the past
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now.Add(-1 * time.Hour),
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}
	repo.Create(ctx, sched)
	repo.ToggleEnabled(ctx, sched.ID, false)

	due, _ := repo.ListDue(ctx, now)
	if len(due) != 0 {
		t.Errorf("expected 0 due schedules (disabled), got %d", len(due))
	}
}

func TestScheduleRepo_MarkRan(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewScheduleRepo(db)
	ctx := context.Background()

	task := createTestTask(t, taskRepo)
	now := time.Now().UTC()

	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now.Add(-1 * time.Minute),
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}
	repo.Create(ctx, sched)

	nextRun := now.Add(24 * time.Hour)
	if err := repo.MarkRan(ctx, sched.ID, now, &nextRun); err != nil {
		t.Fatalf("MarkRan: %v", err)
	}

	got, _ := repo.GetByID(ctx, sched.ID)
	if got.LastRun == nil {
		t.Fatal("expected LastRun to be set")
	}
	if got.NextRun == nil {
		t.Fatal("expected NextRun to be set")
	}
	if got.NextRun.Before(now) {
		t.Error("expected NextRun to be in the future")
	}
}

func TestScheduleRepo_MarkRan_OneTime_NilNextRun(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewScheduleRepo(db)
	ctx := context.Background()

	task := createTestTask(t, taskRepo)
	now := time.Now().UTC()

	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now.Add(-1 * time.Minute),
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}
	repo.Create(ctx, sched)

	if err := repo.MarkRan(ctx, sched.ID, now, nil); err != nil {
		t.Fatalf("MarkRan: %v", err)
	}

	got, _ := repo.GetByID(ctx, sched.ID)
	if got.NextRun != nil {
		t.Error("expected NextRun to be nil for one-time schedule after running")
	}
}

func TestScheduleRepo_ListByTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewScheduleRepo(db)
	ctx := context.Background()

	task := createTestTask(t, taskRepo)
	now := time.Now().UTC()

	repo.Create(ctx, &models.Schedule{TaskID: task.ID, RunAt: now, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true})
	repo.Create(ctx, &models.Schedule{TaskID: task.ID, RunAt: now.Add(1 * time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true})

	schedules, err := repo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(schedules) != 2 {
		t.Errorf("expected 2 schedules, got %d", len(schedules))
	}
}

func TestScheduleRepo_ListByTaskIDs(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewScheduleRepo(db)
	ctx := context.Background()

	task1 := createTestTask(t, taskRepo)
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryScheduled, Status: models.StatusPending, Prompt: "p2"}
	taskRepo.Create(ctx, task2)

	now := time.Now().UTC()
	repo.Create(ctx, &models.Schedule{TaskID: task1.ID, RunAt: now, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true})
	repo.Create(ctx, &models.Schedule{TaskID: task1.ID, RunAt: now.Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true})
	repo.Create(ctx, &models.Schedule{TaskID: task2.ID, RunAt: now, RepeatType: models.RepeatWeekly, RepeatInterval: 1, Enabled: true})

	// Batch load
	result, err := repo.ListByTaskIDs(ctx, []string{task1.ID, task2.ID})
	if err != nil {
		t.Fatalf("ListByTaskIDs: %v", err)
	}
	if len(result[task1.ID]) != 2 {
		t.Errorf("expected 2 schedules for task1, got %d", len(result[task1.ID]))
	}
	if len(result[task2.ID]) != 1 {
		t.Errorf("expected 1 schedule for task2, got %d", len(result[task2.ID]))
	}

	// Empty input
	result2, err := repo.ListByTaskIDs(ctx, []string{})
	if err != nil {
		t.Fatalf("ListByTaskIDs empty: %v", err)
	}
	if len(result2) != 0 {
		t.Errorf("expected empty map, got %d entries", len(result2))
	}
}

func TestScheduleRepo_CreateSubDaily(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewScheduleRepo(db)
	ctx := context.Background()

	task := createTestTask(t, taskRepo)
	now := time.Now().UTC().Truncate(time.Second)

	tests := []struct {
		name       string
		repeatType models.RepeatType
		interval   int
	}{
		{"every 10 seconds", models.RepeatSeconds, 10},
		{"every 5 minutes", models.RepeatMinutes, 5},
		{"every 2 hours", models.RepeatHours, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched := &models.Schedule{
				TaskID:         task.ID,
				RunAt:          now,
				RepeatType:     tt.repeatType,
				RepeatInterval: tt.interval,
				Enabled:        true,
			}
			if err := repo.Create(ctx, sched); err != nil {
				t.Fatalf("Create(%s): %v", tt.name, err)
			}
			if sched.ID == "" {
				t.Fatal("expected ID to be set")
			}

			got, err := repo.GetByID(ctx, sched.ID)
			if err != nil {
				t.Fatalf("GetByID: %v", err)
			}
			if got.RepeatType != tt.repeatType {
				t.Errorf("expected RepeatType=%s, got %s", tt.repeatType, got.RepeatType)
			}
			if got.RepeatInterval != tt.interval {
				t.Errorf("expected RepeatInterval=%d, got %d", tt.interval, got.RepeatInterval)
			}
		})
	}
}

func TestScheduleRepo_Delete(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewScheduleRepo(db)
	ctx := context.Background()

	task := createTestTask(t, taskRepo)
	sched := &models.Schedule{TaskID: task.ID, RunAt: time.Now(), RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
	repo.Create(ctx, sched)

	if err := repo.Delete(ctx, sched.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, _ := repo.GetByID(ctx, sched.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestScheduleRepo_ToggleEnabled_Persistence(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewScheduleRepo(db)
	ctx := context.Background()

	task := createTestTask(t, taskRepo)
	future := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)

	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          future,
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}
	if err := repo.Create(ctx, sched); err != nil {
		t.Fatalf("Create: %v", err)
	}
	schedID := sched.ID

	// Disable — NextRun pointer is set by Create; verify it is preserved.
	if err := repo.ToggleEnabled(ctx, schedID, false); err != nil {
		t.Fatalf("ToggleEnabled(false): %v", err)
	}
	got, err := repo.GetByID(ctx, schedID)
	if err != nil {
		t.Fatalf("GetByID after disable: %v", err)
	}
	if got.Enabled {
		t.Error("expected Enabled=false after disabling")
	}
	// NextRun must be preserved (not wiped) when disabling.
	if got.NextRun == nil {
		t.Error("expected NextRun to be preserved after disabling")
	}

	// Re-enable.
	if err := repo.ToggleEnabled(ctx, schedID, true); err != nil {
		t.Fatalf("ToggleEnabled(true): %v", err)
	}
	got, err = repo.GetByID(ctx, schedID)
	if err != nil {
		t.Fatalf("GetByID after re-enable: %v", err)
	}
	if !got.Enabled {
		t.Error("expected Enabled=true after re-enabling")
	}

	// Round-trip: disable again then verify ListDue excludes it.
	if err := repo.ToggleEnabled(ctx, schedID, false); err != nil {
		t.Fatalf("second ToggleEnabled(false): %v", err)
	}
	// Set NextRun to the past so it would normally be due.
	past := time.Now().Add(-time.Hour).UTC()
	sched.ID = schedID
	sched.NextRun = &past
	if err := repo.Update(ctx, sched); err != nil {
		t.Fatalf("Update NextRun to past: %v", err)
	}
	due, err := repo.ListDue(ctx, time.Now())
	if err != nil {
		t.Fatalf("ListDue: %v", err)
	}
	for _, d := range due {
		if d.ID == schedID {
			t.Error("disabled schedule must not appear in ListDue")
		}
	}
}

func TestScheduleRepo_ListSchedulesForDiscovery(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	repo := NewScheduleRepo(db)
	ctx := context.Background()

	project2 := &models.Project{Name: "Schedule Discovery Project 2"}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("Create project2: %v", err)
	}

	mkTask := func(projectID, title string) *models.Task {
		task := &models.Task{ProjectID: projectID, Title: title, Category: models.CategoryScheduled, Status: models.StatusPending, Prompt: "p"}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("Create task %q: %v", title, err)
		}
		return task
	}
	mkSched := func(taskID string, repeat models.RepeatType, enabled bool, runAt time.Time) *models.Schedule {
		s := &models.Schedule{TaskID: taskID, RunAt: runAt, RepeatType: repeat, RepeatInterval: 1, Enabled: enabled, ClearContextOnStart: true}
		if err := repo.Create(ctx, s); err != nil {
			t.Fatalf("Create schedule: %v", err)
		}
		return s
	}

	now := time.Now().UTC().Truncate(time.Second)
	alphaTask := mkTask("default", "Alpha nightly deploy")
	betaTask := mkTask("default", "Beta weekly report")
	foreignTask := mkTask(project2.ID, "Foreign schedule task")

	alphaEnabled := mkSched(alphaTask.ID, models.RepeatDaily, true, now.Add(2*time.Hour))
	alphaDisabled := mkSched(alphaTask.ID, models.RepeatHours, false, now.Add(1*time.Hour))
	betaEnabled := mkSched(betaTask.ID, models.RepeatWeekly, true, now.Add(3*time.Hour))
	// Foreign-project schedule must never appear.
	mkSched(foreignTask.ID, models.RepeatDaily, true, now.Add(time.Hour))

	// Project isolation: only the three default-project schedules appear.
	rows, total, err := repo.ListSchedulesForDiscovery(ctx, "default", ScheduleDiscoveryFilter{})
	if err != nil {
		t.Fatalf("ListSchedulesForDiscovery all: %v", err)
	}
	if total != 3 || len(rows) != 3 {
		t.Fatalf("expected 3 default-project schedules, got total=%d len=%d", total, len(rows))
	}
	for _, r := range rows {
		if r.Schedule.TaskID == foreignTask.ID {
			t.Fatalf("cross-project schedule leaked: %s", r.Schedule.ID)
		}
	}

	// Task title provides task identity in the projection.
	titles := map[string]string{}
	for _, r := range rows {
		titles[r.Schedule.ID] = r.TaskTitle
	}
	if titles[alphaEnabled.ID] != "Alpha nightly deploy" {
		t.Fatalf("expected bound task title, got %q", titles[alphaEnabled.ID])
	}

	// Enabled filter.
	enabledTrue := true
	enabledRows, enabledTotal, err := repo.ListSchedulesForDiscovery(ctx, "default", ScheduleDiscoveryFilter{Enabled: &enabledTrue})
	if err != nil {
		t.Fatalf("ListSchedulesForDiscovery enabled: %v", err)
	}
	if enabledTotal != 2 || len(enabledRows) != 2 {
		t.Fatalf("expected 2 enabled schedules, got total=%d len=%d", enabledTotal, len(enabledRows))
	}
	for _, r := range enabledRows {
		if !r.Schedule.Enabled {
			t.Fatalf("disabled schedule leaked into enabled filter: %s", r.Schedule.ID)
		}
	}
	enabledFalse := false
	disabledRows, disabledTotal, err := repo.ListSchedulesForDiscovery(ctx, "default", ScheduleDiscoveryFilter{Enabled: &enabledFalse})
	if err != nil {
		t.Fatalf("ListSchedulesForDiscovery disabled: %v", err)
	}
	if disabledTotal != 1 || len(disabledRows) != 1 || disabledRows[0].Schedule.ID != alphaDisabled.ID {
		t.Fatalf("expected single disabled schedule, got total=%d len=%d", disabledTotal, len(disabledRows))
	}

	// Task identity filter.
	betaRows, betaTotal, err := repo.ListSchedulesForDiscovery(ctx, "default", ScheduleDiscoveryFilter{TaskID: betaTask.ID})
	if err != nil {
		t.Fatalf("ListSchedulesForDiscovery task_id: %v", err)
	}
	if betaTotal != 1 || len(betaRows) != 1 || betaRows[0].Schedule.ID != betaEnabled.ID {
		t.Fatalf("expected single beta schedule, got total=%d len=%d", betaTotal, len(betaRows))
	}

	// Title partial match.
	titleRows, titleTotal, err := repo.ListSchedulesForDiscovery(ctx, "default", ScheduleDiscoveryFilter{Title: "alpha"})
	if err != nil {
		t.Fatalf("ListSchedulesForDiscovery title: %v", err)
	}
	if titleTotal != 2 || len(titleRows) != 2 {
		t.Fatalf("expected 2 alpha schedules, got total=%d len=%d", titleTotal, len(titleRows))
	}

	// Deterministic ordering: next_run ASC. alphaDisabled(+1h) < alphaEnabled(+2h) < betaEnabled(+3h).
	if rows[0].Schedule.ID != alphaDisabled.ID || rows[1].Schedule.ID != alphaEnabled.ID || rows[2].Schedule.ID != betaEnabled.ID {
		t.Fatalf("expected next_run ASC ordering, got %s,%s,%s", rows[0].Schedule.ID, rows[1].Schedule.ID, rows[2].Schedule.ID)
	}

	// Pagination bounds with no duplicates.
	page1, page1Total, err := repo.ListSchedulesForDiscovery(ctx, "default", ScheduleDiscoveryFilter{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("ListSchedulesForDiscovery page1: %v", err)
	}
	if page1Total != 3 || len(page1) != 2 {
		t.Fatalf("expected page1 total=3 len=2, got total=%d len=%d", page1Total, len(page1))
	}
	page2, _, err := repo.ListSchedulesForDiscovery(ctx, "default", ScheduleDiscoveryFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("ListSchedulesForDiscovery page2: %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("expected page2 len=1, got %d", len(page2))
	}
	seen := map[string]bool{}
	for _, r := range append(append([]ScheduleDiscoveryRow{}, page1...), page2...) {
		if seen[r.Schedule.ID] {
			t.Fatalf("pagination returned duplicate schedule %s", r.Schedule.ID)
		}
		seen[r.Schedule.ID] = true
	}

	// Schedules without a next_run sort after pending schedules, even when their
	// created_at value would otherwise sort first.
	if _, err := db.ExecContext(ctx, `UPDATE schedules SET next_run = NULL, created_at = ? WHERE id = ?`, now.Add(24*time.Hour), alphaDisabled.ID); err != nil {
		t.Fatalf("clear next_run: %v", err)
	}
	nilRows, nilTotal, err := repo.ListSchedulesForDiscovery(ctx, "default", ScheduleDiscoveryFilter{})
	if err != nil {
		t.Fatalf("ListSchedulesForDiscovery nil next_run: %v", err)
	}
	if nilTotal != 3 || len(nilRows) != 3 {
		t.Fatalf("expected 3 rows after nil next_run update, got total=%d len=%d", nilTotal, len(nilRows))
	}
	if nilRows[0].Schedule.ID != alphaEnabled.ID || nilRows[1].Schedule.ID != betaEnabled.ID || nilRows[2].Schedule.ID != alphaDisabled.ID {
		t.Fatalf("expected nil next_run last, got %s,%s,%s", nilRows[0].Schedule.ID, nilRows[1].Schedule.ID, nilRows[2].Schedule.ID)
	}
}

func TestScheduleRepo_ListSchedulesForDiscoveryFirstPageUsesOrderedIndex(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedScheduleDiscoveryFixture(t, db, 4, 30)
	repo := NewScheduleRepo(db)
	ctx := context.Background()

	query := scheduleDiscoverySelectSQL(`t.project_id = ?`, true)
	plan := explainScheduleDiscoveryQueryPlan(t, db, query, scheduleDiscoveryTargetProjectID, 50)
	if !strings.Contains(plan, "idx_schedules_discovery_order") {
		t.Fatalf("first-page plan = %s, want ordered schedule discovery index", plan)
	}
	if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("first-page plan = %s, want no temporary ORDER BY sort", plan)
	}

	rows, total, err := repo.ListSchedulesForDiscovery(ctx, scheduleDiscoveryTargetProjectID, ScheduleDiscoveryFilter{Limit: 50, Offset: 0})
	if err != nil {
		t.Fatalf("ListSchedulesForDiscovery first page: %v", err)
	}
	if total != 150 || len(rows) != 50 {
		t.Fatalf("first page total/len = %d/%d, want 150/50", total, len(rows))
	}
	assertScheduleDiscoveryRowsOrdered(t, rows)
	for _, row := range rows {
		if !strings.Contains(row.TaskTitle, scheduleDiscoveryTargetProjectID) {
			t.Fatalf("cross-project schedule leaked through optimized path: task title %q", row.TaskTitle)
		}
	}
}

func TestScheduleRepo_ListSchedulesForDiscoveryFilteredAndOffsetPathsRemainCorrect(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedScheduleDiscoveryFixture(t, db, 3, 12)
	repo := NewScheduleRepo(db)
	ctx := context.Background()

	// Filtered and nonzero-offset paths intentionally retain the project/task-indexed
	// join plus temp-sort strategy instead of forcing the global order index; selective
	// task, title, and enabled predicates can be cheaper than scanning ordered rows
	// across every project.
	filteredQuery := scheduleDiscoverySelectSQL(`t.project_id = ? AND t.title LIKE ?`, false)
	filteredPlan := explainScheduleDiscoveryQueryPlan(t, db, filteredQuery, scheduleDiscoveryTargetProjectID, "%task-00001%", 20, 0)
	if strings.Contains(filteredPlan, "idx_schedules_discovery_order") {
		t.Fatalf("filtered plan = %s, should not force global order index", filteredPlan)
	}

	rows, total, err := repo.ListSchedulesForDiscovery(ctx, scheduleDiscoveryTargetProjectID, ScheduleDiscoveryFilter{Title: "task-00001", Limit: 10})
	if err != nil {
		t.Fatalf("ListSchedulesForDiscovery title filter: %v", err)
	}
	if total != 5 || len(rows) != 5 {
		t.Fatalf("title filter total/len = %d/%d, want 5/5", total, len(rows))
	}
	assertScheduleDiscoveryRowsOrdered(t, rows)

	disabled := false
	disabledRows, disabledTotal, err := repo.ListSchedulesForDiscovery(ctx, scheduleDiscoveryTargetProjectID, ScheduleDiscoveryFilter{Enabled: &disabled, Limit: 50})
	if err != nil {
		t.Fatalf("ListSchedulesForDiscovery enabled=false filter: %v", err)
	}
	if disabledTotal == 0 || len(disabledRows) == 0 {
		t.Fatal("expected disabled schedules in filtered fixture")
	}
	for _, row := range disabledRows {
		if row.Schedule.Enabled {
			t.Fatalf("enabled schedule leaked into disabled filter: %s", row.Schedule.ID)
		}
	}
	assertScheduleDiscoveryRowsOrdered(t, disabledRows)

	page1, page1Total, err := repo.ListSchedulesForDiscovery(ctx, scheduleDiscoveryTargetProjectID, ScheduleDiscoveryFilter{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("ListSchedulesForDiscovery page1: %v", err)
	}
	page2, page2Total, err := repo.ListSchedulesForDiscovery(ctx, scheduleDiscoveryTargetProjectID, ScheduleDiscoveryFilter{Limit: 10, Offset: 10})
	if err != nil {
		t.Fatalf("ListSchedulesForDiscovery page2: %v", err)
	}
	if page1Total != page2Total || page1Total != 60 {
		t.Fatalf("page totals = %d/%d, want 60", page1Total, page2Total)
	}
	seen := map[string]bool{}
	for _, row := range page1 {
		seen[row.Schedule.ID] = true
	}
	for _, row := range page2 {
		if seen[row.Schedule.ID] {
			t.Fatalf("offset page repeated schedule %s", row.Schedule.ID)
		}
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name, description, repo_path) VALUES ('empty-schedule-project', 'Empty schedules', '', '')`); err != nil {
		t.Fatalf("insert empty project: %v", err)
	}
	emptyRows, emptyTotal, err := repo.ListSchedulesForDiscovery(ctx, "empty-schedule-project", ScheduleDiscoveryFilter{Limit: 50})
	if err != nil {
		t.Fatalf("ListSchedulesForDiscovery empty project: %v", err)
	}
	if emptyTotal != 0 || len(emptyRows) != 0 {
		t.Fatalf("empty project total/len = %d/%d, want 0/0", emptyTotal, len(emptyRows))
	}
}

func BenchmarkScheduleDiscoverySelect25kProject(b *testing.B) {
	db := testutil.NewTestDB(b)
	seedScheduleDiscoveryFixture(b, db, 5, 5000)

	legacyQuery := scheduleDiscoverySelectSQL(`t.project_id = ?`, false)
	optimizedQuery := scheduleDiscoverySelectSQL(`t.project_id = ?`, true)

	legacyPlan := explainScheduleDiscoveryQueryPlan(b, db, legacyQuery, scheduleDiscoveryTargetProjectID, 50, 0)
	if !strings.Contains(legacyPlan, "USE TEMP B-TREE FOR ORDER BY") {
		b.Fatalf("legacy plan = %s, want temporary ORDER BY sort", legacyPlan)
	}
	optimizedPlan := explainScheduleDiscoveryQueryPlan(b, db, optimizedQuery, scheduleDiscoveryTargetProjectID, 50)
	if strings.Contains(optimizedPlan, "USE TEMP B-TREE FOR ORDER BY") {
		b.Fatalf("optimized plan = %s, want no temporary ORDER BY sort", optimizedPlan)
	}
	if !strings.Contains(optimizedPlan, "idx_schedules_discovery_order") {
		b.Fatalf("optimized plan = %s, want ordered schedule discovery index", optimizedPlan)
	}

	b.Run("legacy_join_sort", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			rows, err := db.QueryContext(context.Background(), legacyQuery, scheduleDiscoveryTargetProjectID, 50, 0)
			if err != nil {
				b.Fatal(err)
			}
			if got := drainScheduleDiscoveryRows(b, rows); got != 50 {
				b.Fatalf("rows = %d, want 50", got)
			}
		}
	})
	b.Run("ordered_index_first_page", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			rows, err := db.QueryContext(context.Background(), optimizedQuery, scheduleDiscoveryTargetProjectID, 50)
			if err != nil {
				b.Fatal(err)
			}
			if got := drainScheduleDiscoveryRows(b, rows); got != 50 {
				b.Fatalf("rows = %d, want 50", got)
			}
		}
	})
}

const scheduleDiscoveryTargetProjectID = "schedule-discovery-target"

func seedScheduleDiscoveryFixture(tb testing.TB, db *sql.DB, projectCount, tasksPerProject int) {
	tb.Helper()
	if projectCount < 2 {
		tb.Fatalf("projectCount = %d, want at least 2", projectCount)
	}
	if tasksPerProject <= 0 {
		tb.Fatalf("tasksPerProject = %d, want positive", tasksPerProject)
	}
	ctx := context.Background()
	for p := 0; p < projectCount; p++ {
		projectID := fmt.Sprintf("schedule-discovery-project-%02d", p)
		if p == projectCount/2 {
			projectID = scheduleDiscoveryTargetProjectID
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO projects (id, name, description, repo_path) VALUES (?, ?, '', '')`,
			projectID, "Schedule Discovery "+projectID); err != nil {
			tb.Fatalf("insert project %s: %v", projectID, err)
		}
		if _, err := db.ExecContext(ctx, `
			WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < ? - 1)
			INSERT INTO tasks (id, project_id, title, category, priority, status, prompt, created_at, updated_at)
			SELECT
				'schedule-discovery-task-' || ? || '-' || printf('%05d', n),
				?,
				'schedule discovery ' || ? || ' task-' || printf('%05d', n),
				'scheduled',
				2,
				'pending',
				'p',
				datetime('2024-01-01', '+' || n || ' seconds'),
				datetime('2024-01-01', '+' || n || ' seconds')
			FROM seq`, tasksPerProject, projectID, projectID, projectID); err != nil {
			tb.Fatalf("insert tasks for %s: %v", projectID, err)
		}
		if _, err := db.ExecContext(ctx, `
			WITH RECURSIVE seq(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM seq WHERE n < ? - 1),
			slots(slot) AS (VALUES (0), (1), (2), (3), (4))
			INSERT INTO schedules
				(id, task_id, run_at, repeat_type, repeat_interval, enabled, clear_context_on_start, next_run, created_at, updated_at)
			SELECT
				'schedule-discovery-schedule-' || ? || '-' || printf('%05d', n) || '-' || slot,
				'schedule-discovery-task-' || ? || '-' || printf('%05d', n),
				datetime('2024-02-01', '+' || ((n * 5 + slot) * ? + ?) || ' seconds'),
				CASE WHEN slot = 0 THEN 'once' WHEN slot = 1 THEN 'daily' WHEN slot = 2 THEN 'weekly' WHEN slot = 3 THEN 'monthly' ELSE 'hours' END,
				1,
				CASE WHEN (n + slot) % 7 = 0 THEN 0 ELSE 1 END,
				CASE WHEN slot % 2 = 0 THEN 1 ELSE 0 END,
				CASE WHEN slot = 4 AND n % 10 = 0 THEN NULL ELSE datetime('2024-03-01', '+' || ((n * 5 + slot) * ? + ?) || ' seconds') END,
				datetime('2024-01-15', '+' || ((n * 5 + slot) * ? + ?) || ' seconds'),
				datetime('2024-01-16', '+' || ((n * 5 + slot) * ? + ?) || ' seconds')
			FROM seq CROSS JOIN slots`, tasksPerProject, projectID, projectID, projectCount, p, projectCount, p, projectCount, p, projectCount, p); err != nil {
			tb.Fatalf("insert schedules for %s: %v", projectID, err)
		}
	}
}

func explainScheduleDiscoveryQueryPlan(tb testing.TB, db *sql.DB, query string, args ...any) string {
	tb.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		tb.Fatalf("explain query plan: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			tb.Fatalf("scan explain row: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		tb.Fatalf("explain rows: %v", err)
	}
	return strings.Join(details, " | ")
}

func assertScheduleDiscoveryRowsOrdered(t *testing.T, rows []ScheduleDiscoveryRow) {
	t.Helper()
	for i := 1; i < len(rows); i++ {
		prev := rows[i-1].Schedule
		cur := rows[i].Schedule
		if scheduleDiscoveryRowLess(cur, prev) {
			t.Fatalf("row %d out of order: previous=%s current=%s", i, prev.ID, cur.ID)
		}
	}
}

func scheduleDiscoveryRowLess(a, b models.Schedule) bool {
	aNil := a.NextRun == nil
	bNil := b.NextRun == nil
	if aNil != bNil {
		return !aNil
	}
	if !aNil && !a.NextRun.Equal(*b.NextRun) {
		return a.NextRun.Before(*b.NextRun)
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID < b.ID
}

func drainScheduleDiscoveryRows(tb testing.TB, rows *sql.Rows) int {
	tb.Helper()
	defer rows.Close()
	count := 0
	for rows.Next() {
		var row ScheduleDiscoveryRow
		s := &row.Schedule
		if err := rows.Scan(&s.ID, &s.TaskID, &s.RunAt, &s.RepeatType, &s.RepeatInterval,
			&s.Enabled, &s.ClearContextOnStart, &s.NextRun, &s.LastRun, &s.CreatedAt, &s.UpdatedAt, &row.TaskTitle); err != nil {
			tb.Fatalf("scan schedule discovery row: %v", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		tb.Fatalf("schedule discovery rows: %v", err)
	}
	return count
}

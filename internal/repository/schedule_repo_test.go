package repository

import (
	"context"
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

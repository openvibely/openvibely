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

func TestScheduleRepo_CreateAndGetByID(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewScheduleRepo(db)
	ctx := context.Background()

	task := createTestTask(t, taskRepo)
	runAt := time.Now().Add(1 * time.Hour).Truncate(time.Second)

	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          runAt,
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
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

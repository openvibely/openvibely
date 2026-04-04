package service

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestSchedulerService_CheckDueTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create a scheduled task with a past due schedule
	task := &models.Task{
		ProjectID: "default",
		Title:     "Due Task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task)

	now := time.Now().UTC()
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now.Add(-1 * time.Minute),
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}
	scheduleRepo.Create(ctx, sched)

	// Run checkDueTasks
	svc.checkDueTasks(ctx)

	// Verify task was submitted
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected submitted task ID=%s, got %s", task.ID, submitted.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected due task to be submitted")
	}

	// Verify schedule was marked as ran
	updated, _ := scheduleRepo.GetByID(ctx, sched.ID)
	if updated.LastRun == nil {
		t.Error("expected LastRun to be set after checkDueTasks")
	}
}

func TestSchedulerService_CheckDueTasks_SkipsRunningTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create a task that's already running
	task := &models.Task{
		ProjectID: "default",
		Title:     "Running Task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusRunning,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task)

	now := time.Now().UTC()
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now.Add(-1 * time.Minute),
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}
	scheduleRepo.Create(ctx, sched)

	svc.checkDueTasks(ctx)

	select {
	case <-workerSvc.Submitted():
		t.Error("running task should not be submitted again")
	case <-time.After(100 * time.Millisecond):
		// Expected - not submitted
	}
}

func TestSchedulerService_CheckActiveTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create an active+pending task
	task := &models.Task{
		ProjectID: "default",
		Title:     "Active Pending",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task)

	// Create a backlog+pending task (should not be submitted)
	taskRepo.Create(ctx, &models.Task{
		ProjectID: "default",
		Title:     "Backlog Pending",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "test",
	})

	svc.checkActiveTasks(ctx)

	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.Title != "Active Pending" {
			t.Errorf("expected Active Pending, got %q", submitted.Title)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected active pending task to be submitted")
	}

	// Verify backlog task was NOT submitted
	select {
	case submitted := <-workerSvc.Submitted():
		t.Errorf("backlog task should not be submitted, got %q", submitted.Title)
	case <-time.After(100 * time.Millisecond):
		// Expected
	}
}

func TestSchedulerService_CheckActiveTasks_NoDuplicateSubmission(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create multiple active+pending tasks
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	task3 := &models.Task{ProjectID: "default", Title: "Task 3", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task1)
	taskRepo.Create(ctx, task2)
	taskRepo.Create(ctx, task3)

	// First call submits all 3
	svc.checkActiveTasks(ctx)

	// Drain the channel
	drained := 0
	for i := 0; i < 3; i++ {
		select {
		case <-workerSvc.Submitted():
			drained++
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("expected 3 tasks on first call, got %d", drained)
		}
	}

	// Second call should NOT re-submit (dedup prevents it, tasks still in pending map)
	// Simulate that the worker hasn't processed them yet by not clearing pending
	svc.checkActiveTasks(ctx)

	select {
	case got := <-workerSvc.Submitted():
		t.Errorf("expected no duplicates on second call, got task %q", got.Title)
	case <-time.After(100 * time.Millisecond):
		// Expected - dedup prevents re-submission
	}
}

func TestSchedulerService_CheckDueTasks_RepeatingSchedule(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	task := &models.Task{
		ProjectID: "default",
		Title:     "Daily Task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task)

	now := time.Now().UTC()
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now.Add(-1 * time.Minute),
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}
	scheduleRepo.Create(ctx, sched)

	svc.checkDueTasks(ctx)

	// Drain submission
	select {
	case <-workerSvc.Submitted():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected task to be submitted")
	}

	// Verify next_run was set for the future
	updated, _ := scheduleRepo.GetByID(ctx, sched.ID)
	if updated.NextRun == nil {
		t.Fatal("expected NextRun to be set for daily schedule")
	}
	if !updated.NextRun.After(now) {
		t.Error("expected NextRun to be in the future")
	}
}

// TestSchedulerService_DailyScheduleFiresSameDay verifies that a daily schedule
// with next_run set to the current day's run time (even if barely in the past)
// is picked up by the scheduler. This is the core bug fix: previously the handler
// pre-advanced next_run to the NEXT day, so a daily 1:33 AM schedule created at
// 1:34 AM would not run until tomorrow.
func TestSchedulerService_DailyScheduleFiresSameDay(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	task := &models.Task{
		ProjectID: "default",
		Title:     "Daily 1:33 AM",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "test daily schedule",
	}
	taskRepo.Create(ctx, task)

	now := time.Now().UTC()
	// Simulate: next_run is 1 minute in the past (schedule was just missed)
	// With the fix, the handler no longer pre-advances to tomorrow
	pastRunTime := now.Add(-1 * time.Minute)
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          pastRunTime,
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
		NextRun:        &pastRunTime, // next_run = run_at (not pre-advanced)
	}
	// Use Create which sets NextRun = RunAt if nil
	scheduleRepo.Create(ctx, sched)

	// The scheduler should find this schedule as due (next_run <= now)
	svc.checkDueTasks(ctx)

	// Verify task was submitted
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected submitted task ID=%s, got %s", task.ID, submitted.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("daily schedule with next_run 1 minute ago should be submitted")
	}

	// Verify next_run was advanced to tomorrow (not today)
	updated, _ := scheduleRepo.GetByID(ctx, sched.ID)
	if updated.NextRun == nil {
		t.Fatal("expected NextRun to be set after execution")
	}
	if !updated.NextRun.After(now) {
		t.Error("expected NextRun to be in the future after execution")
	}
}

// TestSchedulerService_ScheduledTaskResetsCompletedStatus verifies that the
// scheduler resets a completed/failed task to pending before submitting it.
// This is important for repeating schedules where the task runs multiple times.
func TestSchedulerService_ScheduledTaskResetsCompletedStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create a task that previously completed
	task := &models.Task{
		ProjectID: "default",
		Title:     "Previously Completed",
		Category:  models.CategoryScheduled,
		Status:    models.StatusCompleted,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task)

	now := time.Now().UTC()
	pastTime := now.Add(-30 * time.Second)
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          pastTime,
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}
	scheduleRepo.Create(ctx, sched)

	svc.checkDueTasks(ctx)

	// Should be submitted even though status was "completed"
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected task %s, got %s", task.ID, submitted.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("completed task with due schedule should be reset to pending and submitted")
	}

	// Verify status was reset to pending
	dbTask, _ := taskRepo.GetByID(ctx, task.ID)
	if dbTask.Status != models.StatusPending {
		t.Errorf("expected status=pending, got %s", dbTask.Status)
	}
}

func TestSchedulerService_StartupCatchesMissedSchedules(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	now := time.Now().UTC()

	// Create multiple tasks that should have run while the app was "down"
	task1 := &models.Task{
		ProjectID: "default",
		Title:     "Missed Once",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task1)
	sched1 := &models.Schedule{
		TaskID:         task1.ID,
		RunAt:          now.Add(-2 * time.Hour), // 2 hours ago
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}
	scheduleRepo.Create(ctx, sched1)

	task2 := &models.Task{
		ProjectID: "default",
		Title:     "Missed Daily",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task2)
	sched2 := &models.Schedule{
		TaskID:         task2.ID,
		RunAt:          now.Add(-3 * 24 * time.Hour), // 3 days ago
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}
	scheduleRepo.Create(ctx, sched2)

	// Simulate app startup: checkDueTasks runs immediately
	svc.checkDueTasks(ctx)

	// Both missed tasks should be submitted
	submitted := make(map[string]bool)
	for i := 0; i < 2; i++ {
		select {
		case task := <-workerSvc.Submitted():
			submitted[task.ID] = true
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("expected 2 missed tasks to be submitted, got %d", i)
		}
	}

	if !submitted[task1.ID] {
		t.Error("expected missed one-time task to be submitted on startup")
	}
	if !submitted[task2.ID] {
		t.Error("expected missed daily task to be submitted on startup")
	}

	// Verify one-time schedule has no next run
	updated1, _ := scheduleRepo.GetByID(ctx, sched1.ID)
	if updated1.NextRun != nil {
		t.Error("expected one-time schedule to have nil NextRun after execution")
	}

	// Verify daily schedule has next run in the future
	updated2, _ := scheduleRepo.GetByID(ctx, sched2.ID)
	if updated2.NextRun == nil {
		t.Fatal("expected daily schedule to have NextRun set")
	}
	if !updated2.NextRun.After(now) {
		t.Error("expected daily schedule NextRun to be in the future, not catching up on all missed occurrences")
	}
}

func TestSchedulerService_DragDropReschedule_DoesNotExecuteCompletedTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create a completed one-time scheduled task (simulating a task that was already executed)
	task := &models.Task{
		ProjectID: "default",
		Title:     "Completed Task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusCompleted,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task)

	now := time.Now().UTC()
	// Create a one-time schedule with next_run in the past (simulating drag/drop to past time)
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now.Add(-1 * time.Hour),
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}
	pastTime := now.Add(-30 * time.Minute)
	sched.NextRun = &pastTime
	scheduleRepo.Create(ctx, sched)

	// Run checkDueTasks - this should NOT execute the task
	svc.checkDueTasks(ctx)

	// Verify task was NOT submitted
	select {
	case submitted := <-workerSvc.Submitted():
		t.Fatalf("expected no task submission for completed one-time schedule, but got task ID=%s", submitted.ID)
	case <-time.After(100 * time.Millisecond):
		// Expected - no submission
	}

	// Verify task status is still completed
	updatedTask, _ := taskRepo.GetByID(ctx, task.ID)
	if updatedTask.Status != models.StatusCompleted {
		t.Errorf("expected task status to remain completed, got %s", updatedTask.Status)
	}
}

func TestSchedulerService_RecurringSchedule_ExecutesCompletedTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create a completed daily scheduled task (completed yesterday, should run again today)
	task := &models.Task{
		ProjectID: "default",
		Title:     "Daily Task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusCompleted,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task)

	now := time.Now().UTC()
	// Create a daily schedule with next_run due now
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now.Add(-24 * time.Hour),
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}
	scheduleRepo.Create(ctx, sched)

	// Run checkDueTasks - this SHOULD execute the task for recurring schedules
	svc.checkDueTasks(ctx)

	// Verify task WAS submitted
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected submitted task ID=%s, got %s", task.ID, submitted.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected daily recurring task to be submitted even though it was completed")
	}

	// Verify task status was reset to pending
	updatedTask, _ := taskRepo.GetByID(ctx, task.ID)
	if updatedTask.Status != models.StatusPending {
		t.Errorf("expected task status to be reset to pending, got %s", updatedTask.Status)
	}
}

// TestSchedulerService_RecoverStaleQueuedTasks verifies that checkActiveTasks
// recovers tasks stuck in "queued" status for longer than the stale timeout.
// This handles the case where a thread follow-up goroutine crashed without
// cleaning up the task status.
func TestSchedulerService_RecoverStaleQueuedTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create a task with "queued" status in active category
	task := &models.Task{
		ProjectID: "default",
		Title:     "Stale Queued Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending, // Create as pending first (CHECK constraint)
		Prompt:    "test prompt",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Set status to queued
	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusQueued); err != nil {
		t.Fatalf("UpdateStatus to queued: %v", err)
	}

	// Manually set updated_at to the past to simulate a stale task
	_, err := db.ExecContext(ctx,
		`UPDATE tasks SET updated_at = datetime('now', '-15 minutes') WHERE id = ?`, task.ID)
	if err != nil {
		t.Fatalf("Set stale updated_at: %v", err)
	}

	// Run checkActiveTasks — should recover the stale queued task
	svc.checkActiveTasks(ctx)

	// Verify task was reset to pending
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updatedTask.Status != models.StatusPending {
		t.Errorf("expected stale queued task to be reset to pending, got %s", updatedTask.Status)
	}

	// Verify it was submitted to the worker
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected submitted task ID=%s, got %s", task.ID, submitted.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected stale queued task to be submitted")
	}
}

// TestSchedulerService_DoesNotRecoverRecentQueuedTasks verifies that
// checkActiveTasks does NOT reset tasks that recently entered "queued" status.
// These tasks are actively being handled by thread follow-up goroutines.
func TestSchedulerService_DoesNotRecoverRecentQueuedTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create a task with "queued" status — recently updated (not stale)
	task := &models.Task{
		ProjectID: "default",
		Title:     "Recent Queued Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Set status to queued (updated_at is set to now automatically)
	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusQueued); err != nil {
		t.Fatalf("UpdateStatus to queued: %v", err)
	}

	// Run checkActiveTasks — should NOT recover the recent queued task
	svc.checkActiveTasks(ctx)

	// Verify task is still queued (not reset to pending)
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updatedTask.Status != models.StatusQueued {
		t.Errorf("expected recent queued task to remain queued, got %s", updatedTask.Status)
	}

	// Verify nothing was submitted to the worker (only pending tasks are submitted)
	select {
	case <-workerSvc.Submitted():
		t.Fatal("expected no tasks to be submitted for recent queued task")
	case <-time.After(100 * time.Millisecond):
		// Good — no submission
	}
}

func TestSchedulerService_WorktreeCleanupIntegration(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	// Create worktree service
	worktreeSvc := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	// Create scheduler service and wire worktree service
	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	svc.SetWorktreeService(worktreeSvc)

	// Set cleanup policy
	if err := settingsRepo.Set(ctx, "worktree_cleanup", "after_merge"); err != nil {
		t.Fatal(err)
	}

	// Test that checkWorktreeCleanup can be called without error
	// (actual cleanup functionality is tested in worktree_service_test.go)
	svc.checkWorktreeCleanup(ctx)

	// Verify lastCleanupAt was set
	if svc.lastCleanupAt.IsZero() {
		t.Error("expected lastCleanupAt to be set after cleanup check")
	}

	// Test with nil worktree service (should not panic)
	svc2 := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	svc2.checkWorktreeCleanup(ctx) // Should return immediately

	if !svc2.lastCleanupAt.IsZero() {
		t.Error("expected lastCleanupAt to remain zero with nil worktree service")
	}
}

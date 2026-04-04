package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

// TestUpdateSchedule_CompletedTaskToFuture verifies that when a task has already
// run and completed, updating its schedule to a future time resets the task status
// to 'pending' so it can run again. This is a regression test for the bug where
// completed tasks wouldn't re-run after schedule changes.
func TestUpdateSchedule_CompletedTaskToFuture(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)

	// Create a task with completed status (simulating a task that already ran)
	task := &models.Task{
		Title:      "Completed Task",
		ProjectID:  "default",
		Category:   models.CategoryScheduled,
		Status:     models.StatusCompleted,
		Prompt:     "test prompt",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Create a RepeatOnce schedule that already ran (NextRun = nil, LastRun set)
	yesterday := time.Now().UTC().AddDate(0, 0, -1)
	runAt := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 14, 0, 0, 0, time.UTC)

	schedule := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          runAt,
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}

	if err := scheduleRepo.Create(context.Background(), schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	// Mark it as ran (simulating scheduler execution)
	lastRun := time.Now().UTC().Add(-1 * time.Hour)
	nextRun := schedule.ComputeNextRun(lastRun) // For RepeatOnce, this returns nil
	if err := scheduleRepo.MarkRan(context.Background(), schedule.ID, lastRun, nextRun); err != nil {
		t.Fatalf("failed to mark as ran: %v", err)
	}

	// Verify initial state
	schedule, err := scheduleRepo.GetByID(context.Background(), schedule.ID)
	if err != nil {
		t.Fatalf("failed to reload schedule: %v", err)
	}
	if schedule.NextRun != nil {
		t.Fatalf("expected NextRun to be nil for completed RepeatOnce, got %v", schedule.NextRun)
	}
	if schedule.LastRun == nil {
		t.Fatal("expected LastRun to be set")
	}

	task, err = taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("failed to reload task: %v", err)
	}
	if task.Status != models.StatusCompleted {
		t.Fatalf("expected task status to be completed, got %s", task.Status)
	}

	// Now update the schedule to a future time
	tomorrow := time.Now().UTC().AddDate(0, 0, 1)
	futureRunAt := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 15, 30, 0, 0, time.UTC)

	// Set up the handler with taskSvc (needed for resetting task status)
	h := &Handler{
		scheduleRepo: scheduleRepo,
		taskSvc:      service.NewTaskService(taskRepo, nil, nil),
	}

	e := echo.New()
	formData := url.Values{}
	formData.Set("run_at", futureRunAt.In(time.Local).Format("2006-01-02T15:04"))
	formData.Set("repeat_type", string(models.RepeatOnce))
	formData.Set("repeat_interval", "1")

	req := httptest.NewRequest(http.MethodPost, "/schedules/"+schedule.ID, strings.NewReader(formData.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(schedule.ID)

	// Call UpdateSchedule
	err = h.UpdateSchedule(c)
	if err != nil {
		t.Fatalf("UpdateSchedule failed: %v", err)
	}

	// Verify schedule was updated correctly
	updated, err := scheduleRepo.GetByID(context.Background(), schedule.ID)
	if err != nil {
		t.Fatalf("failed to reload after update: %v", err)
	}

	if updated.NextRun == nil {
		t.Fatal("expected NextRun to be set to the future time")
	}
	// Compare absolute times (both represent the same instant regardless of timezone)
	if !updated.NextRun.Equal(futureRunAt) {
		t.Errorf("expected NextRun to be %v, got %v (local: %v)", futureRunAt, updated.NextRun, updated.NextRun.Local())
	}

	// CRITICAL: Verify task status was reset to pending so it can run again
	updatedTask, err := taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("failed to reload task after schedule update: %v", err)
	}

	if updatedTask.Status != models.StatusPending {
		t.Errorf("BUG: task status should be reset to 'pending' when schedule is updated to future, got '%s'", updatedTask.Status)
		t.Log("This means the scheduler won't be able to run this task because ClaimTask only works on pending tasks")
	}

	t.Logf("Schedule updated successfully: NextRun=%v, TaskStatus=%s", updated.NextRun, updatedTask.Status)
}

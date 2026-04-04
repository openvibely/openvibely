package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

// TestHandler_RescheduleTask_RecurringTaskPreservesNewTime verifies that when
// a recurring task is rescheduled via drag-and-drop, the new time is preserved
// for future occurrences
func TestHandler_RescheduleTask_RecurringTaskPreservesNewTime(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{
		Name:        "Test Project",
		Description: "For testing reschedule",
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task with a daily schedule at 3:00 PM
	task := &models.Task{
		Title:      "Daily Task",
		Prompt:     "Test daily task",
		Category:   models.CategoryScheduled,
		Status:     models.StatusPending,
		ProjectID:  project.ID,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Create a daily schedule for 3:00 PM
	originalTime := time.Date(2026, 2, 20, 15, 0, 0, 0, time.UTC)
	schedule := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          originalTime,
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}
	if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	// Verify initial schedule
	if schedule.RunAt.Hour() != 15 {
		t.Errorf("initial RunAt hour = %d, want 15", schedule.RunAt.Hour())
	}

	// Reschedule to 5:00 PM on Feb 22
	newDate := "2026-02-22"
	newHour := "17"

	formData := url.Values{}
	formData.Set("new_date", newDate)
	formData.Set("hour", newHour)

	req := httptest.NewRequest(http.MethodPatch, "/schedules/"+schedule.ID+"/reschedule", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 No Content, got %d", rec.Code)
	}

	// Verify the schedule was updated
	updatedSchedule, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("failed to get updated schedule: %v", err)
	}

	// Verify RunAt was updated to 5:00 PM local time (hour form value is local)
	if updatedSchedule.RunAt.Local().Hour() != 17 {
		t.Errorf("after reschedule, RunAt local hour = %d, want 17", updatedSchedule.RunAt.Local().Hour())
	}

	// Verify NextRun is also 5:00 PM local on Feb 22
	if updatedSchedule.NextRun == nil {
		t.Fatal("NextRun is nil after reschedule")
	}
	if updatedSchedule.NextRun.Local().Hour() != 17 {
		t.Errorf("after reschedule, NextRun local hour = %d, want 17", updatedSchedule.NextRun.Local().Hour())
	}

	// Verify that ComputeNextRun uses the NEW time (5:00 PM local), not the old time
	// Use the actual stored RunAt time as the execution time (simulating on-time execution)
	executionTime := updatedSchedule.RunAt
	nextRun := updatedSchedule.ComputeNextRun(executionTime)

	if nextRun == nil {
		t.Fatal("ComputeNextRun returned nil for daily schedule")
	}

	// Next run should be 5:00 PM local on Feb 23
	expectedNextRun := time.Date(2026, 2, 23, 17, 0, 0, 0, time.Local)
	if !nextRun.Equal(expectedNextRun) {
		t.Errorf("ComputeNextRun after reschedule = %v (local: %v), want %v",
			nextRun, nextRun.Local(), expectedNextRun)
	}
}

// TestHandler_RescheduleTask_OneTimeTask verifies that one-time tasks
// are also rescheduled correctly
func TestHandler_RescheduleTask_OneTimeTask(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{
		Name:        "Test Project",
		Description: "For testing one-time reschedule",
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task with a one-time schedule
	task := &models.Task{
		Title:      "One-time Task",
		Prompt:     "Test one-time task",
		Category:   models.CategoryScheduled,
		Status:     models.StatusPending,
		ProjectID:  project.ID,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Create a one-time schedule
	originalTime := time.Date(2026, 2, 20, 15, 0, 0, 0, time.UTC)
	schedule := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          originalTime,
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}
	if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	// Reschedule to 5:00 PM on Feb 22
	newDate := "2026-02-22"
	newHour := "17"

	formData := url.Values{}
	formData.Set("new_date", newDate)
	formData.Set("hour", newHour)

	req := httptest.NewRequest(http.MethodPatch, "/schedules/"+schedule.ID+"/reschedule", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 No Content, got %d", rec.Code)
	}

	// Verify the schedule was updated
	updatedSchedule, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("failed to get updated schedule: %v", err)
	}

	// Verify RunAt was updated (hour from form is local time)
	expected := time.Date(2026, 2, 22, 17, 0, 0, 0, time.Local)
	if !updatedSchedule.RunAt.Equal(expected) {
		t.Errorf("after reschedule, RunAt = %v (local: %v), want %v", updatedSchedule.RunAt, updatedSchedule.RunAt.Local(), expected)
	}

	// Verify NextRun was also updated
	if updatedSchedule.NextRun == nil {
		t.Fatal("NextRun is nil after reschedule")
	}
	if !updatedSchedule.NextRun.Equal(expected) {
		t.Errorf("after reschedule, NextRun = %v (local: %v), want %v", updatedSchedule.NextRun, updatedSchedule.NextRun.Local(), expected)
	}

	// Verify ComputeNextRun returns nil for one-time tasks
	nextRun := updatedSchedule.ComputeNextRun(expected)
	if nextRun != nil {
		t.Errorf("ComputeNextRun for one-time task = %v, want nil", nextRun)
	}
}

// TestHandler_RescheduleTask_DoesNotChangeTaskStatus verifies that drag-and-drop
// reschedule only updates the schedule time without modifying task status.
// This prevents accidental task execution triggered by the status reset.
func TestHandler_RescheduleTask_DoesNotChangeTaskStatus(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{
		Name:        "Test Project",
		Description: "For testing reschedule status",
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Test with various initial task statuses to ensure none are changed
	for _, initialStatus := range []models.TaskStatus{
		models.StatusPending,
		models.StatusCompleted,
		models.StatusFailed,
	} {
		t.Run(string(initialStatus), func(t *testing.T) {
			task := &models.Task{
				Title:     "Task with status " + string(initialStatus),
				Prompt:    "Test task",
				Category:  models.CategoryScheduled,
				Status:    initialStatus,
				ProjectID: project.ID,
			}
			if err := h.taskSvc.Create(ctx, task); err != nil {
				t.Fatalf("failed to create task: %v", err)
			}

			// Set initial status (Create may normalize it)
			if err := h.taskSvc.UpdateStatus(ctx, task.ID, initialStatus); err != nil {
				t.Fatalf("failed to set initial status: %v", err)
			}

			schedule := &models.Schedule{
				TaskID:         task.ID,
				RunAt:          time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC),
				RepeatType:     models.RepeatDaily,
				RepeatInterval: 1,
				Enabled:        true,
			}
			if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
				t.Fatalf("failed to create schedule: %v", err)
			}

			// Reschedule to a far-future date to ensure NextRun > now
			formData := url.Values{}
			formData.Set("new_date", "2030-06-15")
			formData.Set("hour", "14")

			req := httptest.NewRequest(http.MethodPatch, "/schedules/"+schedule.ID+"/reschedule", strings.NewReader(formData.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("HX-Request", "true")
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("expected 204, got %d", rec.Code)
			}

			// Verify task status was NOT changed
			updatedTask, err := h.taskSvc.GetByID(ctx, task.ID)
			if err != nil {
				t.Fatalf("failed to get task: %v", err)
			}
			if updatedTask.Status != initialStatus {
				t.Errorf("task status changed from %s to %s; drag-and-drop reschedule should not modify task status",
					initialStatus, updatedTask.Status)
			}
		})
	}
}

// TestHandler_RescheduleTask_DoesNotSubmitToWorker verifies that drag-and-drop
// reschedule does not submit the task to the worker pool for execution.
func TestHandler_RescheduleTask_DoesNotSubmitToWorker(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{
		Name:        "Test Project",
		Description: "For testing no worker submission",
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		Title:     "Completed Scheduled Task",
		Prompt:    "Should not execute on reschedule",
		Category:  models.CategoryScheduled,
		Status:    models.StatusCompleted,
		ProjectID: project.ID,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	if err := h.taskSvc.UpdateStatus(ctx, task.ID, models.StatusCompleted); err != nil {
		t.Fatalf("failed to set completed status: %v", err)
	}

	schedule := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC),
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}
	if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	// Reschedule to a future time
	formData := url.Values{}
	formData.Set("new_date", "2030-06-15")
	formData.Set("hour", "14")

	req := httptest.NewRequest(http.MethodPatch, "/schedules/"+schedule.ID+"/reschedule", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	// Verify task status is still completed (not reset to pending)
	updatedTask, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updatedTask.Status != models.StatusCompleted {
		t.Errorf("task status = %s, want completed; reschedule should not trigger execution", updatedTask.Status)
	}

	// Verify the worker queue is empty (no task was submitted)
	if h.workerSvc.QueueSize() != 0 {
		t.Errorf("worker queue size = %d, want 0; reschedule should not submit tasks", h.workerSvc.QueueSize())
	}
}

// TestHandler_RescheduleTask_PastTimeAdjustsToNextOccurrence verifies that when
// a user drags a task to a past time slot, the handler automatically computes the
// next future occurrence to prevent immediate execution by the scheduler.
func TestHandler_RescheduleTask_PastTimeAdjustsToNextOccurrence(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{
		Name:        "Test Project",
		Description: "For testing past time adjustment",
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		Title:     "Daily Task",
		Prompt:    "Should not execute immediately",
		Category:  models.CategoryScheduled,
		Status:    models.StatusCompleted,
		ProjectID: project.ID,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Create a daily schedule
	schedule := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          time.Now().Add(24 * time.Hour), // Tomorrow
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}
	if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	// Drag to a past date (1 week ago) at 4:00 PM
	pastDate := time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	formData := url.Values{}
	formData.Set("new_date", pastDate)
	formData.Set("hour", "16")

	req := httptest.NewRequest(http.MethodPatch, "/schedules/"+schedule.ID+"/reschedule", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	// Verify the schedule's NextRun was adjusted to a FUTURE time
	updatedSchedule, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("failed to get schedule: %v", err)
	}

	if updatedSchedule.NextRun == nil {
		t.Fatal("NextRun is nil after reschedule")
	}

	now := time.Now()
	if !updatedSchedule.NextRun.After(now) {
		t.Errorf("NextRun = %v (should be in the future), but now = %v", updatedSchedule.NextRun, now)
		t.Errorf("Drag/drop to past time should adjust NextRun to next future occurrence, not trigger immediate execution")
	}

	// Verify the hour is preserved (4:00 PM local time)
	if updatedSchedule.NextRun.Local().Hour() != 16 {
		t.Errorf("NextRun local hour = %d, want 16 (hour from drag should be preserved)", updatedSchedule.NextRun.Local().Hour())
	}

	// Verify task status is still completed (not reset to pending)
	updatedTask, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updatedTask.Status != models.StatusCompleted {
		t.Errorf("task status = %s, want completed; reschedule should not reset status", updatedTask.Status)
	}
}

// TestScheduler_NoTimeDrift verifies that the scheduler doesn't cause time drift
// when executing recurring tasks
func TestScheduler_NoTimeDrift(t *testing.T) {
	// Create a daily schedule for 3:00 PM
	runAt := time.Date(2026, 2, 20, 15, 0, 0, 0, time.UTC)
	schedule := &models.Schedule{
		TaskID:         "test-task",
		RunAt:          runAt,
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}

	// Simulate scheduler executing task 5 seconds late
	executionTime := time.Date(2026, 2, 22, 15, 0, 5, 0, time.UTC)
	nextRun := schedule.ComputeNextRun(executionTime)

	if nextRun == nil {
		t.Fatal("expected next run, got nil")
	}

	// Next run should still be 3:00 PM tomorrow, not 3:00:05 PM
	expected := time.Date(2026, 2, 23, 15, 0, 0, 0, time.UTC)
	if !nextRun.Equal(expected) {
		t.Errorf("time drift detected: got %v, want %v", nextRun, expected)
		t.Errorf("time-of-day shifted from %02d:%02d:%02d to %02d:%02d:%02d",
			runAt.Hour(), runAt.Minute(), runAt.Second(),
			nextRun.Hour(), nextRun.Minute(), nextRun.Second())
	}

	// Verify after multiple executions, time stays consistent
	currentRun := *nextRun
	for i := 0; i < 10; i++ {
		// Simulate each execution being a few seconds late
		execTime := currentRun.Add(time.Duration(i+1) * time.Second)
		nextRun = schedule.ComputeNextRun(execTime)
		if nextRun == nil {
			t.Fatalf("iteration %d: unexpected nil next run", i)
		}
		// Time should always be 3:00 PM
		if nextRun.Hour() != 15 || nextRun.Minute() != 0 || nextRun.Second() != 0 {
			t.Errorf("iteration %d: time drifted to %02d:%02d:%02d, want 15:00:00",
				i, nextRun.Hour(), nextRun.Minute(), nextRun.Second())
		}
		currentRun = *nextRun
	}
}

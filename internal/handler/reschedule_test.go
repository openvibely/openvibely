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
		Title:     "Daily Task",
		Prompt:    "Test daily task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		ProjectID: project.ID,
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
		Title:     "One-time Task",
		Prompt:    "Test one-time task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		ProjectID: project.ID,
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

func TestHandler_RescheduleTask_MultiSelectMovesEveryScheduleTogether(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := &models.Project{Name: "Grouped schedule project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	makeSchedule := func(title string, hour, minute int) *models.Schedule {
		t.Helper()
		task := &models.Task{Title: title, Prompt: "test", Category: models.CategoryScheduled, Status: models.StatusPending, ProjectID: project.ID}
		if err := h.taskSvc.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
		runAt := time.Date(2031, 4, 8, hour, minute, 0, 0, time.Local).UTC()
		schedule := &models.Schedule{TaskID: task.ID, RunAt: runAt, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
		if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
			t.Fatal(err)
		}
		return schedule
	}
	first := makeSchedule("Grouped first", 9, 15)
	second := makeSchedule("Grouped second", 11, 45)

	form := url.Values{
		"new_date": {"2031-04-09"}, "hour": {"12"},
		"source_date": {"2031-04-08"}, "source_hour": {"9"},
		"schedule_ids": {first.ID + "," + second.ID}, "project_id": {project.ID},
	}
	req := httptest.NewRequest(http.MethodPatch, "/schedules/"+first.ID+"/reschedule?project_id="+url.QueryEscape(project.ID), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	updatedFirst, _ := h.scheduleRepo.GetByID(ctx, first.ID)
	updatedSecond, _ := h.scheduleRepo.GetByID(ctx, second.ID)
	if got := updatedFirst.RunAt.Local(); got.Day() != 9 || got.Hour() != 12 || got.Minute() != 15 {
		t.Fatalf("anchor moved to %v, want Apr 9 12:15", got)
	}
	if got := updatedSecond.RunAt.Local(); got.Day() != 9 || got.Hour() != 14 || got.Minute() != 45 {
		t.Fatalf("second schedule moved to %v, want Apr 9 14:45", got)
	}
}

func TestHandler_RescheduleTask_MultiSelectPreservesLocalHourAcrossDST(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	previousLocal := time.Local
	time.Local = location
	t.Cleanup(func() { time.Local = previousLocal })

	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := &models.Project{Name: "DST grouped schedules"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	makeSchedule := func(title string) *models.Schedule {
		t.Helper()
		task := &models.Task{Title: title, Prompt: "test", Category: models.CategoryScheduled, Status: models.StatusPending, ProjectID: project.ID}
		if err := h.taskSvc.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
		runAt := time.Date(2031, 3, 9, 1, 30, 0, 0, location).UTC()
		schedule := &models.Schedule{TaskID: task.ID, RunAt: runAt, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
		if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
			t.Fatal(err)
		}
		return schedule
	}
	first := makeSchedule("DST first")
	second := makeSchedule("DST second")

	form := url.Values{
		"new_date": {"2031-03-09"}, "hour": {"3"},
		"source_date": {"2031-03-09"}, "source_hour": {"1"},
		"schedule_ids": {first.ID + "," + second.ID}, "project_id": {project.ID},
	}
	req := httptest.NewRequest(http.MethodPatch, "/schedules/"+first.ID+"/reschedule?project_id="+url.QueryEscape(project.ID), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	for _, id := range []string{first.ID, second.ID} {
		stored, err := h.scheduleRepo.GetByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got := stored.RunAt.In(location); got.Hour() != 3 || got.Minute() != 30 {
			t.Fatalf("schedule %s moved to local %v, want 03:30 across spring-forward", id, got)
		}
	}
}

func TestHandler_RescheduleTask_MultiSelectMovesDisabledAndRejectsForeignWithoutMutation(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	ownedProject := &models.Project{Name: "Owned schedules"}
	foreignProject := &models.Project{Name: "Foreign schedules"}
	if err := h.projectSvc.Create(ctx, ownedProject); err != nil {
		t.Fatal(err)
	}
	if err := h.projectSvc.Create(ctx, foreignProject); err != nil {
		t.Fatal(err)
	}

	makeSchedule := func(projectID, title string, enabled bool) *models.Schedule {
		t.Helper()
		task := &models.Task{Title: title, Prompt: "test", Category: models.CategoryScheduled, Status: models.StatusPending, ProjectID: projectID}
		if err := h.taskSvc.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
		runAt := time.Date(2031, 5, 10, 8, 30, 0, 0, time.Local).UTC()
		s := &models.Schedule{TaskID: task.ID, RunAt: runAt, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: enabled}
		if err := h.scheduleRepo.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	owned := makeSchedule(ownedProject.ID, "Owned", true)
	disabled := makeSchedule(ownedProject.ID, "Disabled", false)
	foreign := makeSchedule(foreignProject.ID, "Foreign", true)
	original := owned.RunAt

	form := url.Values{"new_date": {"2031-05-11"}, "hour": {"10"}, "source_date": {"2031-05-10"}, "source_hour": {"8"}, "schedule_ids": {owned.ID + "," + disabled.ID}, "project_id": {ownedProject.ID}}
	req := httptest.NewRequest(http.MethodPatch, "/schedules/"+owned.ID+"/reschedule?project_id="+url.QueryEscape(ownedProject.ID), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected mixed enabled/disabled grouped request to succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	movedDisabled, _ := h.scheduleRepo.GetByID(ctx, disabled.ID)
	if movedDisabled.Enabled {
		t.Fatal("dragging a disabled schedule must not enable it")
	}
	if got := movedDisabled.RunAt.Local(); got.Day() != 11 || got.Hour() != 10 || got.Minute() != 30 {
		t.Fatalf("disabled schedule moved to %v, want May 11 10:30", got)
	}

	movedOwned, _ := h.scheduleRepo.GetByID(ctx, owned.ID)
	beforeForeignFailure := movedOwned.RunAt
	form.Set("schedule_ids", owned.ID+","+foreign.ID)
	req = httptest.NewRequest(http.MethodPatch, "/schedules/"+owned.ID+"/reschedule?project_id="+url.QueryEscape(ownedProject.ID), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code == http.StatusNoContent {
		t.Fatal("expected foreign grouped request to fail")
	}
	stored, _ := h.scheduleRepo.GetByID(ctx, owned.ID)
	if !stored.RunAt.Equal(beforeForeignFailure) {
		t.Fatalf("foreign grouped request partially moved owned schedule to %v", stored.RunAt)
	}
	if stored.RunAt.Equal(original) {
		t.Fatal("fixture did not first move the owned schedule")
	}
}

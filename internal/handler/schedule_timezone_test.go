package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
)

func TestCreateSchedule_TimezoneHandling(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a test project
	project := &models.Project{
		Name: "Test Project",
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("Failed to create test project: %v", err)
	}

	// Create a test task
	task := &models.Task{
		Title:     "Test Task",
		ProjectID: project.ID,
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("Failed to create test task: %v", err)
	}

	// Test case: User creates a schedule for a FUTURE date
	// Use a date 3 days from now to ensure it's always in the future
	futureDate := time.Now().Add(72 * time.Hour)
	localTimeStr := time.Date(futureDate.Year(), futureDate.Month(), futureDate.Day(), 17, 0, 0, 0, time.Local).Format("2006-01-02T15:04")

	// Create the schedule via HTTP request
	form := url.Values{}
	form.Set("run_at", localTimeStr)
	form.Set("repeat_type", "weekly")
	form.Set("repeat_interval", "1")

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/tasks/%s/schedules", task.ID), strings.NewReader(form.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.CreateSchedule(c); err != nil {
		t.Fatalf("CreateSchedule failed: %v", err)
	}

	// Fetch the created schedule
	schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("Failed to list schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("Expected 1 schedule, got %d", len(schedules))
	}

	schedule := schedules[0]

	actualRunAtLocal := schedule.RunAt.In(time.Local)
	actualNextRunLocal := schedule.NextRun.In(time.Local)

	// Check that RunAt matches what the user intended (local time)
	if actualRunAtLocal.Format("2006-01-02T15:04") != localTimeStr {
		t.Errorf("Schedule RunAt in local time: got %s, want %s",
			actualRunAtLocal.Format("2006-01-02T15:04"), localTimeStr)
		t.Logf("  RunAt stored as: %v", schedule.RunAt)
		t.Logf("  RunAt in Local:  %v", actualRunAtLocal)
	}

	// For a future RunAt, NextRun should equal RunAt
	if actualNextRunLocal.Format("2006-01-02T15:04") != localTimeStr {
		t.Errorf("Schedule NextRun in local time: got %s, want %s",
			actualNextRunLocal.Format("2006-01-02T15:04"), localTimeStr)
		t.Logf("  NextRun stored as: %v", schedule.NextRun)
		t.Logf("  NextRun in Local:  %v", actualNextRunLocal)
	}

	// Verify times are stored as UTC in the database
	zone, _ := schedule.RunAt.Zone()
	if zone != "UTC" {
		t.Errorf("RunAt should be stored as UTC, got zone %s", zone)
	}
}

// TestWeeklySchedule_PreservesDayOfWeek verifies that when a weekly schedule fires,
// the next run is on the same day-of-week as the original RunAt, not drifted by
// execution timing. This is the bug reported: "Feb 21 11PM EST" weekly schedule
// should always land on Saturdays, not shift to a different day.
func TestWeeklySchedule_PreservesDayOfWeek(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("Failed to create project: %v", err)
	}

	task := &models.Task{
		Title:     "Weekly Saturday Task",
		ProjectID: project.ID,
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// User creates weekly schedule for "Feb 21, 11PM" (local time)
	// Feb 21, 2026 is a Saturday
	localTimeStr := "2026-02-21T23:00"
	form := url.Values{}
	form.Set("run_at", localTimeStr)
	form.Set("repeat_type", "weekly")
	form.Set("repeat_interval", "1")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/tasks/%s/schedules", task.ID), strings.NewReader(form.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.CreateSchedule(c); err != nil {
		t.Fatalf("CreateSchedule failed: %v", err)
	}

	// Fetch the schedule
	schedules, _ := h.scheduleRepo.ListByTask(ctx, task.ID)
	if len(schedules) != 1 {
		t.Fatalf("Expected 1 schedule, got %d", len(schedules))
	}
	schedule := schedules[0]

	// Verify the RunAt local time matches what the user set
	runAtLocal := schedule.RunAt.Local()
	if runAtLocal.Format("2006-01-02T15:04") != localTimeStr {
		t.Errorf("RunAt local time: got %s, want %s", runAtLocal.Format("2006-01-02T15:04"), localTimeStr)
	}

	// Verify Feb 21 is Saturday
	if runAtLocal.Weekday() != time.Saturday {
		t.Errorf("Feb 21 should be Saturday, got %s", runAtLocal.Weekday())
	}

	// Simulate the scheduler firing the task 2 days late (e.g., server was down)
	// The scheduler fires on Monday Feb 23 at some random time
	schedulerTime := time.Date(2026, 2, 23, 19, 45, 0, 0, time.UTC) // Monday 7:45 PM UTC

	// Compute next run as the scheduler would
	nextRun := schedule.ComputeNextRun(schedulerTime)
	if nextRun == nil {
		t.Fatal("Expected next run for weekly schedule, got nil")
	}

	// The next run should be on the NEXT Saturday (Feb 28), NOT on Monday (Mar 2)
	nextRunLocal := nextRun.Local()
	t.Logf("RunAt:    %v (local: %v, weekday: %s)", schedule.RunAt, runAtLocal, runAtLocal.Weekday())
	t.Logf("Now:      %v (weekday: %s)", schedulerTime, schedulerTime.Weekday())
	t.Logf("NextRun:  %v (local: %v, weekday: %s)", nextRun, nextRunLocal, nextRunLocal.Weekday())

	// Must be Saturday (same day-of-week as RunAt)
	if nextRunLocal.Weekday() != time.Saturday {
		t.Errorf("NextRun should be Saturday, got %s (next run: %v)",
			nextRunLocal.Weekday(), nextRunLocal)
	}

	// Must be at 11:00 PM local (same time-of-day as RunAt)
	if nextRunLocal.Hour() != 23 || nextRunLocal.Minute() != 0 {
		t.Errorf("NextRun should be at 11:00 PM local, got %02d:%02d",
			nextRunLocal.Hour(), nextRunLocal.Minute())
	}

	// Must be after the scheduler time (in the future)
	if !nextRun.After(schedulerTime) {
		t.Errorf("NextRun should be after scheduler time: %v is not after %v",
			nextRun, schedulerTime)
	}

	// Verify the date is Feb 28 (next Saturday after Feb 23)
	expectedDate := time.Date(2026, 2, 28, 23, 0, 0, 0, time.Local)
	if !nextRun.Equal(expectedDate) {
		t.Errorf("NextRun should be Feb 28 11PM, got %v (expected %v)", nextRunLocal, expectedDate)
	}
}

// TestUpdateSchedule_WeeklyTimezoneRoundTrip verifies that updating a weekly schedule
// that has already run doesn't corrupt the time due to timezone mismatches.
// This is the exact bug scenario: user sets "Feb 21 11PM EST", but after save
// sees "weekly Next: 2026-03-03 6:00 PM" because:
// 1. RunAt stored as local wall-clock time (23:00) instead of UTC (04:00)
// 2. NextRun computed from "now" + 7 days instead of advancing from RunAt
func TestUpdateSchedule_WeeklyTimezoneRoundTrip(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("Failed to create project: %v", err)
	}

	task := &models.Task{
		Title:     "Weekly Task",
		ProjectID: project.ID,
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// Create initial weekly schedule for Saturday 11PM local time
	runAtLocal := time.Date(2026, 2, 21, 23, 0, 0, 0, time.Local)
	schedule := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          runAtLocal.UTC(), // Store as UTC
		RepeatType:     models.RepeatWeekly,
		RepeatInterval: 1,
		Enabled:        true,
	}
	if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("Failed to create schedule: %v", err)
	}

	// Simulate the schedule having run once
	ranTime := time.Date(2026, 2, 22, 4, 5, 0, 0, time.UTC) // 5 seconds after due
	nextAfterRun := schedule.ComputeNextRun(ranTime)
	if err := h.scheduleRepo.MarkRan(ctx, schedule.ID, ranTime, nextAfterRun); err != nil {
		t.Fatalf("Failed to mark ran: %v", err)
	}

	// Now update the schedule via the handler (simulating user editing)
	e := echo.New()
	form := url.Values{}
	form.Set("run_at", "2026-02-21T23:00") // Same time - Feb 21 11PM local
	form.Set("repeat_type", "weekly")
	form.Set("repeat_interval", "1")

	req := httptest.NewRequest(http.MethodPut, "/schedules/"+schedule.ID, strings.NewReader(form.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(schedule.ID)

	if err := h.UpdateSchedule(c); err != nil {
		t.Fatalf("UpdateSchedule failed: %v", err)
	}

	// Reload the schedule from DB
	updated, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("Failed to reload schedule: %v", err)
	}

	// Verify the NextRun is correct
	if updated.NextRun == nil {
		t.Fatal("NextRun should not be nil")
	}

	nextRunLocal := updated.NextRun.Local()
	t.Logf("Updated RunAt:   %v (local: %v)", updated.RunAt, updated.RunAt.Local())
	t.Logf("Updated NextRun: %v (local: %v)", updated.NextRun, nextRunLocal)

	// NextRun should be on a Saturday
	if nextRunLocal.Weekday() != time.Saturday {
		t.Errorf("NextRun should be Saturday, got %s", nextRunLocal.Weekday())
	}

	// NextRun should be at 11PM local
	if nextRunLocal.Hour() != 23 {
		t.Errorf("NextRun should be at 11 PM local, got %d:00 (this is the timezone corruption bug!)",
			nextRunLocal.Hour())
	}

	// NextRun = RunAt (the scheduler handles advancing to the next future occurrence).
	// Verify it equals RunAt (Feb 21 23:00 local).
	runAtExpected := updated.RunAt.Local()
	if nextRunLocal.Format("2006-01-02T15:04") != runAtExpected.Format("2006-01-02T15:04") {
		t.Errorf("NextRun should equal RunAt. RunAt: %s, NextRun: %s",
			runAtExpected.Format("2006-01-02T15:04"), nextRunLocal.Format("2006-01-02T15:04"))
	}

	// Verify NextRun is NOT the buggy value (e.g., 6:00 PM local which would indicate
	// the 5-hour EST timezone offset corruption)
	if nextRunLocal.Hour() == 18 {
		t.Errorf("TIMEZONE BUG DETECTED: NextRun is 6:00 PM local instead of 11:00 PM. "+
			"This means the time was stored as local wall-clock and re-read as UTC, "+
			"shifting by the timezone offset. NextRun: %v", nextRunLocal)
	}
}

// TestCreateSchedule_PastDateRecurring verifies that creating a recurring schedule
// with a past RunAt immediately computes the next future occurrence, rather than
// setting NextRun to the past date and letting the scheduler race.
func TestCreateSchedule_PastDateRecurring(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("Failed to create project: %v", err)
	}

	task := &models.Task{
		Title:     "Weekly Past Task",
		ProjectID: project.ID,
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// Create a weekly schedule with RunAt 2 days in the past (local time)
	pastDate := time.Now().Add(-48 * time.Hour)
	localTimeStr := pastDate.Format("2006-01-02T15:04")

	form := url.Values{}
	form.Set("run_at", localTimeStr)
	form.Set("repeat_type", "weekly")
	form.Set("repeat_interval", "1")

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/tasks/%s/schedules", task.ID), strings.NewReader(form.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.CreateSchedule(c); err != nil {
		t.Fatalf("CreateSchedule failed: %v", err)
	}

	// Fetch the schedule
	schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("Failed to list schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("Expected 1 schedule, got %d", len(schedules))
	}

	schedule := schedules[0]

	// NextRun should equal RunAt. For past RunAt values, the scheduler picks
	// it up immediately on its next tick and advances NextRun to the future.
	if schedule.NextRun == nil {
		t.Fatal("NextRun should not be nil")
	}

	nextRunLocal := schedule.NextRun.Local()
	runAtLocal := schedule.RunAt.Local()

	// NextRun = RunAt (handler no longer pre-advances to the future)
	if nextRunLocal.Format("2006-01-02T15:04") != runAtLocal.Format("2006-01-02T15:04") {
		t.Errorf("NextRun should equal RunAt. RunAt: %s, NextRun: %s",
			runAtLocal.Format("2006-01-02T15:04"), nextRunLocal.Format("2006-01-02T15:04"))
	}

	t.Logf("RunAt:   %v (local: %v, %s)", schedule.RunAt, runAtLocal, runAtLocal.Weekday())
	t.Logf("NextRun: %v (local: %v, %s)", schedule.NextRun, nextRunLocal, nextRunLocal.Weekday())
}

// TestUpdateSchedule_PastDateRecurring verifies that updating a recurring schedule
// with a past RunAt computes the next future occurrence regardless of LastRun state.
func TestUpdateSchedule_PastDateRecurring(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("Failed to create project: %v", err)
	}

	task := &models.Task{
		Title:     "Weekly Update Task",
		ProjectID: project.ID,
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// Create initial schedule (in the past, never run)
	pastDate := time.Now().Add(-48 * time.Hour)
	schedule := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          pastDate.UTC(),
		RepeatType:     models.RepeatWeekly,
		RepeatInterval: 1,
		Enabled:        true,
	}
	// Manually set NextRun to a future date (to simulate repo.Create behavior)
	nextRun := schedule.ComputeNextRun(time.Now().UTC())
	schedule.NextRun = nextRun
	if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("Failed to create schedule: %v", err)
	}

	// Update the schedule with a different past date
	newPastDate := time.Now().Add(-24 * time.Hour)
	form := url.Values{}
	form.Set("run_at", newPastDate.Format("2006-01-02T15:04"))
	form.Set("repeat_type", "weekly")
	form.Set("repeat_interval", "1")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPut, "/schedules/"+schedule.ID, strings.NewReader(form.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(schedule.ID)

	if err := h.UpdateSchedule(c); err != nil {
		t.Fatalf("UpdateSchedule failed: %v", err)
	}

	// Reload
	updated, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("Failed to reload schedule: %v", err)
	}

	if updated.NextRun == nil {
		t.Fatal("NextRun should not be nil")
	}

	// NextRun = RunAt (handler sets NextRun to RunAt; scheduler handles advancing)
	nextRunLocal := updated.NextRun.Local()
	runAtLocal := updated.RunAt.Local()
	if nextRunLocal.Format("2006-01-02T15:04") != runAtLocal.Format("2006-01-02T15:04") {
		t.Errorf("NextRun should equal RunAt. RunAt: %s, NextRun: %s",
			runAtLocal.Format("2006-01-02T15:04"), nextRunLocal.Format("2006-01-02T15:04"))
	}

	t.Logf("RunAt:   %v (local: %v)", updated.RunAt, runAtLocal)
	t.Logf("NextRun: %v (local: %v)", updated.NextRun, nextRunLocal)
}

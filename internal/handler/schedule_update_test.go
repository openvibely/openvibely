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
	"github.com/openvibely/openvibely/internal/testutil"
)

// TestUpdateSchedule_RecurringAfterRun verifies that updating a recurring schedule
// that has already run doesn't reset NextRun to the past
func TestUpdateSchedule_RecurringAfterRun(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)

	// Create a task
	task := &models.Task{
		Title:      "Test Task",
		ProjectID:  "default",
		Category:   "scheduled",
		Status:     "pending",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Create a weekly schedule for "yesterday"
	yesterday := time.Now().AddDate(0, 0, -1)
	runAt := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 14, 0, 0, 0, time.UTC)

	schedule := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          runAt,
		RepeatType:     models.RepeatWeekly,
		RepeatInterval: 1,
		Enabled:        true,
	}

	if err := scheduleRepo.Create(context.Background(), schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	// Simulate the scheduler having run it
	now := time.Now()
	nextRun := schedule.ComputeNextRun(now)
	if err := scheduleRepo.MarkRan(context.Background(), schedule.ID, now, nextRun); err != nil {
		t.Fatalf("failed to mark as ran: %v", err)
	}

	// Reload to get the updated state
	schedule, err := scheduleRepo.GetByID(context.Background(), schedule.ID)
	if err != nil {
		t.Fatalf("failed to reload: %v", err)
	}

	originalNextRun := *schedule.NextRun

	// Now simulate user updating the schedule (changing interval to 2)
	// Set up the handler
	h := &Handler{
		scheduleRepo: scheduleRepo,
		taskSvc:      nil, // Not needed for this test
	}

	e := echo.New()
	formData := url.Values{}
	formData.Set("run_at", runAt.In(time.Local).Format("2006-01-02T15:04"))
	formData.Set("repeat_type", "weekly")
	formData.Set("repeat_interval", "2") // Change interval to 2 weeks

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

	// Reload the schedule
	updated, err := scheduleRepo.GetByID(context.Background(), schedule.ID)
	if err != nil {
		t.Fatalf("failed to reload after update: %v", err)
	}

	// NextRun should equal RunAt. The handler now always sets NextRun = RunAt,
	// and the scheduler handles picking up past-due schedules immediately.
	runAtLocal := updated.RunAt.Local()
	nextRunLocal := updated.NextRun.Local()
	if nextRunLocal.Format("2006-01-02T15:04") != runAtLocal.Format("2006-01-02T15:04") {
		t.Errorf("NextRun should equal RunAt. RunAt=%s, NextRun=%s",
			runAtLocal.Format("2006-01-02T15:04"), nextRunLocal.Format("2006-01-02T15:04"))
	}

	// Verify the interval was updated
	if updated.RepeatInterval != 2 {
		t.Errorf("RepeatInterval should be 2, got %d", updated.RepeatInterval)
	}

	t.Logf("Original NextRun: %s", originalNextRun.Format("2006-01-02 15:04:05"))
	t.Logf("Updated NextRun:  %s", updated.NextRun.Format("2006-01-02 15:04:05"))
	t.Logf("Updated interval: %d", updated.RepeatInterval)
}

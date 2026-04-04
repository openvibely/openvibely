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

	"github.com/openvibely/openvibely/internal/models"
)

func TestHandler_RescheduleTask_Simple1AMto2AM(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{
		Name:        "Test Project",
		Description: "For testing 1 AM to 2 AM",
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task scheduled for 1:30 AM on March 14, 2026
	task := &models.Task{
		Title:      "1 AM Task",
		Prompt:     "Test task at 1 AM",
		Category:   models.CategoryScheduled,
		Status:     models.StatusPending,
		ProjectID:  project.ID,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Create schedule for 1:30 AM
	scheduleTime := time.Date(2026, 3, 14, 1, 30, 0, 0, time.Local).UTC()
	schedule := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          scheduleTime,
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}
	schedule.NextRun = &scheduleTime
	if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	t.Logf("Original schedule time (UTC): %v", schedule.RunAt)
	t.Logf("Original schedule time (Local): %v", schedule.RunAt.Local())

	// Now drag it to 2 AM on the same day
	form := url.Values{}
	form.Set("new_date", "2026-03-14")
	form.Set("hour", "2")

	req := httptest.NewRequest(http.MethodPatch, "/schedules/"+schedule.ID+"/reschedule", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	c := e.NewContext(req, rec)
	c.SetParamNames("scheduleId")
	c.SetParamValues(schedule.ID)

	if err := h.RescheduleTask(c); err != nil {
		t.Fatalf("RescheduleTask failed: %v", err)
	}

	// Check the response
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}

	// Fetch the updated schedule
	updated, err := h.scheduleRepo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("failed to fetch updated schedule: %v", err)
	}

	t.Logf("Updated RunAt (UTC): %v", updated.RunAt)
	t.Logf("Updated RunAt (Local): %v", updated.RunAt.Local())
	t.Logf("Updated NextRun (UTC): %v", updated.NextRun)
	t.Logf("Updated NextRun (Local): %v", updated.NextRun.Local())

	// The task should now be scheduled for 2:30 AM (same minutes as original)
	actualLocal := updated.RunAt.Local()

	if actualLocal.Hour() != 2 {
		t.Errorf("expected hour 2, got %d", actualLocal.Hour())
	}
	if actualLocal.Minute() != 30 {
		t.Errorf("expected minute 30, got %d", actualLocal.Minute())
	}
	if actualLocal.Year() != 2026 || actualLocal.Month() != 3 || actualLocal.Day() != 14 {
		t.Errorf("expected date 2026-03-14, got %04d-%02d-%02d",
			actualLocal.Year(), actualLocal.Month(), actualLocal.Day())
	}

	fmt.Printf("✓ Successfully rescheduled from 1:30 AM to 2:30 AM\n")
}

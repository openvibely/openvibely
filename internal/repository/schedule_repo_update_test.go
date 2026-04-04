package repository

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

// TestUpdateSchedule_DoesNotAffectOtherSchedules verifies that updating one schedule
// does not modify other schedules (regression test for cross-schedule update bug)
func TestUpdateSchedule_DoesNotAffectOtherSchedules(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	broadcaster := events.NewBroadcaster()

	schedRepo := NewScheduleRepo(db)
	taskRepo := NewTaskRepo(db, broadcaster)

	// Create two tasks
	task1 := &models.Task{
		ProjectID:   "default",
		Title:       "Task 1",
		Category:    "scheduled",
		Status:      "pending",
	}
	task2 := &models.Task{
		ProjectID:   "default",
		Title:       "Task 2",
		Category:    "scheduled",
		Status:      "pending",
	}
	if err := taskRepo.Create(ctx, task1); err != nil {
		t.Fatalf("creating task1: %v", err)
	}
	if err := taskRepo.Create(ctx, task2); err != nil {
		t.Fatalf("creating task2: %v", err)
	}

	// Create two schedules for different tasks
	runAt1 := time.Date(2026, 2, 22, 10, 0, 0, 0, time.UTC)
	runAt2 := time.Date(2026, 2, 23, 11, 0, 0, 0, time.UTC)

	sched1 := &models.Schedule{
		TaskID:         task1.ID,
		RunAt:          runAt1,
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}
	sched2 := &models.Schedule{
		TaskID:         task2.ID,
		RunAt:          runAt2,
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}

	if err := schedRepo.Create(ctx, sched1); err != nil {
		t.Fatalf("creating schedule1: %v", err)
	}
	if err := schedRepo.Create(ctx, sched2); err != nil {
		t.Fatalf("creating schedule2: %v", err)
	}

	// Store original schedule2 values
	origSched2RunAt := sched2.RunAt
	origSched2RepeatType := sched2.RepeatType
	origSched2NextRun := *sched2.NextRun

	// Update schedule1
	newRunAt1 := time.Date(2026, 2, 22, 14, 30, 0, 0, time.UTC)
	sched1.RunAt = newRunAt1
	sched1.RepeatType = models.RepeatWeekly
	sched1.RepeatInterval = 2
	sched1.NextRun = &newRunAt1

	if err := schedRepo.Update(ctx, sched1); err != nil {
		t.Fatalf("updating schedule1: %v", err)
	}

	// Fetch schedule2 again and verify it wasn't modified
	fetchedSched2, err := schedRepo.GetByID(ctx, sched2.ID)
	if err != nil {
		t.Fatalf("fetching schedule2: %v", err)
	}
	if fetchedSched2 == nil {
		t.Fatal("schedule2 not found")
	}

	// Verify schedule2 values are unchanged
	if !fetchedSched2.RunAt.Equal(origSched2RunAt) {
		t.Errorf("schedule2.RunAt changed! expected %v, got %v", origSched2RunAt, fetchedSched2.RunAt)
	}
	if fetchedSched2.RepeatType != origSched2RepeatType {
		t.Errorf("schedule2.RepeatType changed! expected %v, got %v", origSched2RepeatType, fetchedSched2.RepeatType)
	}
	if fetchedSched2.NextRun == nil {
		t.Error("schedule2.NextRun became nil!")
	} else if !fetchedSched2.NextRun.Equal(origSched2NextRun) {
		t.Errorf("schedule2.NextRun changed! expected %v, got %v", origSched2NextRun, *fetchedSched2.NextRun)
	}
	if fetchedSched2.TaskID != task2.ID {
		t.Errorf("schedule2.TaskID changed! expected %s, got %s", task2.ID, fetchedSched2.TaskID)
	}

	// Verify schedule1 was actually updated
	fetchedSched1, err := schedRepo.GetByID(ctx, sched1.ID)
	if err != nil {
		t.Fatalf("fetching schedule1: %v", err)
	}
	if !fetchedSched1.RunAt.Equal(newRunAt1) {
		t.Errorf("schedule1.RunAt not updated! expected %v, got %v", newRunAt1, fetchedSched1.RunAt)
	}
	if fetchedSched1.RepeatType != models.RepeatWeekly {
		t.Errorf("schedule1.RepeatType not updated! expected %v, got %v", models.RepeatWeekly, fetchedSched1.RepeatType)
	}
}

// TestReschedulePreservesTimezone verifies that when rescheduling a task,
// the timezone is preserved from the original schedule.
func TestReschedulePreservesTimezone(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := NewScheduleRepo(db)
	taskRepo := NewTaskRepo(db, events.NewBroadcaster())
	ctx := context.Background()

	// Create a task first (required for foreign key constraint)
	task := &models.Task{
		ProjectID:   "default",
		Title:       "Test Task",
		//Description: "Test task for timezone test",
		Category:    "scheduled",
		Status:      "pending",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Create a schedule in PST timezone
	pst, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("failed to load PST location: %v", err)
	}

	// Original schedule: Feb 22, 2026 at 10:30 AM PST
	originalTime := time.Date(2026, 2, 22, 10, 30, 0, 0, pst)
	schedule := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          originalTime,
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}

	if err := scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	// Simulate what the RescheduleTask handler should do:
	// Move to Feb 23, 2026 at 14:00 (preserving PST timezone)
	// The minute and second should be preserved from original (30 minutes, 0 seconds)
	newTime := time.Date(2026, 2, 23, 14, 30, 0, 0, pst)
	schedule.RunAt = newTime
	schedule.NextRun = &newTime

	if err := scheduleRepo.Update(ctx, schedule); err != nil {
		t.Fatalf("failed to update schedule: %v", err)
	}

	// Retrieve and verify
	updated, err := scheduleRepo.GetByID(ctx, schedule.ID)
	if err != nil {
		t.Fatalf("failed to get updated schedule: %v", err)
	}

	// Verify the timezone is PST, not UTC
	expectedZone, expectedOffset := newTime.Zone()
	actualZone, actualOffset := updated.RunAt.Zone()

	if expectedZone != actualZone || expectedOffset != actualOffset {
		t.Errorf("timezone not preserved: expected %s (offset %d), got %s (offset %d)",
			expectedZone, expectedOffset, actualZone, actualOffset)
	}

	// Verify the time is correct in PST
	if !updated.RunAt.Equal(newTime) {
		t.Errorf("expected RunAt %v, got %v", newTime, updated.RunAt)
	}
}

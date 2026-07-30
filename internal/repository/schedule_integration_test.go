package repository

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

// TestScheduleLifecycle_WeeklyCreatedInPast tests the full lifecycle of a weekly
// schedule that is created with a time that has already passed today
func TestScheduleLifecycle_WeeklyCreatedInPast(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewScheduleRepo(db)
	taskRepo := NewTaskRepo(db, nil)

	// Create a task first (required by foreign key)
	task := &models.Task{
		Title:      "Test Task",
		ProjectID:  "default",
		Category:   "scheduled",
		Status:     "pending",
	}
	err := taskRepo.Create(context.Background(), task)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// User creates a weekly schedule for "today at 2:00 PM"
	// But it's currently 5:00 PM (schedule time has passed)
	runAt := time.Date(2026, 2, 22, 14, 0, 0, 0, time.UTC) // Saturday 2:00 PM

	schedule := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          runAt,
		RepeatType:     models.RepeatWeekly,
		RepeatInterval: 1,
		Enabled:        true,
	}

	// Create the schedule (like the UI would)
	err = repo.Create(context.Background(), schedule)
	if err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	// Check initial state
	if schedule.NextRun == nil {
		t.Fatal("NextRun should be set after creation")
	}

	t.Logf("After creation:")
	t.Logf("  RunAt:    %s", runAt.Format("2006-01-02 15:04:05"))
	t.Logf("  NextRun:  %s", schedule.NextRun.Format("2006-01-02 15:04:05"))

	// Initial NextRun should match RunAt
	if !schedule.NextRun.Equal(runAt) {
		t.Errorf("expected NextRun to equal RunAt initially, got NextRun=%v RunAt=%v", schedule.NextRun, runAt)
	}

	// Simulate scheduler processing it (it's 5:00 PM now, so it's "due")
	now := time.Date(2026, 2, 22, 17, 0, 0, 0, time.UTC) // Saturday 5:00 PM

	// Scheduler would call ComputeNextRun
	nextRun := schedule.ComputeNextRun(now)
	if nextRun == nil {
		t.Fatal("ComputeNextRun should return a value for weekly schedule")
	}

	t.Logf("After scheduler processes:")
	t.Logf("  Now:       %s", now.Format("2006-01-02 15:04:05"))
	t.Logf("  NextRun:   %s", nextRun.Format("2006-01-02 15:04:05"))

	// Expected: Next Sunday at 2:00 PM (7 days from today, preserving time-of-day)
	expectedNextRun := time.Date(2026, 3, 1, 14, 0, 0, 0, time.UTC) // Next Sunday 2:00 PM

	if !nextRun.Equal(expectedNextRun) {
		t.Errorf("expected NextRun=%v, got %v", expectedNextRun, *nextRun)
	}

	// Scheduler would mark it as ran
	err = repo.MarkRan(context.Background(), schedule.ID, now, nextRun)
	if err != nil {
		t.Fatalf("failed to mark as ran: %v", err)
	}

	// Reload from DB (like the UI would on refresh)
	reloaded, err := repo.GetByID(context.Background(), schedule.ID)
	if err != nil {
		t.Fatalf("failed to reload: %v", err)
	}

	t.Logf("After reload from DB:")
	t.Logf("  RunAt:    %s", reloaded.RunAt.Format("2006-01-02 15:04:05"))
	t.Logf("  NextRun:  %s", reloaded.NextRun.Format("2006-01-02 15:04:05"))
	t.Logf("  LastRun:  %s", reloaded.LastRun.Format("2006-01-02 15:04:05"))

	// UI should show Next Sunday (same day of week, 7 days later), NOT tomorrow
	if !reloaded.NextRun.Equal(expectedNextRun) {
		t.Errorf("after reload, expected NextRun=%v, got %v", expectedNextRun, *reloaded.NextRun)
	}

	// Check the day of week - should be same as runAt (Sunday)
	if reloaded.NextRun.Weekday() != runAt.Weekday() {
		t.Errorf("expected next run on %v (same as RunAt), got %v", runAt.Weekday(), reloaded.NextRun.Weekday())
	}

	// Check it's 7 days later
	daysDiff := reloaded.NextRun.Sub(runAt).Hours() / 24
	if daysDiff != 7 {
		t.Errorf("expected 7 days between RunAt and NextRun, got %.0f days", daysDiff)
	}
}

// TestScheduleLifecycle_MonthlyMonthEndSequence proves that a monthly schedule
// anchored to a month-end date (Jan 31) advances one calendar month at a time
// without skipping February, and that the persisted next_run follows the
// corrected clamp-and-recover sequence after each occurrence is claimed via
// MarkRan (the scheduler's advance-on-claim path).
func TestScheduleLifecycle_MonthlyMonthEndSequence(t *testing.T) {
	// ComputeNextRun preserves local wall-clock time across DST transitions;
	// pin the local zone to UTC so month-end assertions are deterministic.
	restore := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = restore })

	db := testutil.NewTestDB(t)
	repo := NewScheduleRepo(db)
	taskRepo := NewTaskRepo(db, nil)

	task := &models.Task{
		Title:     "Month-end billing",
		ProjectID: "default",
		Category:  "scheduled",
		Status:    "pending",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Anchor: January 31, 2026 at 10:00 (2026 is a non-leap year).
	runAt := time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC)
	schedule := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          runAt,
		RepeatType:     models.RepeatMonthly,
		RepeatInterval: 1,
		Enabled:        true,
	}
	if err := repo.Create(context.Background(), schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}
	if schedule.NextRun == nil || !schedule.NextRun.Equal(runAt) {
		t.Fatalf("expected initial NextRun=%v, got %v", runAt, schedule.NextRun)
	}

	// Expected persisted next_run after each claim, one per calendar month.
	expected := []time.Time{
		time.Date(2026, 2, 28, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 31, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC),
	}

	for i, want := range expected {
		reloaded, err := repo.GetByID(context.Background(), schedule.ID)
		if err != nil {
			t.Fatalf("step %d: failed to reload: %v", i, err)
		}
		due := *reloaded.NextRun

		// Scheduler fires when the occurrence is due and advances next_run.
		nextRun := reloaded.ComputeNextRun(due)
		if nextRun == nil {
			t.Fatalf("step %d: ComputeNextRun returned nil", i)
		}
		if err := repo.MarkRan(context.Background(), schedule.ID, due, nextRun); err != nil {
			t.Fatalf("step %d: MarkRan failed: %v", i, err)
		}

		persisted, err := repo.GetByID(context.Background(), schedule.ID)
		if err != nil {
			t.Fatalf("step %d: failed to reload after MarkRan: %v", i, err)
		}
		if persisted.NextRun == nil || !persisted.NextRun.Equal(want) {
			t.Fatalf("step %d: expected persisted next_run=%v, got %v", i, want, persisted.NextRun)
		}
		// February must not be skipped: consecutive occurrences are exactly one
		// calendar month apart.
		if persisted.NextRun.Month() != want.Month() {
			t.Fatalf("step %d: expected month %v, got %v", i, want.Month(), persisted.NextRun.Month())
		}
	}
}

// TestScheduleLifecycle_WeeklyCreatedInFuture tests a weekly schedule created
// for a future time today (hasn't run yet)
func TestScheduleLifecycle_WeeklyCreatedInFuture(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewScheduleRepo(db)
	taskRepo := NewTaskRepo(db, nil)

	// Create a task first (required by foreign key)
	task := &models.Task{
		Title:      "Test Task",
		ProjectID:  "default",
		Category:   "scheduled",
		Status:     "pending",
	}
	err := taskRepo.Create(context.Background(), task)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// User creates a weekly schedule for "today at 8:00 PM"
	// It's currently 5:00 PM (schedule time is in the future)
	runAt := time.Date(2026, 2, 22, 20, 0, 0, 0, time.UTC) // Saturday 8:00 PM

	schedule := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          runAt,
		RepeatType:     models.RepeatWeekly,
		RepeatInterval: 1,
		Enabled:        true,
	}

	err = repo.Create(context.Background(), schedule)
	if err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	t.Logf("After creation (time hasn't arrived yet):")
	t.Logf("  RunAt:    %s", runAt.Format("2006-01-02 15:04:05"))
	t.Logf("  NextRun:  %s", schedule.NextRun.Format("2006-01-02 15:04:05"))

	// NextRun should still be today at 8pm (hasn't run yet)
	if !schedule.NextRun.Equal(runAt) {
		t.Errorf("expected NextRun to equal RunAt, got NextRun=%v RunAt=%v", schedule.NextRun, runAt)
	}

	// UI should show "today at 8pm"
	now := time.Date(2026, 2, 22, 17, 0, 0, 0, time.UTC) // Saturday 5:00 PM
	if schedule.NextRun.Before(now) {
		t.Error("NextRun should not be in the past")
	}

	if schedule.NextRun.Day() != now.Day() {
		t.Errorf("NextRun should be today (day %d), got day %d", now.Day(), schedule.NextRun.Day())
	}
}

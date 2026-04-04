package models

import (
	"testing"
	"time"
)

// TestSchedule_ComputeNextRun_TimeDrift verifies that recurring schedules
// preserve the time-of-day from RunAt rather than drifting based on execution time
func TestSchedule_ComputeNextRun_TimeDrift(t *testing.T) {
	// Create a daily schedule for 3:00 PM
	runAt := time.Date(2026, 2, 20, 15, 0, 0, 0, time.UTC)
	s := &Schedule{
		RunAt:          runAt,
		RepeatType:     RepeatDaily,
		RepeatInterval: 1,
	}

	// Simulate scheduler executing task 5 seconds late
	executionTime := time.Date(2026, 2, 22, 15, 0, 5, 0, time.UTC)
	nextRun := s.ComputeNextRun(executionTime)

	// Expected: Next run should be 3:00 PM tomorrow, NOT 3:00:05 PM
	expected := time.Date(2026, 2, 23, 15, 0, 0, 0, time.UTC)

	if nextRun == nil {
		t.Fatal("expected next run, got nil")
	}

	if !nextRun.Equal(expected) {
		t.Errorf("time drift detected: got %v, want %v", nextRun, expected)
		t.Errorf("time-of-day shifted from %02d:%02d:%02d to %02d:%02d:%02d",
			runAt.Hour(), runAt.Minute(), runAt.Second(),
			nextRun.Hour(), nextRun.Minute(), nextRun.Second())
	}
}

// TestSchedule_ComputeNextRun_Reschedule verifies that when a recurring task
// is rescheduled, the new time is preserved via updating RunAt
func TestSchedule_ComputeNextRun_Reschedule(t *testing.T) {
	// Original schedule: daily at 3:00 PM
	s := &Schedule{
		RunAt:          time.Date(2026, 2, 20, 15, 0, 0, 0, time.UTC),
		RepeatType:     RepeatDaily,
		RepeatInterval: 1,
	}

	// User reschedules to 5:00 PM
	// BOTH RunAt and NextRun must be updated to preserve the new time
	newTime := time.Date(2026, 2, 22, 17, 0, 0, 0, time.UTC)
	s.RunAt = newTime
	s.NextRun = &newTime

	// Task executes at 5:00 PM
	nextRun := s.ComputeNextRun(newTime)

	// Expected: Next run should be 5:00 PM tomorrow
	expected := time.Date(2026, 2, 23, 17, 0, 0, 0, time.UTC)

	if nextRun == nil {
		t.Fatal("expected next run, got nil")
	}

	if !nextRun.Equal(expected) {
		t.Errorf("reschedule not preserved: got %v, want %v", nextRun, expected)
	}
}

// TestSchedule_ComputeNextRun_WeeklyPreservesDayOfWeek verifies that weekly
// schedules always land on the same day-of-week as RunAt, even when the
// scheduler fires late (e.g., server was down).
func TestSchedule_ComputeNextRun_WeeklyPreservesDayOfWeek(t *testing.T) {
	// Schedule for Saturday Feb 21 at 4:00 AM UTC (= Feb 20 11PM EST)
	runAt := time.Date(2026, 2, 21, 4, 0, 0, 0, time.UTC) // Saturday
	s := &Schedule{
		RunAt:          runAt,
		RepeatType:     RepeatWeekly,
		RepeatInterval: 1,
	}

	if runAt.Weekday() != time.Saturday {
		t.Fatalf("RunAt should be Saturday, got %s", runAt.Weekday())
	}

	tests := []struct {
		name          string
		from          time.Time
		expectedDate  time.Time
		expectedDay   time.Weekday
	}{
		{
			name:         "fires on time (Saturday)",
			from:         time.Date(2026, 2, 21, 4, 0, 0, 0, time.UTC),
			expectedDate: time.Date(2026, 2, 28, 4, 0, 0, 0, time.UTC),
			expectedDay:  time.Saturday,
		},
		{
			name:         "fires 1 day late (Sunday)",
			from:         time.Date(2026, 2, 22, 12, 0, 0, 0, time.UTC),
			expectedDate: time.Date(2026, 2, 28, 4, 0, 0, 0, time.UTC),
			expectedDay:  time.Saturday,
		},
		{
			name:         "fires 2 days late (Monday)",
			from:         time.Date(2026, 2, 23, 19, 45, 0, 0, time.UTC),
			expectedDate: time.Date(2026, 2, 28, 4, 0, 0, 0, time.UTC),
			expectedDay:  time.Saturday,
		},
		{
			name:         "fires 5 days late (Thursday)",
			from:         time.Date(2026, 2, 26, 10, 0, 0, 0, time.UTC),
			expectedDate: time.Date(2026, 2, 28, 4, 0, 0, 0, time.UTC),
			expectedDay:  time.Saturday,
		},
		{
			name:         "fires 6 days late (Friday, just before next occurrence)",
			from:         time.Date(2026, 2, 27, 23, 0, 0, 0, time.UTC),
			expectedDate: time.Date(2026, 2, 28, 4, 0, 0, 0, time.UTC),
			expectedDay:  time.Saturday,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextRun := s.ComputeNextRun(tt.from)
			if nextRun == nil {
				t.Fatal("expected next run, got nil")
			}

			if nextRun.Weekday() != tt.expectedDay {
				t.Errorf("expected %s, got %s (next run: %v)", tt.expectedDay, nextRun.Weekday(), nextRun)
			}

			if !nextRun.Equal(tt.expectedDate) {
				t.Errorf("expected %v, got %v", tt.expectedDate, nextRun)
			}

			if !nextRun.After(tt.from) {
				t.Errorf("next run should be after %v, got %v", tt.from, nextRun)
			}
		})
	}
}

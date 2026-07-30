package models

import (
	"testing"
	"time"
)

func TestSchedule_ComputeNextRun_Once(t *testing.T) {
	s := &Schedule{RepeatType: RepeatOnce, RepeatInterval: 1}
	now := time.Now()
	next := s.ComputeNextRun(now)
	if next != nil {
		t.Error("expected nil for one-time schedule")
	}
}

func TestSchedule_ComputeNextRun_Daily(t *testing.T) {
	s := &Schedule{
		RepeatType:     RepeatDaily,
		RepeatInterval: 1,
		RunAt:          time.Date(2026, 2, 21, 10, 0, 0, 0, time.UTC),
	}
	from := time.Date(2026, 2, 21, 10, 0, 0, 0, time.UTC)
	next := s.ComputeNextRun(from)
	if next == nil {
		t.Fatal("expected next run for daily schedule")
	}
	expected := from.Add(24 * time.Hour)
	if !next.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, *next)
	}
}

func TestSchedule_ComputeNextRun_DailyInterval(t *testing.T) {
	s := &Schedule{
		RepeatType:     RepeatDaily,
		RepeatInterval: 3,
		RunAt:          time.Date(2026, 2, 21, 10, 0, 0, 0, time.UTC),
	}
	from := time.Date(2026, 2, 21, 10, 0, 0, 0, time.UTC)
	next := s.ComputeNextRun(from)
	if next == nil {
		t.Fatal("expected next run")
	}
	expected := from.Add(3 * 24 * time.Hour)
	if !next.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, *next)
	}
}

func TestSchedule_ComputeNextRun_Weekly(t *testing.T) {
	s := &Schedule{
		RepeatType:     RepeatWeekly,
		RepeatInterval: 1,
		RunAt:          time.Date(2026, 2, 21, 10, 0, 0, 0, time.UTC),
	}
	from := time.Date(2026, 2, 21, 10, 0, 0, 0, time.UTC)
	next := s.ComputeNextRun(from)
	if next == nil {
		t.Fatal("expected next run for weekly schedule")
	}
	expected := from.Add(7 * 24 * time.Hour)
	if !next.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, *next)
	}
}

func TestSchedule_ComputeNextRun_Monthly(t *testing.T) {
	s := &Schedule{
		RepeatType:     RepeatMonthly,
		RepeatInterval: 1,
		RunAt:          time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
	}
	from := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	next := s.ComputeNextRun(from)
	if next == nil {
		t.Fatal("expected next run for monthly schedule")
	}
	expected := time.Date(2026, 2, 15, 10, 0, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, *next)
	}
}

// TestSchedule_ComputeNextRun_MonthlyMonthEnd verifies that monthly schedules
// anchored to a late-month date emit exactly one occurrence per interval without
// skipping short months, clamp to the last day of months that lack the anchor
// day, and recover the original anchor day in later months that contain it.
func TestSchedule_ComputeNextRun_MonthlyMonthEnd(t *testing.T) {
	// ComputeNextRun preserves local wall-clock time across DST transitions.
	// Pin the local zone to UTC so month-end assertions are deterministic
	// regardless of the host machine's timezone.
	restore := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = restore })

	tests := []struct {
		name     string
		runAt    time.Time
		interval int
		from     time.Time
		expected time.Time
	}{
		// January 31 across a non-leap February (2026) clamps to Feb 28.
		{
			name:     "jan31 to feb28 non-leap",
			runAt:    time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC),
			interval: 1,
			from:     time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC),
			expected: time.Date(2026, 2, 28, 10, 0, 0, 0, time.UTC),
		},
		// January 31 recovers to March 31 (anchor day exists in March).
		{
			name:     "jan31 recovers to mar31",
			runAt:    time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC),
			interval: 1,
			from:     time.Date(2026, 2, 28, 10, 0, 0, 0, time.UTC),
			expected: time.Date(2026, 3, 31, 10, 0, 0, 0, time.UTC),
		},
		// January 30 clamps to Feb 28 in a non-leap year.
		{
			name:     "jan30 to feb28 non-leap",
			runAt:    time.Date(2026, 1, 30, 9, 30, 0, 0, time.UTC),
			interval: 1,
			from:     time.Date(2026, 1, 30, 9, 30, 0, 0, time.UTC),
			expected: time.Date(2026, 2, 28, 9, 30, 0, 0, time.UTC),
		},
		// January 29 clamps to Feb 28 in a non-leap year.
		{
			name:     "jan29 to feb28 non-leap",
			runAt:    time.Date(2026, 1, 29, 8, 0, 0, 0, time.UTC),
			interval: 1,
			from:     time.Date(2026, 1, 29, 8, 0, 0, 0, time.UTC),
			expected: time.Date(2026, 2, 28, 8, 0, 0, 0, time.UTC),
		},
		// January 29 lands exactly on Feb 29 in a leap year (2028).
		{
			name:     "jan29 to feb29 leap",
			runAt:    time.Date(2028, 1, 29, 8, 0, 0, 0, time.UTC),
			interval: 1,
			from:     time.Date(2028, 1, 29, 8, 0, 0, 0, time.UTC),
			expected: time.Date(2028, 2, 29, 8, 0, 0, 0, time.UTC),
		},
		// January 30 clamps to Feb 29 in a leap year.
		{
			name:     "jan30 to feb29 leap",
			runAt:    time.Date(2028, 1, 30, 8, 0, 0, 0, time.UTC),
			interval: 1,
			from:     time.Date(2028, 1, 30, 8, 0, 0, 0, time.UTC),
			expected: time.Date(2028, 2, 29, 8, 0, 0, 0, time.UTC),
		},
		// January 31 clamps to Feb 29 in a leap year, then recovers in March.
		{
			name:     "jan31 to feb29 leap",
			runAt:    time.Date(2028, 1, 31, 8, 0, 0, 0, time.UTC),
			interval: 1,
			from:     time.Date(2028, 1, 31, 8, 0, 0, 0, time.UTC),
			expected: time.Date(2028, 2, 29, 8, 0, 0, 0, time.UTC),
		},
		{
			name:     "jan31 leap recovers to mar31",
			runAt:    time.Date(2028, 1, 31, 8, 0, 0, 0, time.UTC),
			interval: 1,
			from:     time.Date(2028, 2, 29, 8, 0, 0, 0, time.UTC),
			expected: time.Date(2028, 3, 31, 8, 0, 0, 0, time.UTC),
		},
		// Interval greater than one: Jan 31 every 2 months -> Mar 31 (skips
		// clamping since March contains the anchor day).
		{
			name:     "jan31 interval2 to mar31",
			runAt:    time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC),
			interval: 2,
			from:     time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC),
			expected: time.Date(2026, 3, 31, 10, 0, 0, 0, time.UTC),
		},
		// Interval greater than one that lands on a short month: Dec 31 every 2
		// months -> Feb 28 (non-leap 2027).
		{
			name:     "dec31 interval2 to feb28",
			runAt:    time.Date(2026, 12, 31, 10, 0, 0, 0, time.UTC),
			interval: 2,
			from:     time.Date(2026, 12, 31, 10, 0, 0, 0, time.UTC),
			expected: time.Date(2027, 2, 28, 10, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Schedule{
				RepeatType:     RepeatMonthly,
				RepeatInterval: tt.interval,
				RunAt:          tt.runAt,
			}
			next := s.ComputeNextRun(tt.from)
			if next == nil {
				t.Fatal("expected next run for monthly schedule")
			}
			if !next.Equal(tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, *next)
			}
			if !next.After(tt.from) {
				t.Errorf("next run %v should be after from %v", *next, tt.from)
			}
		})
	}
}

// TestSchedule_ComputeNextRun_MonthlyFullSequence walks the corrected monthly
// sequence for a Jan 31 anchor across a non-leap year, proving no target month
// is skipped and the anchor day is preserved whenever the month contains it.
func TestSchedule_ComputeNextRun_MonthlyFullSequence(t *testing.T) {
	restore := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = restore })

	s := &Schedule{
		RepeatType:     RepeatMonthly,
		RepeatInterval: 1,
		RunAt:          time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC),
	}

	// Expected occurrences, one per calendar month, from Jan 31 2026 onward.
	expected := []time.Time{
		time.Date(2026, 2, 28, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 31, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC),
	}

	from := s.RunAt
	for i, want := range expected {
		next := s.ComputeNextRun(from)
		if next == nil {
			t.Fatalf("step %d: expected next run, got nil", i)
		}
		if !next.Equal(want) {
			t.Fatalf("step %d: expected %v, got %v", i, want, *next)
		}
		from = *next
	}
}

func TestSchedule_ComputeNextRun_Seconds(t *testing.T) {
	s := &Schedule{
		RepeatType:     RepeatSeconds,
		RepeatInterval: 10,
		RunAt:          time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC),
	}
	from := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC)
	next := s.ComputeNextRun(from)
	if next == nil {
		t.Fatal("expected next run for seconds schedule")
	}
	expected := from.Add(10 * time.Second)
	if !next.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, *next)
	}
}

func TestSchedule_ComputeNextRun_Seconds_FarFuture(t *testing.T) {
	// Test that the fast-forward optimization works correctly for large gaps
	s := &Schedule{
		RepeatType:     RepeatSeconds,
		RepeatInterval: 5,
		RunAt:          time.Date(2026, 2, 20, 10, 0, 0, 0, time.UTC),
	}
	// 5 days later
	from := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC)
	next := s.ComputeNextRun(from)
	if next == nil {
		t.Fatal("expected next run")
	}
	// Next should be after 'from' and within 5 seconds of it
	if !next.After(from) {
		t.Errorf("expected next run after 'from', got %v", *next)
	}
	if next.Sub(from) > 5*time.Second {
		t.Errorf("expected next run within 5s of 'from', got %v (diff=%v)", *next, next.Sub(from))
	}
}

func TestSchedule_ComputeNextRun_Minutes(t *testing.T) {
	s := &Schedule{
		RepeatType:     RepeatMinutes,
		RepeatInterval: 5,
		RunAt:          time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC),
	}
	from := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC)
	next := s.ComputeNextRun(from)
	if next == nil {
		t.Fatal("expected next run for minutes schedule")
	}
	expected := from.Add(5 * time.Minute)
	if !next.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, *next)
	}
}

func TestSchedule_ComputeNextRun_Hours(t *testing.T) {
	s := &Schedule{
		RepeatType:     RepeatHours,
		RepeatInterval: 2,
		RunAt:          time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC),
	}
	from := time.Date(2026, 2, 25, 10, 0, 0, 0, time.UTC)
	next := s.ComputeNextRun(from)
	if next == nil {
		t.Fatal("expected next run for hours schedule")
	}
	expected := from.Add(2 * time.Hour)
	if !next.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, *next)
	}
}

func TestRepeatType_IsSubDaily(t *testing.T) {
	tests := []struct {
		rt       RepeatType
		expected bool
	}{
		{RepeatOnce, false},
		{RepeatSeconds, true},
		{RepeatMinutes, true},
		{RepeatHours, true},
		{RepeatDaily, false},
		{RepeatWeekly, false},
		{RepeatMonthly, false},
	}
	for _, tt := range tests {
		if got := tt.rt.IsSubDaily(); got != tt.expected {
			t.Errorf("RepeatType(%q).IsSubDaily() = %v, want %v", tt.rt, got, tt.expected)
		}
	}
}

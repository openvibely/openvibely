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

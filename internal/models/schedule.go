package models

import (
	"fmt"
	"time"
)

type RepeatType string

const (
	RepeatOnce    RepeatType = "once"
	RepeatSeconds RepeatType = "seconds"
	RepeatMinutes RepeatType = "minutes"
	RepeatHours   RepeatType = "hours"
	RepeatDaily   RepeatType = "daily"
	RepeatWeekly  RepeatType = "weekly"
	RepeatMonthly RepeatType = "monthly"

	MaxScheduleRepeatInterval = 365
)

func ValidateScheduleRepeatInterval(interval int) error {
	if interval < 1 || interval > MaxScheduleRepeatInterval {
		return fmt.Errorf("repeat interval must be between 1 and %d", MaxScheduleRepeatInterval)
	}
	return nil
}

// IsSubDaily returns true if the repeat type runs more frequently than once per day.
func (rt RepeatType) IsSubDaily() bool {
	return rt == RepeatSeconds || rt == RepeatMinutes || rt == RepeatHours
}

type Schedule struct {
	ID                  string     `json:"id"`
	TaskID              string     `json:"task_id"`
	RunAt               time.Time  `json:"run_at"`
	RepeatType          RepeatType `json:"repeat_type"`
	RepeatInterval      int        `json:"repeat_interval"`
	Enabled             bool       `json:"enabled"`
	ClearContextOnStart bool       `json:"clear_context_on_start"`
	NextRun             *time.Time `json:"next_run"`
	LastRun             *time.Time `json:"last_run"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// ComputeNextRun calculates the next run time based on repeat settings.
// It advances from RunAt in fixed intervals until it finds a time after 'from'.
// This preserves exact day-of-week (weekly), day-of-month (monthly), and time-of-day,
// regardless of when the scheduler actually processes the schedule.
func (s *Schedule) ComputeNextRun(from time.Time) *time.Time {
	if ValidateScheduleRepeatInterval(s.RepeatInterval) != nil {
		return nil
	}

	switch s.RepeatType {
	case RepeatOnce:
		return nil // One-time schedule has no next run

	case RepeatSeconds:
		interval := time.Duration(s.RepeatInterval) * time.Second
		next := s.RunAt
		// For very short intervals, jump close to 'from' first to avoid slow loop
		if elapsed := from.Sub(next); elapsed > 0 {
			steps := int(elapsed / interval)
			next = next.Add(time.Duration(steps) * interval)
		}
		for !next.After(from) {
			next = next.Add(interval)
		}
		return &next

	case RepeatMinutes:
		interval := time.Duration(s.RepeatInterval) * time.Minute
		next := s.RunAt
		if elapsed := from.Sub(next); elapsed > 0 {
			steps := int(elapsed / interval)
			next = next.Add(time.Duration(steps) * interval)
		}
		for !next.After(from) {
			next = next.Add(interval)
		}
		return &next

	case RepeatHours:
		interval := time.Duration(s.RepeatInterval) * time.Hour
		next := s.RunAt
		if elapsed := from.Sub(next); elapsed > 0 {
			steps := int(elapsed / interval)
			next = next.Add(time.Duration(steps) * interval)
		}
		for !next.After(from) {
			next = next.Add(interval)
		}
		return &next

	case RepeatDaily:
		// Convert to local time to preserve time-of-day across DST transitions
		next := s.RunAt.Local()
		fromLocal := from.Local()
		for !next.After(fromLocal) {
			next = next.AddDate(0, 0, s.RepeatInterval)
		}
		nextUTC := next.UTC()
		return &nextUTC

	case RepeatWeekly:
		// Convert to local time to preserve time-of-day across DST transitions
		next := s.RunAt.Local()
		fromLocal := from.Local()
		for !next.After(fromLocal) {
			next = next.AddDate(0, 0, 7*s.RepeatInterval)
		}
		nextUTC := next.UTC()
		return &nextUTC

	case RepeatMonthly:
		// Convert to local time to preserve time-of-day across DST transitions.
		// Advance one interval at a time from the original anchor day, clamping to
		// the last day of any target month that does not contain the anchor day.
		// This emits exactly one occurrence per interval without skipping short
		// months (e.g. Jan 31 -> Feb 28 -> Mar 31) and recovers the anchor day in
		// months that contain it.
		anchor := s.RunAt.Local()
		fromLocal := from.Local()
		months := 0
		next := anchor
		for !next.After(fromLocal) {
			months += s.RepeatInterval
			next = AddMonthsClamped(anchor, months)
		}
		nextUTC := next.UTC()
		return &nextUTC

	default:
		return nil
	}
}

// AddMonthsClamped adds months to anchor while preserving the anchor's
// day-of-month when the target month contains it, and clamping to the target
// month's final day when it does not. It preserves the anchor's time-of-day.
// Unlike time.AddDate, this never rolls a month-end date into the following
// month (e.g. Jan 31 + 1 month yields Feb 28/29, not Mar 3).
func AddMonthsClamped(anchor time.Time, months int) time.Time {
	year := anchor.Year()
	// Zero-based month index makes arithmetic across year boundaries simple.
	monthIdx := int(anchor.Month()) - 1 + months
	year += monthIdx / 12
	monthIdx %= 12
	if monthIdx < 0 {
		monthIdx += 12
		year--
	}
	targetMonth := time.Month(monthIdx + 1)

	day := anchor.Day()
	if last := lastDayOfMonth(year, targetMonth, anchor.Location()); day > last {
		day = last
	}
	return time.Date(year, targetMonth, day,
		anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(),
		anchor.Location())
}

// lastDayOfMonth returns the final calendar day of the given month, accounting
// for leap years in February.
func lastDayOfMonth(year int, month time.Month, loc *time.Location) int {
	// Day 0 of the next month normalizes to the last day of the target month.
	return time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()
}

package models

import "time"

type RepeatType string

const (
	RepeatOnce    RepeatType = "once"
	RepeatSeconds RepeatType = "seconds"
	RepeatMinutes RepeatType = "minutes"
	RepeatHours   RepeatType = "hours"
	RepeatDaily   RepeatType = "daily"
	RepeatWeekly  RepeatType = "weekly"
	RepeatMonthly RepeatType = "monthly"
)

// IsSubDaily returns true if the repeat type runs more frequently than once per day.
func (rt RepeatType) IsSubDaily() bool {
	return rt == RepeatSeconds || rt == RepeatMinutes || rt == RepeatHours
}

type Schedule struct {
	ID             string     `json:"id"`
	TaskID         string     `json:"task_id"`
	RunAt          time.Time  `json:"run_at"`
	RepeatType     RepeatType `json:"repeat_type"`
	RepeatInterval int        `json:"repeat_interval"`
	Enabled        bool       `json:"enabled"`
	NextRun        *time.Time `json:"next_run"`
	LastRun        *time.Time `json:"last_run"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// ComputeNextRun calculates the next run time based on repeat settings.
// It advances from RunAt in fixed intervals until it finds a time after 'from'.
// This preserves exact day-of-week (weekly), day-of-month (monthly), and time-of-day,
// regardless of when the scheduler actually processes the schedule.
func (s *Schedule) ComputeNextRun(from time.Time) *time.Time {
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
		// Convert to local time to preserve time-of-day across DST transitions
		next := s.RunAt.Local()
		fromLocal := from.Local()
		for !next.After(fromLocal) {
			next = next.AddDate(0, s.RepeatInterval, 0)
		}
		nextUTC := next.UTC()
		return &nextUTC

	default:
		return nil
	}
}

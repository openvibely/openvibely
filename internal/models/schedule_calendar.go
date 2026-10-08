package models

// ScheduleSkip excludes a half-open interval of Unix seconds. An empty ScheduleID
// applies to every schedule in the project, including schedules created later.
type ScheduleSkip struct {
	ScheduleID string `json:"schedule_id"`
	StartAt    int64  `json:"start_at"`
	EndAt      int64  `json:"end_at"`
	Restored   bool   `json:"restored,omitempty"`
}

type ScheduleSkipChange struct {
	Skip    ScheduleSkip `json:"skip"`
	Present bool         `json:"present"`
}

type ScheduleCalendarState struct {
	Paused bool           `json:"paused"`
	Skips  []ScheduleSkip `json:"skips"`
}

type ScheduleCalendarAction struct {
	Action      string               `json:"action"`
	ScheduleIDs []string             `json:"schedule_ids,omitempty"`
	Skips       []ScheduleSkip       `json:"skips,omitempty"`
	Changes     []ScheduleSkipChange `json:"changes,omitempty"`
}

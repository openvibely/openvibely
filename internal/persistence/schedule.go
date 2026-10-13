package persistence

import (
	"github.com/openvibely/openvibely/internal/models"
)

// ScheduleDiscoveryFilter bounds a read-only, current-project schedule discovery query.
type ScheduleDiscoveryFilter struct {
	// TaskID optionally restricts results to schedules bound to a single task.
	TaskID string
	// Title is an optional partial (case-insensitive substring) task title match.
	Title string
	// Enabled optionally restricts results to enabled (true) or disabled (false)
	// schedules. Nil returns both.
	Enabled *bool
	// Limit caps the number of returned rows. Callers should clamp before use.
	Limit int
	// Offset skips the first N rows for pagination.
	Offset int
}

// ScheduleDiscoveryRow is a schedule paired with its bound task's title for
// read-only discovery projections.
type ScheduleDiscoveryRow struct {
	Schedule  models.Schedule
	TaskTitle string
}

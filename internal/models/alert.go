package models

import "time"

type AlertType string

const (
	AlertTaskFailed        AlertType = "task_failed"
	AlertTaskNeedsFollowup AlertType = "task_needs_followup"
	AlertCustom            AlertType = "custom"
)

type AlertSeverity string

const (
	SeverityInfo    AlertSeverity = "info"
	SeverityWarning AlertSeverity = "warning"
	SeverityError   AlertSeverity = "error"
)

type Alert struct {
	ID          string        `json:"id"`
	ProjectID   string        `json:"project_id"`
	TaskID      *string       `json:"task_id,omitempty"`
	ExecutionID *string       `json:"execution_id,omitempty"`
	Type        AlertType     `json:"type"`
	Severity    AlertSeverity `json:"severity"`
	Title       string        `json:"title"`
	Message     string        `json:"message"`
	IsRead      bool          `json:"is_read"`
	CreatedAt   time.Time     `json:"created_at"`
}

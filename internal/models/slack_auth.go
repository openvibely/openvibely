package models

import "time"

// SlackAuthorizedUser represents a Slack user authorized to interact with a project's Slack integration.
type SlackAuthorizedUser struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	SlackUserID string    `json:"slack_user_id"`
	DisplayName string    `json:"display_name"`
	AddedAt     time.Time `json:"added_at"`
	AddedBy     string    `json:"added_by"`
}

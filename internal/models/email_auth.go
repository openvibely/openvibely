package models

import "time"

// EmailAuthorizedSender represents an email address authorized to use a project's email channel.
type EmailAuthorizedSender struct {
	ChannelSenderMapping
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	EmailAddress string    `json:"email_address"`
	DisplayName  string    `json:"display_name"`
	AddedAt      time.Time `json:"added_at"`
	AddedBy      string    `json:"added_by"`
}

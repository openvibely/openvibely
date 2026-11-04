package models

import "time"

// DiscordAuthorizedUser represents a Discord user authorized to interact with a project's Discord integration.
type DiscordAuthorizedUser struct {
	ApplicationUserID string    `json:"application_user_id,omitempty"`
	ID                string    `json:"id"`
	ProjectID         string    `json:"project_id"`
	DiscordUserID     string    `json:"discord_user_id"`
	DisplayName       string    `json:"display_name"`
	AddedAt           time.Time `json:"added_at"`
	AddedBy           string    `json:"added_by"`
}

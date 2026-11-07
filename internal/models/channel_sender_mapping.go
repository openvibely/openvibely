package models

// ChannelSenderMapping is independent of project membership. Empty mappings
// cannot authenticate a sender when login is enabled.
type ChannelSenderMapping struct {
	ApplicationUserID string `json:"application_user_id"`
	MappingReview     bool   `json:"mapping_review"`
	TeamID            string `json:"team_id,omitempty"`
}

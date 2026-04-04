package models

import "time"

// SlackTaskContext stores Slack reply context for tasks created from Slack so
// completion notifications can be posted back to the originating thread.
type SlackTaskContext struct {
	TaskID         string    `json:"task_id"`
	SlackTeamID    string    `json:"slack_team_id"`
	SlackChannelID string    `json:"slack_channel_id"`
	SlackThreadTS  string    `json:"slack_thread_ts"`
	SlackUserID    string    `json:"slack_user_id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

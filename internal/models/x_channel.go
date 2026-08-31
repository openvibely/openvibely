package models

import "time"

// XAuthorizedUser grants one X identity inbound access to one project.
type XAuthorizedUser struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	XUserID   string    `json:"x_user_id"`
	Username  string    `json:"username,omitempty"`
	AddedAt   time.Time `json:"added_at"`
}

// XTaskContext preserves the tweet reply destination for channel-origin work.
type XTaskContext struct {
	TaskID         string    `json:"task_id"`
	ProjectID      string    `json:"project_id"`
	AccountID      string    `json:"account_id"`
	ConversationID string    `json:"conversation_id"`
	ReplyToTweetID string    `json:"reply_to_tweet_id"`
	XUserID        string    `json:"x_user_id"`
	Username       string    `json:"username,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

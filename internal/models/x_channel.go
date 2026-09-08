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

const (
	XReplyDeliveryPending = "pending"
	XReplyDeliveryPosting = "posting"
	XReplyDeliverySent    = "sent"
)

// XReplyDelivery durably tracks one task-completion reply independently of task execution.
type XReplyDelivery struct {
	ID             string    `json:"id"`
	TaskID         string    `json:"task_id"`
	ProjectID      string    `json:"project_id"`
	AccountID      string    `json:"account_id"`
	ReplyToTweetID string    `json:"reply_to_tweet_id"`
	ResponseText   string    `json:"response_text"`
	Status         string    `json:"status"`
	ProviderPostID string    `json:"provider_post_id,omitempty"`
	Attempts       int       `json:"attempts"`
	LastError      string    `json:"last_error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
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

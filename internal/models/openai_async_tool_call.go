package models

import "time"

const (
	OpenAIAsyncToolCallPending          = "pending"
	OpenAIAsyncToolCallRunning          = "running"
	OpenAIAsyncToolCallCompleted        = "completed"
	OpenAIAsyncToolCallDelivered        = "delivered"
	OpenAIAsyncToolCallFailed           = "failed"
	OpenAIAsyncToolCallCancelled        = "cancelled"
	OpenAIAsyncToolCallExpired          = "expired"
	OpenAIAsyncToolCallProviderRejected = "provider_rejected"
	OpenAIAsyncToolCallStale            = "stale"
)

type OpenAIAsyncToolCall struct {
	ID              string
	ProjectID       string
	TaskID          string
	ExecutionID     string
	ResponseID      string
	CallID          string
	ToolName        string
	ArgumentsJSON   string
	Status          string
	Result          string
	IsError         bool
	ErrorMessage    string
	DeliverAttempts int
	DeadlineAt      time.Time
	DeliveredAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

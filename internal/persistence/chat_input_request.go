package persistence

import (
	"time"
)

type ChatInputRequestStatus string

const (
	ChatInputRequestPending  ChatInputRequestStatus = "pending"
	ChatInputRequestResolved ChatInputRequestStatus = "resolved"
)

type ChatInputRequestRecord struct {
	ID            string
	ProjectID     string
	ExecutionID   string
	QuestionsJSON string
	AnswersJSON   string
	Status        ChatInputRequestStatus
	CreatedAt     time.Time
	ExpiresAt     time.Time
	ResolvedAt    *time.Time
}

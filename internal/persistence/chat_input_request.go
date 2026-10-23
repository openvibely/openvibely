package persistence

import (
	"encoding/json"
	"fmt"
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

// Installation metadata is written only by the app's dependency loader. Ordinary
// request_user_input calls store the existing JSON array of questions instead.
type ChatInstallationRequest struct {
	ToolCallID              string          `json:"tool_call_id,omitempty"`
	ToolArguments           json.RawMessage `json:"tool_arguments,omitempty"`
	Manifest                json.RawMessage `json:"manifest"`
	AgentID                 string          `json:"agent_id,omitempty"`
	ChatMode                string          `json:"chat_mode,omitempty"`
	Result                  string          `json:"result,omitempty"`
	Handled                 bool            `json:"handled,omitempty"`
	ContinuationExecutionID string          `json:"continuation_execution_id,omitempty"`
}

type ChatInputRequestPayload struct {
	Kind         string                   `json:"kind"`
	Questions    json.RawMessage          `json:"questions"`
	Installation *ChatInstallationRequest `json:"installation"`
}

func (r ChatInputRequestRecord) DecodeQuestions() (json.RawMessage, *ChatInstallationRequest, error) {
	var questions []json.RawMessage
	if err := json.Unmarshal([]byte(r.QuestionsJSON), &questions); err == nil {
		return json.RawMessage(r.QuestionsJSON), nil, nil
	}
	var payload ChatInputRequestPayload
	if err := json.Unmarshal([]byte(r.QuestionsJSON), &payload); err != nil {
		return nil, nil, err
	}
	if payload.Kind != "skill_installation" || payload.Installation == nil || !json.Valid(payload.Questions) {
		return nil, nil, fmt.Errorf("invalid input request payload")
	}
	return payload.Questions, payload.Installation, nil
}

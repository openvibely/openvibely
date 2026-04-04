package service

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestBuildAnthropicClientHistory(t *testing.T) {
	history := []models.Execution{
		{PromptSent: "hello", Output: "hi", Status: models.ExecCompleted},
		{PromptSent: "", Output: "ignored", Status: models.ExecRunning},
		{PromptSent: "bye", Output: "goodbye", Status: models.ExecFailed},
	}
	messages := buildAnthropicClientHistory(history)

	if len(messages) != 4 {
		t.Errorf("expected 4 messages, got %d", len(messages))
	}
}

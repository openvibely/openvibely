package service

import (
	"context"
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

func TestLLMServiceSmallHelpers(t *testing.T) {
	if got := statusOrNil(nil); got != "<nil>" {
		t.Fatalf("statusOrNil(nil) = %q", got)
	}
	task := &models.Task{Status: models.StatusRunning}
	if got := statusOrNil(task); got != string(models.StatusRunning) {
		t.Fatalf("statusOrNil(task) = %q", got)
	}

	ctx := WithDirectUsageProject(context.Background(), "project-1")
	if got := directUsageProjectFromContext(ctx); got != "project-1" {
		t.Fatalf("directUsageProjectFromContext = %q", got)
	}
}

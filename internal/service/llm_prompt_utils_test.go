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
	for _, value := range []string{"low", " MEDIUM ", "High", "max"} {
		if got := claudeEffort(value); got == "" {
			t.Fatalf("claudeEffort(%q) returned empty", value)
		}
	}
	if got := claudeEffort("extreme"); got != "" {
		t.Fatalf("claudeEffort invalid = %q, want empty", got)
	}

	prefixOnly := prependDirectNoToolsInstruction("")
	if prefixOnly == "" || prefixOnly[len(prefixOnly)-1:] == "\n" {
		t.Fatalf("unexpected empty prompt instruction: %q", prefixOnly)
	}
	withPrompt := prependDirectNoToolsInstruction("  hello  ")
	if withPrompt == prefixOnly || withPrompt[len(withPrompt)-5:] != "hello" {
		t.Fatalf("expected trimmed prompt after no-tools instruction, got %q", withPrompt)
	}

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

package service

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
)

func TestBuildAnthropicClientHistory_AlternatingRoles(t *testing.T) {
	tests := []struct {
		name     string
		history  []models.Execution
		expected []anthropicclient.Message
	}{
		{
			name:     "empty history",
			history:  nil,
			expected: nil,
		},
		{
			name: "normal alternating history",
			history: []models.Execution{
				{PromptSent: "hello", Output: "hi there", Status: models.ExecCompleted},
				{PromptSent: "how are you", Output: "I'm good", Status: models.ExecCompleted},
			},
			expected: []anthropicclient.Message{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi there"},
				{Role: "user", Content: "how are you"},
				{Role: "assistant", Content: "I'm good"},
			},
		},
		{
			name: "consecutive user messages from missing outputs are merged",
			history: []models.Execution{
				{PromptSent: "first", Output: "", Status: models.ExecFailed},
				{PromptSent: "second", Output: "", Status: models.ExecFailed},
				{PromptSent: "third", Output: "response", Status: models.ExecCompleted},
			},
			expected: []anthropicclient.Message{
				{Role: "user", Content: "first\n\nsecond\n\nthird"},
				{Role: "assistant", Content: "response"},
			},
		},
		{
			name: "trailing user message is dropped",
			history: []models.Execution{
				{PromptSent: "hello", Output: "hi", Status: models.ExecCompleted},
				{PromptSent: "another", Output: "", Status: models.ExecRunning},
			},
			expected: []anthropicclient.Message{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi"},
			},
		},
		{
			name: "only user messages with no outputs produces nil",
			history: []models.Execution{
				{PromptSent: "a", Output: "", Status: models.ExecFailed},
				{PromptSent: "b", Output: "", Status: models.ExecFailed},
			},
			expected: nil,
		},
		{
			name: "running status output is skipped",
			history: []models.Execution{
				{PromptSent: "q1", Output: "a1", Status: models.ExecCompleted},
				{PromptSent: "q2", Output: "partial", Status: models.ExecRunning},
			},
			expected: []anthropicclient.Message{
				{Role: "user", Content: "q1"},
				{Role: "assistant", Content: "a1"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildAnthropicClientHistory(tt.history)
			if len(got) != len(tt.expected) {
				t.Fatalf("got %d messages, want %d\ngot: %+v", len(got), len(tt.expected), got)
			}
			for i := range got {
				if got[i].Role != tt.expected[i].Role {
					t.Errorf("message[%d].Role = %q, want %q", i, got[i].Role, tt.expected[i].Role)
				}
				if got[i].Content != tt.expected[i].Content {
					t.Errorf("message[%d].Content = %q, want %q", i, got[i].Content, tt.expected[i].Content)
				}
			}
			// Verify no consecutive same-role messages
			for i := 1; i < len(got); i++ {
				if got[i].Role == got[i-1].Role {
					t.Errorf("consecutive same role at index %d-%d: both %q", i-1, i, got[i].Role)
				}
			}
		})
	}
}

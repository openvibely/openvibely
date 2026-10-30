package anthropic

import (
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	"testing"
)

func TestBuildClientHistoryLegacyTextCases(t *testing.T) {
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
			name: "failed executions without output preserve failure context",
			history: []models.Execution{
				{PromptSent: "first", Output: "", ErrorMessage: "provider failed", Status: models.ExecFailed},
				{PromptSent: "second", Output: "", Status: models.ExecFailed},
				{PromptSent: "third", Output: "response", Status: models.ExecCompleted},
			},
			expected: []anthropicclient.Message{
				{Role: "user", Content: "first"},
				{Role: "assistant", Content: "Previous execution failed before producing output: provider failed"},
				{Role: "user", Content: "second"},
				{Role: "assistant", Content: "Previous execution failed before producing output."},
				{Role: "user", Content: "third"},
				{Role: "assistant", Content: "response"},
			},
		},
		{
			name: "trailing prior user message is preserved",
			history: []models.Execution{
				{PromptSent: "hello", Output: "hi", Status: models.ExecCompleted},
				{PromptSent: "another", Output: "", Status: models.ExecRunning},
			},
			expected: []anthropicclient.Message{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi"},
				{Role: "user", Content: "another"},
			},
		},
		{
			name: "only failed messages with no outputs still produce history",
			history: []models.Execution{
				{PromptSent: "a", Output: "", Status: models.ExecFailed},
				{PromptSent: "b", Output: "", Status: models.ExecFailed},
			},
			expected: []anthropicclient.Message{
				{Role: "user", Content: "a"},
				{Role: "assistant", Content: "Previous execution failed before producing output."},
				{Role: "user", Content: "b"},
				{Role: "assistant", Content: "Previous execution failed before producing output."},
			},
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
				{Role: "user", Content: "q2"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildClientHistory(tt.history)
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

func TestBuildClientHistoryFailedOutput(t *testing.T) {
	history := []models.Execution{
		{PromptSent: "hello", Output: "hi", Status: models.ExecCompleted},
		{PromptSent: "", Output: "ignored", Status: models.ExecRunning},
		{PromptSent: "bye", Output: "goodbye", Status: models.ExecFailed},
	}
	messages := buildClientHistory(history)

	if len(messages) != 4 {
		t.Errorf("expected 4 messages, got %d", len(messages))
	}
}

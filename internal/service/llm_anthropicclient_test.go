package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestAnthropicClientToolSecondaryInfoAndAttachmentConversion(t *testing.T) {
	if got := toolSecondaryInfo("read_file", json.RawMessage(`{"file_path":"/tmp/project/README.md"}`)); got != "README.md" {
		t.Fatalf("read_file secondary = %q", got)
	}
	longCommand := strings.Repeat("x", 80)
	if got := toolSecondaryInfo("Bash", json.RawMessage(`{"command":"`+longCommand+`"}`)); !strings.HasPrefix(got, "$ "+strings.Repeat("x", 60)) || !strings.HasSuffix(got, "…") {
		t.Fatalf("bash secondary not truncated as expected: %q", got)
	}
	longPattern := strings.Repeat("p", 50)
	if got := toolSecondaryInfo("Grep", json.RawMessage(`{"pattern":"`+longPattern+`"}`)); !strings.HasPrefix(got, strings.Repeat("p", 40)) || !strings.HasSuffix(got, "…") {
		t.Fatalf("grep secondary not truncated as expected: %q", got)
	}
	if got := toolSecondaryInfo("Glob", json.RawMessage(`{"pattern":"**/*.go"}`)); got != "**/*.go" {
		t.Fatalf("glob secondary = %q", got)
	}
	if got := toolSecondaryInfo("unknown", json.RawMessage(`{"command":"ignored"}`)); got != "" {
		t.Fatalf("unknown secondary = %q", got)
	}
	if got := toolSecondaryInfo("read_file", json.RawMessage(`{`)); got != "" {
		t.Fatalf("invalid json secondary = %q", got)
	}
	parts := splitPath("/tmp/project/file.txt")
	if len(parts) == 0 || parts[len(parts)-1] != "file.txt" {
		t.Fatalf("splitPath parts = %#v", parts)
	}

	root := t.TempDir()
	textPath := filepath.Join(root, "note.txt")
	if err := os.WriteFile(textPath, []byte("hello attachment"), 0o644); err != nil {
		t.Fatal(err)
	}
	converted, err := convertAttachments([]models.Attachment{{FileName: "note.txt", FilePath: textPath, MediaType: "text/plain"}})
	if err != nil {
		t.Fatalf("convertAttachments: %v", err)
	}
	if len(converted) != 1 || converted[0] == nil {
		t.Fatalf("unexpected converted attachments: %#v", converted)
	}
	if empty, err := convertAttachments(nil); err != nil || empty != nil {
		t.Fatalf("empty convertAttachments = %#v, %v", empty, err)
	}
	if _, err := convertAttachments([]models.Attachment{{FileName: "missing.txt", FilePath: filepath.Join(root, "missing.txt")}}); err == nil || !strings.Contains(err.Error(), "load attachment missing.txt") {
		t.Fatalf("expected missing attachment error, got %v", err)
	}
}

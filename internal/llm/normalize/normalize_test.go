package normalize

import (
	"context"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

func TestNormalizeRequest_TrimsAndLimits(t *testing.T) {
	history := make([]models.Execution, 25)
	for i := range history {
		history[i] = models.Execution{PromptSent: "p", Output: "o", Status: models.ExecCompleted}
	}

	in := llmcontracts.AgentRequest{
		Ctx:       context.Background(),
		Operation: llmcontracts.OperationStreaming,
		Message:   "  hello  ",
		Attachments: []models.Attachment{
			{FilePath: "./go.mod", FileName: " go.mod ", MediaType: " text/plain "},
		},
		ChatHistory: history,
	}

	out, err := NormalizeRequest(in)
	if err != nil {
		t.Fatalf("NormalizeRequest error: %v", err)
	}
	if out.Message != "hello" {
		t.Fatalf("expected trimmed message, got %q", out.Message)
	}
	if len(out.ChatHistory) != 20 {
		t.Fatalf("expected limited history=20, got %d", len(out.ChatHistory))
	}
	if out.Attachments[0].FileName != "go.mod" {
		t.Fatalf("expected trimmed filename, got %q", out.Attachments[0].FileName)
	}
	if out.Attachments[0].MediaType != "text/plain" {
		t.Fatalf("expected trimmed media type, got %q", out.Attachments[0].MediaType)
	}
}

func TestNormalizeRequest_WorkDirAbsolutePreservedWithoutRepoPrefixing(t *testing.T) {
	const selectedPath = "/Users/dubee/go/src/github.com/claude-code"

	out, err := NormalizeRequest(llmcontracts.AgentRequest{
		Ctx:       context.Background(),
		Operation: llmcontracts.OperationDirect,
		Message:   "test",
		WorkDir:   selectedPath,
	})
	if err != nil {
		t.Fatalf("NormalizeRequest error: %v", err)
	}
	if out.WorkDir != selectedPath {
		t.Fatalf("expected WorkDir=%q, got %q", selectedPath, out.WorkDir)
	}
	if strings.Contains(out.WorkDir, "/openvibely/openvibely/") {
		t.Fatalf("work dir should not be prefixed by app repository root: %q", out.WorkDir)
	}
}

func TestNormalizeRequest_WorkDirRelativeUsesCurrentWorkingDirectory(t *testing.T) {
	const relative = "claude-code"

	out, err := NormalizeRequest(llmcontracts.AgentRequest{
		Ctx:       context.Background(),
		Operation: llmcontracts.OperationDirect,
		Message:   "test",
		WorkDir:   relative,
	})
	if err != nil {
		t.Fatalf("NormalizeRequest error: %v", err)
	}
	if out.WorkDir == relative {
		t.Fatalf("expected absolute normalized WorkDir for relative input, got %q", out.WorkDir)
	}
	if !strings.HasSuffix(out.WorkDir, "/claude-code") {
		t.Fatalf("expected normalized WorkDir to end with %q, got %q", relative, out.WorkDir)
	}
}

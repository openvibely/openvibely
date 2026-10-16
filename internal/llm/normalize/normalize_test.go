package normalize

import (
	"context"
	llmattachment "github.com/openvibely/openvibely/internal/llm/attachment"
	"os"
	"path/filepath"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

func TestNormalizeRequest_TrimsAndPreservesFullHistory(t *testing.T) {
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
	if len(out.ChatHistory) != len(history) {
		t.Fatalf("expected full history to be preserved for provider-aware compaction, got %d", len(out.ChatHistory))
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

// Exercise the send path through normalization and provider input preparation.
func TestNormalizeRequest_LargeImagesOnlyLimitDirectInputs(t *testing.T) {
	for _, tc := range []struct {
		name, media string
		direct      bool
	}{
		{"photo.heic", "image/heic", false}, {"photo.heif", "image/heif", false}, {"photo.bmp", "image/bmp", false},
		{"photo.png", "image/png", true}, {"photo.jpg", "image/jpeg", true}, {"photo.jpeg", "image/jpeg", true}, {"photo.gif", "image/gif", true}, {"photo.webp", "image/webp", true},
		{"photo.PNG", "", true}, {"photo", "image/png", true},
	} {
		t.Run(tc.name+tc.media, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name)
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			err = f.Truncate(15 << 20)
			closeErr := f.Close()
			if err != nil {
				t.Fatal(err)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			req, err := NormalizeRequest(llmcontracts.AgentRequest{Attachments: []models.Attachment{{FileName: tc.name, FilePath: path, MediaType: tc.media}}})
			if tc.direct {
				if err == nil || !strings.Contains(err.Error(), "image attachment too large") {
					t.Fatalf("expected direct-image size rejection, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			inputs, err := llmattachment.ModelInputs(context.Background(), req.Attachments)
			if err != nil {
				t.Fatal(err)
			}
			if len(inputs) != 1 || inputs[0].MediaType != "text/plain" || string(inputs[0].Data) != llmattachment.Reference(req.Attachments[0]) {
				t.Fatalf("expected only a file reference, got %#v", inputs)
			}
		})
	}
}

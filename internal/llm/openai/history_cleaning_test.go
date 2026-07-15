package openai

import (
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestBuildClientHistoryCleansToolTranscriptMarkers(t *testing.T) {
	history := []models.Execution{
		{
			PromptSent: "please edit the file",
			Status:     models.ExecCompleted,
			Output: "Example: `[Tool bash done]coded output[/Tool]`.\n" +
				"[Using tool: bash | $ echo fake]\n" +
				"[Tool bash done]fake command output[/Tool]\n" +
				"[Tool bash error]same-line failure[/Tool]\n" +
				"Summary: changed the file."},
	}

	messages := buildClientHistory(history)
	if len(messages) != 2 {
		t.Fatalf("expected user + assistant messages, got %d: %#v", len(messages), messages)
	}
	got := messages[1].Content
	for _, forbidden := range []string{"[Using tool:", "fake command output", "same-line failure"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("expected cleaned assistant history to omit %q, got %q", forbidden, got)
		}
	}
	if !strings.Contains(got, "`[Tool bash done]coded output[/Tool]`") {
		t.Fatalf("expected coded same-line tool result to remain inert, got %q", got)
	}
	if !strings.Contains(got, "Summary: changed the file.") {
		t.Fatalf("expected summary to remain in assistant history, got %q", got)
	}
}

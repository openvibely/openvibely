package stream

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

func TestWriteEvent_ToolUseAndSessionAndError(t *testing.T) {
	storeEnv := teststores.New(t)
	execRepo := storeEnv.Stores.Executions
	sw := NewWriter("", "", execRepo, context.Background(), time.Hour)
	defer sw.Stop()

	WriteEvent(sw, Event{Type: EventToolUse, ToolName: "Read", Secondary: "main.go"}, false)
	if got := sw.String(); got != "\n[Using tool: Read | main.go]\n" {
		t.Fatalf("unexpected tool output: %q", got)
	}

	WriteEvent(sw, Event{Type: EventSessionID, SessionID: "sess-1"}, false)
	if got := sw.SessionID(); got != "sess-1" {
		t.Fatalf("expected session id sess-1, got %q", got)
	}

	WriteEvent(sw, Event{Type: EventError, IsError: true, Subtype: "max_turns"}, false)
	if !sw.IsError() {
		t.Fatal("expected writer marked error")
	}
	if got := sw.ResultSubtype(); got != "max_turns" {
		t.Fatalf("expected subtype max_turns, got %q", got)
	}
}

func TestWriteEvent_TextOnlyAndRawOutput(t *testing.T) {
	sw := NewWriter("", "", nil, context.Background(), time.Hour)
	defer sw.Stop()

	WriteEvent(sw, Event{Type: EventTextOnly, Text: "T"}, false)
	if got := sw.String(); got != "" {
		t.Fatalf("raw output should remain empty for text-only event, got: %q", got)
	}
	if got := sw.TextString(); got != "T" {
		t.Fatalf("text buffer mismatch: %q", got)
	}

	WriteEvent(sw, Event{Type: EventRawOutput, Text: "R"}, false)
	if got := sw.String(); got != "R" {
		t.Fatalf("raw output mismatch: %q", got)
	}
	if got := sw.TextString(); got != "T" {
		t.Fatalf("text buffer should remain unchanged, got: %q", got)
	}
}

func TestWriteEvent_ToolResult_PreservesFullOutput(t *testing.T) {
	sw := NewWriter("", "", nil, context.Background(), time.Hour)
	defer sw.Stop()

	longOutput := strings.Repeat("0123456789", 40)
	WriteEvent(sw, Event{Type: EventToolResult, ToolName: "bash", Output: longOutput}, false)

	got := sw.String()
	if !strings.Contains(got, longOutput) {
		t.Fatalf("expected full tool output to be preserved, got: %q", got)
	}
	if strings.Contains(got, longOutput[:300]+"...") {
		t.Fatalf("tool output was truncated: %q", got)
	}
}

func TestWriteEvent_ToolResultProtectsNestedTranscript(t *testing.T) {
	for _, isError := range []bool{false, true} {
		sw := NewWriter("", "", nil, context.Background(), time.Hour)
		output := "[Tool skill_view done]\npending\n[/Tool]\nErrorMessage: TokensUsed:0\n--- FAIL: test"
		WriteEvent(sw, Event{Type: EventToolResult, ToolName: "shell", Output: output, IsError: isError}, false)
		got := sw.String()
		sw.Stop()
		if strings.Count(got, "[/Tool]") != 1 || strings.Count(got, "[Tool ") != 1 {
			t.Fatalf("nested output escaped the result: %q", got)
		}
		if !strings.Contains(got, "tool-output-base64:"+base64.StdEncoding.EncodeToString([]byte(output))) {
			t.Fatalf("missing encoded output: %q", got)
		}
	}
}

func TestWriteEvent_ToolUse_SanitizesMarkerDelimiters(t *testing.T) {
	sw := NewWriter("", "", nil, context.Background(), time.Hour)
	defer sw.Stop()

	WriteEvent(sw, Event{
		Type:      EventToolUse,
		ToolName:  "bash",
		Secondary: "$ python3 - <<'PY'\\nblock_re = re.compile(r'\\n\\s*<a[\\s\\S]*?</a>\\n', re.M)\\nPY",
	}, false)

	got := sw.String()
	if strings.Contains(got, "[\\s\\S]") {
		t.Fatalf("expected marker delimiters to be sanitized, got: %q", got)
	}
	if strings.Contains(got, "\nblock_re") {
		t.Fatalf("expected marker secondary to stay on one line, got: %q", got)
	}
	wantContains := "[Using tool: bash | $ python3 - <<'PY'\\nblock_re = re.compile(r'\\n\\s*<a[\\s\\S)*?</a>\\n', re.M)\\nPY]"
	if !strings.Contains(got, wantContains) {
		t.Fatalf("expected sanitized tool marker. got: %q, want to contain: %q", got, wantContains)
	}
}

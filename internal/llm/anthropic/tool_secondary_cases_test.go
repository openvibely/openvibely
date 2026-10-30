package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolSecondaryInfoLegacyCases(t *testing.T) {
	if got := toolSecondaryInfo("read_file", json.RawMessage(`{"file_path":"/tmp/project/README.md"}`)); got != "README.md" {
		t.Fatalf("read_file secondary = %q", got)
	}
	longCommand := strings.Repeat("x", 400)
	if got := toolSecondaryInfo("Bash", json.RawMessage(`{"command":"`+longCommand+`"}`)); !strings.HasPrefix(got, "$ "+strings.Repeat("x", 320)) || !strings.HasSuffix(got, "…") {
		t.Fatalf("bash secondary not truncated as expected: %q", got)
	}
	longPattern := strings.Repeat("p", 200)
	if got := toolSecondaryInfo("Grep", json.RawMessage(`{"pattern":"`+longPattern+`"}`)); !strings.HasPrefix(got, strings.Repeat("p", 140)) || !strings.HasSuffix(got, "…") {
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

}

package openaiclient

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/pkg/executionenv"
)

func TestEnvironmentOnResponsesAndCompletionsWire(t *testing.T) {
	dir := t.TempDir()
	items := ensureExecutionEnvironment([]any{map[string]any{"type": "message", "role": "user", "content": "run tests"}}, dir)
	items = ensureExecutionEnvironment(items, dir)
	if len(items) != 2 || items[1].(map[string]any)["content"] != "run tests" {
		t.Fatal(items)
	}
	wire, _ := json.Marshal(map[string]any{"input": items})
	if !strings.Contains(string(wire), "Shell executable:") || !strings.Contains(string(wire), dir) {
		t.Fatal(string(wire))
	}
	compacted := ensureExecutionEnvironment([]any{map[string]any{"type": "compaction", "encrypted_content": "opaque"}}, dir)
	if len(compacted) != 2 || !strings.HasPrefix(compacted[1].(map[string]any)["content"].(string), executionenv.Marker) {
		t.Fatal(compacted)
	}
	messages := ensureCompletionsEnvironment([]completionsMessage{{Role: "system", Content: "instructions"}, {Role: "user", Content: "run tests"}}, dir)
	messages = ensureCompletionsEnvironment(messages, dir)
	if len(messages) != 3 || messages[0].Role != "system" || messages[1].Role != "user" || messages[2].Content != "run tests" {
		t.Fatal(messages)
	}
}

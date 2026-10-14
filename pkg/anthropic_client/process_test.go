package anthropicclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/pkg/executionenv"
)

func TestProcessSessionProviderLoop(t *testing.T) {
	selected, err := executionenv.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	command := `read -r line; printf 'received:%s\n' "$line"`
	if selected.Name == "powershell" {
		command = `$line = [Console]::ReadLine(); Write-Output "received:$line"`
	}
	if selected.Name == "cmd" {
		command = `set /p line= & call echo received:%%line%%`
	}
	var mu sync.Mutex
	var sessionID, last, combined string
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		defs, _ := json.Marshal(body["tools"])
		if !strings.Contains(string(defs), "process") {
			t.Error("missing process tool")
		}
		emit := func(v any) { b, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", b) }
		w.Header().Set("Content-Type", "text/event-stream")
		emit(map[string]any{"type": "message_start", "message": map[string]any{"id": fmt.Sprint("msg_", calls), "model": "claude-sonnet-4-20250514", "usage": map[string]int{"input_tokens": 10}}})
		finished := strings.Contains(last, "status: completed") || calls > 6
		reason := "end_turn"
		if !finished {
			reason = "tool_use"
			name := "shell"
			args := map[string]any{"command": command, "yield_time_ms": 250, "timeout": 10}
			if calls > 1 {
				name = "process"
				args = map[string]any{"session_id": sessionID, "yield_time_ms": 1000}
				if calls == 2 {
					args["input"] = "hello\n"
				}
			}
			raw, _ := json.Marshal(args)
			emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprint("tool_", calls), "name": name}})
			emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(raw)}})
		} else {
			emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "done"}})
		}
		emit(map[string]any{"type": "content_block_stop", "index": 0})
		emit(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason}, "usage": map[string]int{"output_tokens": 10}})
		emit(map[string]any{"type": "message_stop"})
	}))
	defer server.Close()
	old := AnthropicAPIHost
	AnthropicAPIHost = server.URL
	defer func() { AnthropicAPIHost = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = NewWithAPIKey("test").SendAgentic(ctx, "run test", &AgenticOptions{Model: "claude-sonnet-4-20250514", WorkDir: t.TempDir(), MaxTurns: 8, OnToolResult: func(_ string, output string, isError bool) {
		mu.Lock()
		defer mu.Unlock()
		if isError {
			t.Errorf("tool failed: %s", output)
		}
		last = output
		combined += output
		if match := regexp.MustCompile(`session_id: (sh_[a-f0-9]+)`).FindStringSubmatch(output); len(match) > 1 {
			sessionID = match[1]
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if sessionID == "" || !strings.Contains(combined, "received:hello") || !strings.Contains(last, "exit_code: 0") {
		t.Fatalf("session=%s output=%s", sessionID, combined)
	}
}

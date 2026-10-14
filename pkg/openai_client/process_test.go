package openaiclient

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

func TestProcessSessionProviderLoops(t *testing.T) {
	for _, completions := range []bool{false, true} {
		t.Run(fmt.Sprintf("completions=%v", completions), func(t *testing.T) {
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
			capture := func(_ string, output string, isError bool) {
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
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				definitions, _ := json.Marshal(body["tools"])
				if !strings.Contains(string(definitions), "interact_process") {
					t.Error("missing shared process tool")
				}
				emit := func(v any) { b, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", b) }
				w.Header().Set("Content-Type", "text/event-stream")
				name := "shell"
				args := map[string]any{"command": command, "yield_time_ms": 250, "timeout": 10}
				finished := strings.Contains(last, "status: completed") || calls > 6
				if calls > 1 {
					name = "interact_process"
					args = map[string]any{"session_id": sessionID, "yield_time_ms": 1000}
					if calls == 2 {
						args["input"] = "hello\n"
					}
				}
				raw, _ := json.Marshal(args)
				if completions {
					delta := map[string]any{"content": "done"}
					reason := "stop"
					if !finished {
						delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprint("call_", calls), "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}}}
						reason = "tool_calls"
					}
					emit(map[string]any{"choices": []any{map[string]any{"delta": delta}}})
					emit(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": reason}}})
					fmt.Fprint(w, "data: [DONE]\n\n")
				} else {
					if !finished {
						emit(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "function_call", "call_id": fmt.Sprint("call_", calls), "name": name, "arguments": string(raw)}})
					} else {
						emit(map[string]any{"type": "response.output_text.delta", "delta": "done"})
					}
					emit(map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprint("resp_", calls), "status": "completed", "model": "gpt-5.3-codex"}})
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if completions {
				client := NewWithCompatibleAPIKey("test", server.URL+"/v1", "", "")
				_, err = client.SendCompletions(ctx, "run test", &CompletionsOptions{Model: "test", WorkDir: dir, OnToolResult: capture, MaxTurns: 8})
			} else {
				old := OpenAIAPIBaseURL
				OpenAIAPIBaseURL = server.URL + "/"
				defer func() { OpenAIAPIBaseURL = old }()
				client := newHTTPTestAPIKeyClient("test")
				_, err = client.SendAgentic(ctx, "run test", &AgenticOptions{Model: "gpt-5.3-codex", WorkDir: dir, OnToolResult: capture, MaxTurns: 8})
			}
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if sessionID == "" || !strings.Contains(combined, "received:hello") || !strings.Contains(last, "exit_code: 0") {
				t.Fatalf("session=%s output=%s", sessionID, combined)
			}
		})
	}
}

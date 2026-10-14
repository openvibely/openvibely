package openaiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	if !strings.Contains(string(wire), "Shell:") || strings.Contains(string(wire), "Shell executable:") || !strings.Contains(string(wire), dir) {
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

func TestEnvironmentPersistsAcrossToolDisabledTurns(t *testing.T) {
	for _, completions := range []bool{false, true} {
		t.Run(fmt.Sprint("completions=", completions), func(t *testing.T) {
			requests := make(chan []map[string]any, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				key := "input"
				if completions {
					key = "messages"
				}
				var messages []map[string]any
				if err := json.Unmarshal(body[key], &messages); err != nil {
					t.Error(err)
					return
				}
				requests <- messages
				w.Header().Set("Content-Type", "text/event-stream")
				if completions {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				} else {
					fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\"}}\n\n")
				}
			}))
			defer server.Close()
			oldURL := OpenAIAPIBaseURL
			OpenAIAPIBaseURL = server.URL + "/"
			defer func() { OpenAIAPIBaseURL = oldURL }()
			client := NewWithCompatibleAPIKey("test", server.URL, "Authorization", "Bearer ")
			dir := t.TempDir()
			var previous []map[string]any
			for turn := 0; turn < 3; turn++ {
				if turn == 2 {
					dir = t.TempDir()
				}
				var err error
				if completions {
					_, err = client.SendCompletions(context.Background(), "question", &CompletionsOptions{Model: "test", WorkDir: dir, DisableTools: true, MaxTurns: 1})
				} else {
					_, err = client.SendAgentic(context.Background(), "question", &AgenticOptions{Model: "test", WorkDir: dir, DisableTools: true, MaxTurns: 1})
				}
				if err != nil {
					t.Fatal(err)
				}
				messages := <-requests
				if turn > 0 && !reflect.DeepEqual(messages[:len(previous)], previous) {
					t.Fatalf("history prefix changed: %#v -> %#v", previous, messages)
				}
				count := 0
				for _, m := range messages {
					if text, ok := m["content"].(string); ok && strings.HasPrefix(text, executionenv.Marker) {
						count++
					}
				}
				want := 1
				if turn == 2 {
					want = 2
				}
				if count != want {
					t.Fatalf("environment messages=%d, want %d: %#v", count, want, messages)
				}
				if messages[len(messages)-1]["content"] != "question" {
					t.Fatal("user request must stay last")
				}
				previous = messages
			}
			client = NewWithCompatibleAPIKey("test", server.URL, "Authorization", "Bearer ")
			if completions {
				_, err := client.SendCompletions(context.Background(), "raw", &CompletionsOptions{Model: "test", DisableTools: true, OmitExecutionEnvironment: true, MaxTurns: 1})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				_, err := client.SendAgentic(context.Background(), "raw", &AgenticOptions{Model: "test", DisableTools: true, OmitExecutionEnvironment: true, MaxTurns: 1})
				if err != nil {
					t.Fatal(err)
				}
			}
			if messages := <-requests; len(messages) != 1 {
				t.Fatalf("raw request contains extra context: %#v", messages)
			}
		})
	}
}

func TestEnvironmentRestoredAfterCompactionWithRetainedUserHistory(t *testing.T) {
	dir := t.TempDir()
	items := []any{
		map[string]any{"role": "user", "content": executionenv.Context(dir)},
		map[string]any{"role": "user", "content": "old question"},
		map[string]any{"type": "compaction", "encrypted_content": "summary"},
		map[string]any{"type": "configuration_update", "reasoning": map[string]any{"effort": "high"}},
	}
	restored := ensureExecutionEnvironment(items, dir)
	if len(restored) != 5 || restored[2].(map[string]any)["type"] != "compaction" || restored[3].(map[string]any)["content"] != executionenv.Context(dir) || restored[4].(map[string]any)["type"] != "configuration_update" {
		t.Fatalf("wrong restoration order: %#v", restored)
	}
	if got := ensureExecutionEnvironment(restored, dir); len(got) != 5 {
		t.Fatal("environment duplicated after compaction")
	}
}

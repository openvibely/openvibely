package anthropicclient

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

func TestEnvironmentPersistsAcrossToolDisabledTurns(t *testing.T) {
	requests := make(chan []map[string]any, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests <- body.Messages
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"message_start","message":{"id":"test","model":"test","usage":{"input_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer server.Close()
	old := AnthropicAPIHost
	AnthropicAPIHost = server.URL
	defer func() { AnthropicAPIHost = old }()
	client := NewWithAPIKey("test")
	dir := t.TempDir()
	var previous []map[string]any
	for turn := 0; turn < 3; turn++ {
		if turn == 2 {
			dir = t.TempDir()
		}
		if _, err := client.SendAgentic(context.Background(), "question", &AgenticOptions{Model: "test", WorkDir: dir, DisableTools: true, MaxTurns: 1}); err != nil {
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
			t.Fatalf("environment count=%d, want %d", count, want)
		}
		if messages[len(messages)-1]["content"] != "question" {
			t.Fatal("user request must stay last")
		}
		previous = messages
	}
	client = NewWithAPIKey("test")
	if _, err := client.SendAgentic(context.Background(), "raw", &AgenticOptions{Model: "test", DisableTools: true, OmitExecutionEnvironment: true, MaxTurns: 1}); err != nil {
		t.Fatal(err)
	}
	if messages := <-requests; len(messages) != 1 {
		t.Fatalf("raw request has extra context: %#v", messages)
	}
}

func TestCheckpointEnvironmentDoesNotRepeatDuringHistoryReplay(t *testing.T) {
	first := executionenv.Context(t.TempDir())
	second := executionenv.Context(t.TempDir())
	checkpoint := []agenticMessage{{Role: "assistant", Content: "checkpoint"}, {Role: "user", Content: first}}
	replayed := appendHistoryMessage(checkpoint, Message{Role: "user", Content: first})
	if len(replayed) != 2 {
		t.Fatal("duplicated checkpoint environment")
	}
	replayed = appendHistoryMessage(replayed, Message{Role: "user", Content: second})
	if len(replayed) != 3 || replayed[2].Content != second {
		t.Fatal("lost changed environment")
	}
}

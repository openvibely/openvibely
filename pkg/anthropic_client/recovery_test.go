package anthropicclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAnthropicFailedContinuationPreservesClaimedSteerAndToolResult(t *testing.T) {
	client := NewWithAPIKey("test")
	calls, tools, claims := 0, 0, 0
	client.httpClient = &http.Client{Transport: anthropicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls > 1 {
			return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"type":"request_too_large","message":"too many tokens"}}`))}, nil
		}
		events := []string{
			`{"type":"message_start","message":{"id":"first","model":"claude-opus-4-6","usage":{"input_tokens":20}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"write1","name":"write_file","input":{}}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":8}}`,
			`{"type":"message_stop"}`,
		}
		body := "data: " + strings.Join(events, "\n\ndata: ") + "\n\n"
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	_, err := client.SendAgentic(context.Background(), "original task", &AgenticOptions{Model: "claude-opus-4-6",
		ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) {
			tools++
			return "file already written", false, nil
		},
		OnToolBoundarySteering: func(context.Context) (string, error) { claims++; return "actually only review", nil },
	})
	var failure *ConversationError
	if !errors.As(err, &failure) {
		t.Fatalf("missing conversation: %v", err)
	}
	for _, want := range []string{"original task", "write1", "file already written", "actually only review"} {
		if !strings.Contains(failure.MessagesJSON, want) {
			t.Fatalf("snapshot lost %q: %s", want, failure.MessagesJSON)
		}
	}
	if calls != 2 || tools != 1 || claims != 1 {
		t.Fatalf("calls=%d tools=%d claims=%d", calls, tools, claims)
	}
}

func TestAnthropicRecoveryUsesReplacementHistoryAndReturnsFailedRequest(t *testing.T) {
	client := NewWithAPIKey("test")
	client.History = []Message{{Role: "user", Content: "stale history"}}
	client.httpClient = &http.Client{Transport: anthropicRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Messages []Message `json:"messages"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 2 || body.Messages[0].Content != "retained steer" || body.Messages[1].Content != "file already written" {
			t.Fatalf("wrong recovery history: %s", raw)
		}
		return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"type":"request_too_large","message":"too many tokens"}}`))}, nil
	})}
	_, err := client.SendAgentic(context.Background(), "do not repeat original prompt", &AgenticOptions{Model: "claude-opus-4-6", DisableTools: true,
		RecoveryMessages: []Message{{Role: "user", Content: "retained steer"}, {Role: "assistant", Content: "file already written"}}})
	var failure *ConversationError
	if !errors.As(err, &failure) || !strings.Contains(failure.MessagesJSON, "retained steer") || strings.Contains(failure.MessagesJSON, "stale history") {
		t.Fatalf("failed request not preserved: %v", err)
	}
}

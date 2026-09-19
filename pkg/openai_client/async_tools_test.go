package openaiclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestSendAgentic_AsyncToolPersistsExecutesAndDeliversWithOriginalCallID(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		mu.Lock()
		requests = append(requests, req)
		requestIndex := len(requests)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		switch requestIndex {
		case 1:
			_, _ = w.Write([]byte(
				`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_async_1","name":"memory_view"}}` + "\n\n" +
					`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_async_1","name":"memory_view","arguments":"{\"handle\":\"provider_architecture.md\"}"}}` + "\n\n" +
					`data: {"type":"response.completed","response":{"id":"resp_async_1","status":"completed","model":"gpt-test","usage":{"input_tokens":11,"output_tokens":3}}}` + "\n\n",
			))
		case 2:
			_, _ = w.Write([]byte(
				`data: {"type":"response.output_text.delta","delta":"done"}` + "\n\n" +
					`data: {"type":"response.completed","response":{"id":"resp_async_2","status":"completed","model":"gpt-test","usage":{"input_tokens":13,"output_tokens":2}}}` + "\n\n",
			))
		default:
			t.Fatalf("unexpected request %d", requestIndex)
		}
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	var persisted []AsyncToolCall
	var completed []AsyncToolCallRecord
	var delivered []AsyncToolCallRecord
	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "use memory", &AgenticOptions{
		Model:            "gpt-test",
		SkipDefaultTools: true,
		ExtraTools: []ToolDefinition{{
			Type:        "function",
			Name:        "memory_view",
			Description: "Load memory",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Async:       true,
		}},
		EnableAsyncToolCalls: true,
		OnAsyncToolCall: func(_ context.Context, call AsyncToolCall) (AsyncToolCallRecord, error) {
			persisted = append(persisted, call)
			return AsyncToolCallRecord{ID: "async-1", ResponseID: call.ResponseID, CallID: call.CallID, Name: call.Name}, nil
		},
		OnAsyncToolResult: func(_ context.Context, record AsyncToolCallRecord, output string, isError bool) error {
			if output != "memory output" || isError {
				t.Fatalf("unexpected async result output=%q isError=%v", output, isError)
			}
			completed = append(completed, record)
			return nil
		},
		OnAsyncToolDelivered: func(_ context.Context, record AsyncToolCallRecord) error {
			delivered = append(delivered, record)
			return nil
		},
		ToolExecutor: func(_ context.Context, name string, input json.RawMessage) (string, bool, error) {
			if name != "memory_view" || string(input) != `{"handle":"provider_architecture.md"}` {
				t.Fatalf("unexpected tool execution name=%s input=%s", name, input)
			}
			return "memory output", false, nil
		},
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if resp.Text != "done" {
		t.Fatalf("text = %q, want done", resp.Text)
	}
	if len(persisted) != 1 || persisted[0].ResponseID != "resp_async_1" || persisted[0].CallID != "call_async_1" || persisted[0].Name != "memory_view" {
		t.Fatalf("unexpected persisted calls: %#v", persisted)
	}
	if len(completed) != 1 || len(delivered) != 1 || delivered[0].CallID != "call_async_1" {
		t.Fatalf("completed=%#v delivered=%#v", completed, delivered)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Output != "memory output" {
		t.Fatalf("tool calls = %#v", resp.ToolCalls)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	tools, _ := requests[0]["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", requests[0]["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["async"] != true {
		t.Fatalf("tool missing async true: %#v", tool)
	}
	input, _ := requests[1]["input"].([]any)
	foundOutput := false
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		if item["type"] == "function_call_output" && item["call_id"] == "call_async_1" && item["output"] == "memory output" {
			foundOutput = true
		}
	}
	if !foundOutput {
		t.Fatalf("second request missing async function_call_output: %#v", requests[1]["input"])
	}
}

func TestSendAgentic_AsyncDeliveryProviderRejectionIsReported(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_async_reject","name":"memory_view","arguments":"{}"}}` + "\n\n" +
					`data: {"type":"response.completed","response":{"id":"resp_async_reject","status":"completed","model":"gpt-test"}}` + "\n\n",
			))
			return
		}
		http.Error(w, `{"error":{"message":"bad call_id"}}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	var rejected []AsyncToolCallRecord
	client := NewWithAPIKey("sk-test")
	_, err := client.SendAgentic(context.Background(), "use memory", &AgenticOptions{
		Model:                "gpt-test",
		SkipDefaultTools:     true,
		ExtraTools:           []ToolDefinition{{Type: "function", Name: "memory_view", Async: true}},
		EnableAsyncToolCalls: true,
		OnAsyncToolCall: func(_ context.Context, call AsyncToolCall) (AsyncToolCallRecord, error) {
			return AsyncToolCallRecord{ID: "async-reject", ResponseID: call.ResponseID, CallID: call.CallID, Name: call.Name}, nil
		},
		OnAsyncToolResult: func(context.Context, AsyncToolCallRecord, string, bool) error { return nil },
		OnAsyncToolRejected: func(_ context.Context, record AsyncToolCallRecord, providerErr error) error {
			rejected = append(rejected, record)
			return nil
		},
		ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) {
			return "memory output", false, nil
		},
	})
	if err == nil {
		t.Fatal("expected provider rejection error")
	}
	if len(rejected) != 1 || rejected[0].CallID != "call_async_reject" {
		t.Fatalf("rejected = %#v", rejected)
	}
}

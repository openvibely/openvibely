package geminiclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTurnRetryExecutesToolOnlyAfterSuccessfulResponse(t *testing.T) {
	requests, executions := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch requests {
		case 1:
			sse(w, `{"candidates":[{"content":{"parts":[{"functionCall":{"id":"call","name":"test_tool","args":{}}}]}}]}`)
		case 2:
			sse(w, `{"candidates":[{"content":{"parts":[{"functionCall":{"id":"call","name":"test_tool","args":{}}}]},"finishReason":"STOP"}]}`)
		default:
			sse(w, `{"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}]}`)
		}
	}))
	defer server.Close()
	c := NewWithAPIKey("key")
	c.SetBaseURL(server.URL)
	_, err := c.Send(context.Background(), "hello", &Options{
		Model: "gemini-4-neon", SkipDefaultTools: true, OmitExecutionEnvironment: true,
		ExtraTools: []ToolDefinition{{Name: "test_tool", Parameters: json.RawMessage(`{"type":"object"}`)}},
		ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) {
			executions++
			return "ok", false, nil
		},
	})
	if err != nil || requests != 3 || executions != 1 {
		t.Fatalf("err=%v requests=%d executions=%d", err, requests, executions)
	}
}

func TestTurnRetryPreservesPartialHistoryWhenNextAttemptFails(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			sse(w, `{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`)
			return
		}
		http.Error(w, "invalid request", http.StatusBadRequest)
	}))
	defer server.Close()
	c := NewWithAPIKey("key")
	c.SetBaseURL(server.URL)
	_, err := c.Send(context.Background(), "hello", &Options{Model: "gemini-4-neon", DisableTools: true, OmitExecutionEnvironment: true})
	if err == nil || requests != 2 {
		t.Fatalf("err=%v requests=%d", err, requests)
	}
	history := c.History()
	last := history[len(history)-1]
	if last.Role != "model" || len(last.Parts) != 1 || last.Parts[0].Text != "partial" {
		t.Fatalf("lost partial response: %+v", last)
	}
}

package openaiclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRepairInterruptedToolCalls(t *testing.T) {
	for _, kind := range []string{"function_call", "custom_tool_call"} {
		t.Run(kind, func(t *testing.T) {
			call := map[string]any{"type": kind, "call_id": "interrupted"}
			user := map[string]any{"type": "message", "role": "user", "content": "follow up"}
			original := []any{call, user}
			got := repairInterruptedToolCalls(original)
			require.Equal(t, []any{call, map[string]any{"type": kind + "_output", "call_id": "interrupted", "output": "aborted"}, user}, got)
			require.Len(t, original, 2)
			require.Equal(t, got, repairInterruptedToolCalls(got), "normalization is idempotent")
			completed := []any{call, map[string]any{"type": kind + "_output", "call_id": "interrupted", "output": "real output"}, user}
			require.Equal(t, completed, repairInterruptedToolCalls(completed))
		})
	}
}

func TestSendAgenticRepairsInterruptedSteeringReplay(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(buildSSE([]string{`{"type":"response.completed","response":{"id":"resumed","status":"completed","output":[]}}`})))
	}))
	defer srv.Close()
	previousURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	t.Cleanup(func() { OpenAIAPIBaseURL = previousURL })
	client := newHTTPTestAPIKeyClient("test-key")
	client.History = []Message{{ResponsesInputItems: []any{map[string]any{"type": "function_call", "name": "echo", "arguments": "{}", "call_id": "interrupted"}}}}
	_, err := client.SendAgentic(context.Background(), "follow up", &AgenticOptions{Model: "gpt-test", DisableTools: true})
	require.NoError(t, err)
	input := received["input"].([]any)
	require.Len(t, input, 3)
	require.Equal(t, "aborted", input[1].(map[string]any)["output"])
}

func TestSteeringReplayPersistsToolResultAfterCancellation(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			_, _ = w.Write([]byte(buildSSE([]string{`{"type":"response.completed","response":{"id":"first","status":"completed","output":[]}}`})))
			return
		}
		_, _ = w.Write([]byte(buildSSE([]string{
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"tool_after_steer","name":"echo","arguments":"{}"}}`,
			`{"type":"response.completed","response":{"id":"second","status":"completed"}}`,
		})))
	}))
	defer srv.Close()
	previousURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	t.Cleanup(func() { OpenAIAPIBaseURL = previousURL })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	claimed := false
	var saved []any
	client := newHTTPTestAPIKeyClient("test-key")
	_, err := client.SendAgentic(ctx, "original", &AgenticOptions{
		Model: "gpt-test", MaxTurns: 3, SkipDefaultTools: true,
		ExtraTools: []ToolDefinition{{Type: "function", Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}},
		OnLocalSteering: func(context.Context) (LocalSteeringInput, error) {
			if claimed {
				return LocalSteeringInput{}, nil
			}
			claimed = true
			return LocalSteeringInput{Messages: []LocalSteeringMessage{{Text: "steer"}}, Commit: func(commitCtx context.Context, items []any) error {
				if err := commitCtx.Err(); err != nil {
					return err
				}
				saved = append([]any(nil), items...)
				return nil
			}}, nil
		},
		ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) {
			cancel()
			return "completed side effect", false, nil
		},
	})
	require.Error(t, err)
	var output any
	for _, raw := range saved {
		item := raw.(map[string]any)
		if item["type"] == "function_call_output" && item["call_id"] == "tool_after_steer" {
			output = item["output"]
		}
	}
	require.Equal(t, "completed side effect", output)
}

func TestBuildInputItemsRepairsInterruptedSteeringReplay(t *testing.T) {
	items, err := buildInputItems([]Message{
		{ResponsesInputItems: []any{map[string]any{"type": "function_call", "call_id": "interrupted"}}},
		{Role: "user", Content: "follow up"},
	}, nil)
	require.NoError(t, err)
	require.Len(t, items, 3)
	require.Equal(t, "aborted", items[1].(map[string]any)["output"])
}

func TestSteeringReplayUsesRecoveredAsyncOutputBeforeRepair(t *testing.T) {
	items := []any{map[string]any{"type": "function_call", "call_id": "async", "name": "echo", "arguments": "{}"}}
	recovered := []AsyncToolResult{{Record: AsyncToolCallRecord{ID: "record", CallID: "async", Name: "echo"}, Output: "real output"}}
	for range 2 {
		appendRecoveredAsyncToolResults(&items, recovered, 1000)
		items = repairInterruptedToolCalls(items)
		require.Len(t, items, 2, "recovery must not duplicate the checkpoint's call")
		require.Equal(t, "real output", items[1].(map[string]any)["output"])
	}
}

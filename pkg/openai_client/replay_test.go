package openaiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
	require.Len(t, input, 4)
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

func TestInitialSteeringCommitPrecedesSampling(t *testing.T) {
	for _, failCommit := range []bool{false, true} {
		name := "model failure after commit"
		if failCommit {
			name = "commit failure prevents request"
		}
		t.Run(name, func(t *testing.T) {
			var committed atomic.Bool
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				require.True(t, committed.Load(), "commit must precede provider sampling")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"terminal model failure","type":"invalid_request_error"}}`))
			}))
			defer srv.Close()
			previousURL := OpenAIAPIBaseURL
			OpenAIAPIBaseURL = srv.URL + "/"
			t.Cleanup(func() { OpenAIAPIBaseURL = previousURL })
			client := newHTTPTestAPIKeyClient("test-key")
			var saved []any
			_, err := client.SendAgentic(context.Background(), "late steer", &AgenticOptions{
				Model: "gpt-test", DisableTools: true,
				InitialInputCommit: func(ctx context.Context, history []any) error {
					if failCommit {
						return errors.New("checkpoint failure")
					}
					saved = append([]any(nil), history...)
					committed.Store(true)
					return nil
				},
			})
			require.Error(t, err)
			if failCommit {
				require.ErrorContains(t, err, "checkpoint failure")
				require.Zero(t, requests.Load())
			} else {
				require.EqualValues(t, 1, requests.Load())
				require.Equal(t, "late steer", saved[len(saved)-1].(map[string]any)["content"])
			}
		})
	}
}

func TestHistoryContinuationDoesNotAppendConsumedPrompt(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(buildSSE([]string{`{"type":"response.completed","response":{"id":"continued","status":"completed","output":[]}}`})))
	}))
	defer srv.Close()
	previousURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	t.Cleanup(func() { OpenAIAPIBaseURL = previousURL })
	client := newHTTPTestAPIKeyClient("test-key")
	client.History = []Message{{Role: "user", Content: "recorded steer"}, {Role: "assistant", Content: "recorded result"}}
	_, err := client.SendAgentic(context.Background(), "", &AgenticOptions{Model: "gpt-test", DisableTools: true, ContinueFromHistory: true})
	require.NoError(t, err)
	require.Len(t, received["input"].([]any), 3, "do not append an empty user message when resuming recorded history")
}

func TestResponsesReplayUpdateAppendAndRepair(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, buildSSE([]string{`{"type":"response.completed","response":{"id":"ok","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}}`}))
	}))
	defer srv.Close()
	oldURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldURL }()
	for _, tc := range []struct {
		name             string
		prefix           []any
		resume, replaces bool
	}{
		{"append", []any{map[string]any{"type": "message", "role": "user", "content": "old"}}, false, false},
		{"repair", []any{map[string]any{"type": "function_call", "call_id": "unfinished", "name": "read", "arguments": "{}"}, map[string]any{"type": "message", "role": "user", "content": "old"}}, false, true},
		{"retry", []any{map[string]any{"type": "message", "role": "user", "content": "old"}}, true, true},
		{"unverified-steering", []any{map[string]any{"type": "message", "role": "user", "content": "old"}}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newHTTPTestAPIKeyClient("key")
			c.History = []Message{{ResponsesInputItems: tc.prefix}}
			var updates []ResponsesReplayUpdate
			_, err := c.SendAgentic(context.Background(), "new", &AgenticOptions{Model: "gpt-test", DisableTools: true, OmitExecutionEnvironment: true, ContinueFromHistory: tc.resume, OnReplayStart: func(context.Context, []any) (bool, error) { return tc.name != "unverified-steering", nil }, OnReplayChanged: func(_ context.Context, u ResponsesReplayUpdate) error { updates = append(updates, u); return nil }})
			require.NoError(t, err)
			require.NotEmpty(t, updates)
			for _, u := range updates {
				require.Equal(t, tc.replaces, u.ReplacesHistory)
			}
			raw, err := json.Marshal(updates[len(updates)-1].Items)
			require.NoError(t, err)
			if tc.replaces {
				if tc.name == "repair" {
					require.Contains(t, string(raw), "aborted")
				} else {
					require.Contains(t, string(raw), "old")
				}
			} else {
				require.NotContains(t, string(raw), "old", "normal saves contain only this exchange")
				require.Contains(t, string(raw), "new")
			}
		})
	}
}

func TestReplayPreparationFailureDoesNotSendOrSave(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusBadRequest) }))
	defer srv.Close()
	oldURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldURL }()
	c := newHTTPTestAPIKeyClient("key")
	c.History = []Message{{Role: "user", Content: "old"}}
	saved := false
	_, err := c.SendAgentic(context.Background(), "new", &AgenticOptions{
		Model: "gpt-test", DisableTools: true,
		OnReplayStart:   func(context.Context, []any) (bool, error) { return false, errors.New("history read failed") },
		OnReplayChanged: func(context.Context, ResponsesReplayUpdate) error { saved = true; return nil },
	})
	require.ErrorContains(t, err, "history read failed")
	require.Zero(t, requests.Load())
	require.False(t, saved)
}

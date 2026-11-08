package openaiclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/stretchr/testify/require"
)

// Permission can be revoked after preparation (including while tools are
// refreshed). A failed commit must stop the next provider request, not merely
// fail the history write after the instruction has already been sent.
func TestSteeringAuthorizationFailureStopsProviderContinuation(t *testing.T) {
	for _, toolBoundary := range []bool{false, true} {
		name := "after_response"
		if toolBoundary {
			name = "after_tool"
		}
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				events := []string{}
				if toolBoundary {
					events = append(events, `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_auth","name":"echo","arguments":"{}"}}`)
				}
				events = append(events, `{"type":"response.completed","response":{"id":"resp_auth","status":"completed","model":"gpt-test"}}`)
				_, _ = w.Write([]byte(buildSSE(events)))
			}))
			defer srv.Close()
			oldURL := OpenAIAPIBaseURL
			OpenAIAPIBaseURL = srv.URL + "/"
			defer func() { OpenAIAPIBaseURL = oldURL }()
			client := newHTTPTestAPIKeyClient("test-key")
			client.responsesTransportState.websocketDisabled.Store(true)
			denied := errors.New("sender no longer has project access")
			commits := 0
			_, err := client.SendAgentic(context.Background(), "original", &AgenticOptions{
				Model: "gpt-test", MaxTurns: 1, SkipDefaultTools: true,
				ExtraTools:   []ToolDefinition{{Type: "function", Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}},
				ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) { return "ok", false, nil },
				OnLocalSteering: func(context.Context) (LocalSteeringInput, error) {
					return LocalSteeringInput{Text: "revoked instruction", Commit: func(_ context.Context, history []any) error {
						commits++
						encoded, encodeErr := json.Marshal(history)
						require.NoError(t, encodeErr)
						require.Contains(t, string(encoded), "revoked instruction")
						return denied
					}}, nil
				},
			})
			require.ErrorIs(t, err, denied)
			require.Equal(t, 1, commits)
			require.EqualValues(t, 1, requests.Load(), "denied steering must never reach a continuation request")
		})
	}
}

func TestModelSendAuthorizationRunsAfterToolFiltering(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer srv.Close()
	oldURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldURL }()
	denied := errors.New("reviewer removed after preparation")
	for _, websocket := range []bool{false, true} {
		client := newHTTPTestAPIKeyClient("test-key")
		client.supportsResponsesWebsockets = websocket
		revoked := false
		checks := 0
		ctx := llmcontracts.WithBeforeModelSend(context.Background(), func(context.Context) error {
			checks++
			if revoked {
				return denied
			}
			return nil
		})
		_, err := client.sendAgenticTurnOnce(ctx, []any{}, []ToolDefinition{{Type: "function", Name: "echo"}}, &AgenticOptions{Model: "gpt-test", ToolFilter: func(string) bool { revoked = true; return true }}, false)
		require.ErrorIs(t, err, denied)
		require.Equal(t, 1, checks)
	}
	require.Zero(t, requests.Load(), "revoked request must not reach HTTP or websocket transport")
}

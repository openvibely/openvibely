package openaiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/httpretry"
)

func TestStandardWebSearchPermission(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		for _, allowed := range []bool{false, true} {
			t.Run(fmt.Sprintf("oauth=%v/allowed=%v", oauth, allowed), func(t *testing.T) {
				var advertised bool
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request map[string]any
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						return
					}
					tools, _ := request["tools"].([]any)
					for _, raw := range tools {
						if raw.(map[string]any)["type"] == openAIWebSearchToolType {
							advertised = true
						}
					}
					_, _ = w.Write([]byte(buildSSE([]string{`{"type":"response.completed","response":{"id":"done","status":"completed"}}`})))
				}))
				defer srv.Close()
				oldAPI, oldOAuth := OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL
				OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL = srv.URL, srv.URL
				defer func() { OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL = oldAPI, oldOAuth }()
				client := NewWithAPIKey("test")
				if oauth {
					client = NewWithOAuthToken(testOAuthJWT("org_test"), "refresh", time.Now().Add(2*time.Hour).UnixMilli(), "org_test")
				}
				_, err := client.SendAgentic(context.Background(), "hello", &AgenticOptions{Model: "gpt-5.5", MaxTurns: 1, WebSearchEnabled: true, ToolFilter: func(name string) bool { return allowed && name == openAIWebSearchToolType }})
				if err != nil {
					t.Fatal(err)
				}
				if advertised != allowed {
					t.Fatalf("advertised=%v allowed=%v", advertised, allowed)
				}
			})
		}
	}
}

func TestTransientStreamReconnectsWebsocket(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		for _, agentic := range []bool{false, true} {
			t.Run(fmt.Sprintf("oauth=%v/agentic=%v", oauth, agentic), func(t *testing.T) {
				var connections atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet {
						t.Error("transient failure switched to HTTP")
						http.Error(w, "unexpected fallback", 400)
						return
					}
					attempt := connections.Add(1)
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.CloseNow()
					if _, _, err = conn.Read(r.Context()); err != nil {
						t.Error(err)
						return
					}
					if attempt == 1 {
						_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"interrupted"}}`))
						return
					}
					for turn := 0; turn < 2; turn++ {
						if turn > 0 {
							if _, _, err = conn.Read(r.Context()); err != nil {
								t.Error(err)
								return
							}
						}
						_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"done","status":"completed"}}`))
					}
				}))
				defer srv.Close()
				oldAPI, oldOAuth := OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL
				OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL = srv.URL, srv.URL
				defer func() { OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL = oldAPI, oldOAuth }()
				client := NewWithAPIKey("test")
				if oauth {
					client = NewWithOAuthToken(testOAuthJWT("org_test"), "refresh", time.Now().Add(2*time.Hour).UnixMilli(), "org_test")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				for i := 0; i < 2; i++ {
					var err error
					if agentic {
						_, err = client.SendAgentic(ctx, "hello", &AgenticOptions{Model: "gpt-6-sol", MaxTurns: 1, DisableTools: true})
					} else {
						_, err = client.Send(ctx, "hello", &SendOptions{Model: "gpt-6-sol"})
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if connections.Load() != 2 || client.responsesTransportState.websocketDisabled.Load() {
					t.Fatal("WebSocket did not recover and stay enabled")
				}
			})
		}
	}
}

func TestResponsesFallbackBudgets(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-5.5"} {
		client := NewWithAPIKey("test")
		wsAttempts, httpAttempts := 0, 0
		_, err := doResponsesStreamTurn(context.Background(), client, model, httpretry.StreamTurnPolicy{
			MaxRetries: 2, RetryableError: isRetryableResponsesTransportError,
			After: func(time.Duration) <-chan time.Time { ch := make(chan time.Time, 1); ch <- time.Now(); return ch },
		}, func(context.Context) (int, error) {
			if client.responsesTransportState.websocketDisabled.Load() {
				httpAttempts++
			} else {
				wsAttempts++
			}
			return 0, errResponsesWebsocketTransport
		})
		if err == nil || wsAttempts != 3 {
			t.Fatalf("model=%s attempts=%d err=%v", model, wsAttempts, err)
		}
		wantHTTP := 0
		if model == "gpt-6-sol" {
			wantHTTP = 3
		}
		if httpAttempts != wantHTTP {
			t.Fatalf("model=%s HTTP attempts=%d want=%d", model, httpAttempts, wantHTTP)
		}
	}
}

func TestTerminalWebsocketHandshakeDoesNotRetryOrFallback(t *testing.T) {
	for _, status := range []int{400, 401, 403, 429} {
		for _, oauth := range []bool{false, true} {
			for _, agentic := range []bool{false, true} {
				t.Run(fmt.Sprintf("status=%d/oauth=%v/agentic=%v", status, oauth, agentic), func(t *testing.T) {
					var attempts atomic.Int32
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						attempts.Add(1)
						if r.Method != http.MethodGet {
							t.Error("terminal rejection triggered HTTP fallback")
						}
						w.WriteHeader(status)
						_, _ = w.Write([]byte(`{"error":{"code":"denied","message":"Request denied"}}`))
					}))
					defer srv.Close()
					oldAPI, oldOAuth := OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL
					OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL = srv.URL, srv.URL
					defer func() { OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL = oldAPI, oldOAuth }()
					client := NewWithAPIKey("test")
					if oauth {
						client = NewWithOAuthToken(testOAuthJWT("org_test"), "refresh", time.Now().Add(2*time.Hour).UnixMilli(), "org_test")
						client.SetOAuthUnauthorizedHandler(func(context.Context, string) (OAuthTokens, bool, error) { return OAuthTokens{}, false, nil })
					}
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					var err error
					if agentic {
						_, err = client.SendAgentic(ctx, "hello", &AgenticOptions{Model: "gpt-6-sol", MaxTurns: 1, DisableTools: true})
					} else {
						_, err = client.Send(ctx, "hello", &SendOptions{Model: "gpt-6-sol"})
					}
					var apiErr *APIError
					if !errors.As(err, &apiErr) || apiErr.StatusCode != status || apiErr.Code != "denied" {
						t.Fatalf("provider error not preserved: %v", err)
					}
					if attempts.Load() != 1 || client.responsesTransportState.websocketDisabled.Load() {
						t.Fatalf("attempts=%d disabled=%v", attempts.Load(), client.responsesTransportState.websocketDisabled.Load())
					}
				})
			}
		}
	}
}

func TestCompactionConnectionFailuresHaveBoundedRetries(t *testing.T) {
	client := NewWithAPIKey("test")
	var websocketAttempts, httpAttempts atomic.Int32
	client.SetHTTPClient(&http.Client{Transport: completionsRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			websocketAttempts.Add(1)
		} else {
			httpAttempts.Add(1)
		}
		return nil, fmt.Errorf("connection refused")
	})})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, _, err := client.compactAgenticInputItemsViaResponsesV2(ctx, []any{map[string]any{"type": "message", "role": "user", "content": "old message"}}, nil, &AgenticOptions{Model: "gpt-6-sol"})
	if err == nil || ctx.Err() != nil {
		t.Fatalf("compaction did not fail within retry budget: %v", err)
	}
	if websocketAttempts.Load() != 3 || httpAttempts.Load() != 3 {
		t.Fatalf("attempts WS=%d HTTP=%d, want three each", websocketAttempts.Load(), httpAttempts.Load())
	}
}

func TestSteeringStopsUnsafeRetryOrTools(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna"} {
		for _, scenario := range []string{"accepted_disconnect", "unacknowledged_disconnect", "unacknowledged_completion", "committed_disconnect", "committed_completion"} {
			t.Run(model+"/"+scenario, func(t *testing.T) {
				var attempts, executions atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempt := attempts.Add(1)
					if r.Method != http.MethodGet {
						t.Error("unexpected HTTP fallback")
						http.Error(w, "unexpected fallback", 400)
						return
					}
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.CloseNow()
					if _, _, err = conn.Read(r.Context()); err != nil {
						t.Error(err)
						return
					}
					if attempt == 1 {
						_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"original"}}`))
						if _, _, err = conn.Read(r.Context()); err != nil {
							t.Error(err)
							return
						}
						if scenario == "accepted_disconnect" || strings.HasPrefix(scenario, "committed_") {
							_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.steer.accepted","steer":{"id":"steer1","previous_response_id":"original"}}`))
						}
						if strings.HasPrefix(scenario, "committed_") {
							_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.incomplete","response":{"id":"original","incomplete_details":{"reason":"steered"}}}`))
							_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"successor"}}`))
						}
						if scenario != "unacknowledged_completion" && scenario != "committed_completion" {
							return
						}
					}
					// Also return a tool on an erroneous retry, so the test detects
					// unsafe execution rather than merely timing out on a disconnect.
					_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call1","name":"echo","arguments":"{}"}}`))
					_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"done","status":"completed"}}`))
				}))
				defer srv.Close()
				old := OpenAIAPIBaseURL
				OpenAIAPIBaseURL = srv.URL
				defer func() { OpenAIAPIBaseURL = old }()
				client := NewWithAPIKey("test")
				var delivered atomic.Bool
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_, err := client.SendAgentic(ctx, "do work", &AgenticOptions{
					Model: model, MaxTurns: 1, EnableAstraMidTurnSteering: true,
					OnAstraMidTurnSteering: func(ctx context.Context, deliver AstraSteeringDeliverer) error {
						if delivered.Swap(true) {
							return nil
						}
						_, err := deliver(ctx, "stop tool execution")
						return err
					},
					ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) {
						executions.Add(1)
						return "mock only", false, nil
					},
				})
				if scenario == "committed_completion" {
					if err != nil || attempts.Load() != 1 || executions.Load() != 1 {
						t.Fatalf("successful successor blocked: attempts=%d executions=%d err=%v", attempts.Load(), executions.Load(), err)
					}
					return
				}
				if scenario == "committed_disconnect" {
					var committed *astraSteeringCommittedError
					if !errors.As(err, &committed) || len(committed.ids) != 1 || committed.ids[0] != "steer1" {
						t.Fatalf("lost committed steering identity: %v", err)
					}
					if !strings.Contains(err.Error(), "cannot retry after committed steering") {
						t.Fatalf("expected committed steering retry veto, got %v", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "steering delivery is unresolved") {
					t.Fatalf("expected unresolved steering error, got %v", err)
				}
				if attempts.Load() != 1 || executions.Load() != 0 {
					t.Fatalf("attempts=%d tool executions=%d", attempts.Load(), executions.Load())
				}
				if client.responsesTransportState.websocketDisabled.Load() {
					t.Fatal("uncertain steering disabled WebSocket")
				}
				if scenario == "accepted_disconnect" {
					var ambiguous *astraSteeringAmbiguousError
					if !errors.As(err, &ambiguous) || len(ambiguous.ids) != 1 || ambiguous.ids[0] != "steer1" {
						t.Fatalf("lost durable steering identity: %v", err)
					}
				}
			})
		}
	}
}

func TestUncertainSteeringVetoesOverflowRecovery(t *testing.T) {
	client := NewWithAPIKey("test")
	calledRecovery := false
	_, err := doResponsesStreamTurn(context.Background(), client, "gpt-6-sol", httpretry.StreamTurnPolicy{
		Recover: func(error) (bool, error) { calledRecovery = true; return false, nil },
	}, func(context.Context) (int, error) {
		client.responsesTransportState.recordSteeringDelivery(ResponsesSteeringDelivery{Status: AstraSteeringAmbiguous})
		return 0, &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "too much input"}
	})
	if err == nil || calledRecovery {
		t.Fatalf("err=%v recovery=%v", err, calledRecovery)
	}
}

func TestCommittedSteeringVetoesOverflowRecovery(t *testing.T) {
	client := NewWithAPIKey("test")
	calledRecovery := false
	attempts := 0
	_, err := doResponsesStreamTurn(context.Background(), client, "gpt-6-sol", httpretry.StreamTurnPolicy{
		Recover: func(error) (bool, error) { calledRecovery = true; return false, nil },
	}, func(context.Context) (int, error) {
		attempts++
		state := client.responsesTransportState
		state.mu.Lock()
		state.appendAstraSteeringCommitLocked(ResponsesSteeringDelivery{SteeringID: "steer1", ResponseID: "successor"})
		state.mu.Unlock()
		return 0, &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "too much input"}
	})
	if err == nil || calledRecovery || attempts != 1 {
		t.Fatalf("err=%v recovery=%v attempts=%d", err, calledRecovery, attempts)
	}
	if !client.responsesTransportState.hasAstraSteeringCommits() {
		t.Fatal("retry guard consumed durable steering commits")
	}
}

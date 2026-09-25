package openaiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

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
)

func TestResponsesPromptCacheKeyStableAcrossTurnsAndFallback(t *testing.T) {
	for _, model := range []string{"gpt-5.5", "gpt-6-sol"} {
		for _, oauth := range []bool{false, true} {
			for _, agentic := range []bool{false, true} {
				for _, transport := range []string{"http", "websocket_fallback"} {
					t.Run(fmt.Sprintf("%s/oauth=%v/agentic=%v/%s", model, oauth, agentic, transport), func(t *testing.T) {
						type capturedRequest struct {
							transport string
							key       string
						}
						requests := make(chan capturedRequest, 16)
						capture := func(data []byte, transport string) {
							var payload struct {
								Key string `json:"prompt_cache_key"`
							}
							if err := json.Unmarshal(data, &payload); err != nil {
								t.Errorf("decode %s request: %v", transport, err)
								return
							}
							requests <- capturedRequest{transport: transport, key: payload.Key}
						}
						events := []string{
							`{"type":"response.output_text.delta","delta":"ok"}`,
							`{"type":"response.completed","response":{"status":"completed"}}`,
						}
						var handshakes atomic.Int32
						srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							if r.Method == http.MethodGet {
								if handshakes.Add(1) > 1 {
									http.Error(w, "websocket unavailable", http.StatusUpgradeRequired)
									return
								}
								conn, err := websocket.Accept(w, r, nil)
								if err != nil {
									t.Errorf("accept websocket: %v", err)
									return
								}
								defer conn.CloseNow()
								for turn := 0; turn < 2; turn++ {
									_, data, err := conn.Read(r.Context())
									if err != nil {
										t.Errorf("read websocket: %v", err)
										return
									}
									capture(data, "websocket")
									for _, event := range events {
										if err := conn.Write(r.Context(), websocket.MessageText, []byte(event)); err != nil {
											t.Errorf("write websocket: %v", err)
											return
										}
									}
								}
								return
							}
							var payload json.RawMessage
							if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
								t.Errorf("decode HTTP request: %v", err)
								http.Error(w, "invalid request", 400)
								return
							}
							capture(payload, "http")
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = w.Write([]byte(buildSSE(events)))
						}))
						defer srv.Close()
						oldAPI, oldOAuth := OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL
						OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL = srv.URL, srv.URL
						defer func() { OpenAIAPIBaseURL, OpenAIChatGPTAPIBaseURL = oldAPI, oldOAuth }()

						newClient := func() *Client {
							if oauth {
								return NewWithOAuthToken(testOAuthJWT("org_test"), "refresh", time.Now().Add(24*time.Hour).UnixMilli(), "org_test")
							}
							return NewWithAPIKey("test")
						}
						client := newClient()
						state := client.responsesTransportState
						defer state.Close()
						wantKey := client.sessionID
						if wantKey == "" {
							t.Fatal("empty client session ID")
						}
						if transport == "http" {
							client.supportsResponsesWebsockets = false
						}
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						for turn := 0; turn < 4; turn++ {
							if turn == 2 {
								// Recreating a client with the same transport state must keep its cache key.
								// The next WebSocket handshake is rejected so the remaining turns use HTTP.
								state.Close()
								client = newClient()
								if client.sessionID == wantKey {
									t.Fatal("independent client reused the previous session ID")
								}
								client.SetResponsesTransportState(state)
								if transport == "http" {
									client.supportsResponsesWebsockets = false
								}
							}
							if agentic {
								response, err := client.SendAgentic(ctx, "hello", &AgenticOptions{Model: model, MaxTurns: 1, DisableTools: true})
								if err != nil {
									t.Fatal(err)
								}
								if response.Text != "ok" {
									t.Fatalf("response = %q", response.Text)
								}
							} else {
								response, err := client.Send(ctx, "hello", &SendOptions{Model: model, Stream: true})
								if err != nil {
									t.Fatal(err)
								}
								if response.Text != "ok" {
									t.Fatalf("response = %q", response.Text)
								}
							}
							wantTransport := "http"
							if transport == "websocket_fallback" && turn < 2 {
								wantTransport = "websocket"
							}
							select {
							case request := <-requests:
								if request.transport != wantTransport || request.key != wantKey {
									t.Fatalf("turn %d: transport/key = %s/%q, want %s/%q", turn, request.transport, request.key, wantTransport, wantKey)
								}
							default:
								t.Fatalf("turn %d: no request captured", turn)
							}
						}
						if len(requests) != 0 {
							t.Fatalf("unexpected extra requests: %d", len(requests))
						}
						if transport == "websocket_fallback" {
							if handshakes.Load() != 2 || !state.websocketDisabled.Load() {
								t.Fatalf("fallback did not persist: handshakes=%d disabled=%v", handshakes.Load(), state.websocketDisabled.Load())
							}
						} else if handshakes.Load() != 0 {
							t.Fatal("HTTP mode attempted WebSocket")
						}
					})
				}
			}
		}
	}
}

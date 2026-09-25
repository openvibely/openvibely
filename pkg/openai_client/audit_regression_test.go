package openaiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestStandaloneWebSearchRespectsRuntimeFilter(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		called := false
		opts := &AgenticOptions{
			ToolFilter: func(string) bool { return allowed },
			standaloneWebSearch: func(context.Context, json.RawMessage) (WebSearchResult, bool, error) {
				called = true
				return WebSearchResult{Output: "searched"}, false, nil
			},
		}
		_, failed, result := runOpenAIToolTask(context.Background(), opts, "web.run", json.RawMessage(`{}`))
		if called != allowed || failed == allowed || (result != nil) != allowed {
			t.Fatalf("allowed=%v called=%v failed=%v result=%v", allowed, called, failed, result)
		}
	}
}

func TestStandaloneWebSearchFilteredFromRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		_, data, err := conn.Read(r.Context())
		if err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(string(data), `web.run`) {
			t.Error("denied web.run advertised to model")
		}
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"done","status":"completed"}}`))
	}))
	defer srv.Close()
	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL
	defer func() { OpenAIAPIBaseURL = original }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := NewWithAPIKey("test").SendAgentic(ctx, "hello", &AgenticOptions{Model: "gpt-6-luna", MaxTurns: 1, WebSearchEnabled: true, ToolFilter: func(string) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
}

func TestResponsesWebsocketRecoverableProviderErrors(t *testing.T) {
	for _, code := range []string{"previous_response_not_found", "websocket_connection_limit_reached"} {
		for _, agentic := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/agentic=%v", code, agentic), func(t *testing.T) {
				var connections atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet {
						t.Error("unexpected HTTP fallback")
						http.Error(w, "no fallback", 400)
						return
					}
					attempt := connections.Add(1)
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.CloseNow()
					_, data, err := conn.Read(r.Context())
					if err != nil {
						t.Error(err)
						return
					}
					if attempt == 1 {
						_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"first","status":"completed"}}`))
						_, data, err = conn.Read(r.Context())
						if err != nil {
							t.Error(err)
							return
						}
						if !strings.Contains(string(data), `"previous_response_id":"first"`) {
							t.Errorf("expected incremental request: %s", data)
						}
						_ = conn.Write(r.Context(), websocket.MessageText, []byte(fmt.Sprintf(`{"type":"error","status":400,"error":{"code":%q,"message":%q}}`, code, code)))
						return
					}
					var request map[string]any
					if err := json.Unmarshal(data, &request); err != nil {
						t.Error(err)
						return
					}
					if request["previous_response_id"] != nil || !strings.Contains(string(data), "original message") || !strings.Contains(string(data), "next message") {
						t.Errorf("retry did not replay full input: %s", data)
					}
					_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"second","status":"completed"}}`))
				}))
				defer srv.Close()
				original := OpenAIAPIBaseURL
				OpenAIAPIBaseURL = srv.URL
				defer func() { OpenAIAPIBaseURL = original }()
				client := NewWithAPIKey("test")
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				for _, prompt := range []string{"original message", "next message"} {
					var err error
					if agentic {
						_, err = client.SendAgentic(ctx, prompt, &AgenticOptions{Model: "gpt-6-luna", DisableTools: true, MaxTurns: 1})
					} else {
						_, err = client.Send(ctx, prompt, &SendOptions{Model: "gpt-6-luna", Stream: true})
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if connections.Load() != 2 {
					t.Fatalf("connections=%d, want 2", connections.Load())
				}
			})
		}
	}
}

func TestCompactionUsageAndSteeringIsolation(t *testing.T) {
	var phase atomic.Int32
	var duringCompaction atomic.Int32
	steered := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		for turn := 0; turn < 2; turn++ {
			if _, _, err = conn.Read(r.Context()); err != nil {
				t.Error(err)
				return
			}
			if turn == 0 {
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"compact"}}`))
				// Allow the normal steering poll to run if accidentally enabled.
				select {
				case <-steered:
					t.Error("steering callback ran during compaction")
				case <-time.After(200 * time.Millisecond):
				}
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"opaque"}}`))
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"compact","status":"completed","usage":{"input_tokens":100,"output_tokens":50,"input_tokens_details":{"cached_tokens":20},"output_tokens_details":{"reasoning_tokens":30}}}}`))
			} else {
				phase.Store(1)
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"normal"}}`))
				select {
				case <-steered:
				case <-time.After(2 * time.Second):
					t.Error("normal steering disabled")
				}
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"normal","status":"completed","usage":{"input_tokens":10,"output_tokens":1,"input_tokens_details":{"cached_tokens":2}},"output":[{"type":"message","content":[{"type":"output_text","text":"2"}]}]}}`))
			}
		}
	}))
	defer srv.Close()
	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL
	defer func() { OpenAIAPIBaseURL = original }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opts := &AgenticOptions{Model: "gpt-6-astra", MaxTurns: 1, AutoCompaction: true, ForceCompactionBeforeTurn: true, InitialInputItems: []any{map[string]any{"type": "message", "role": "user", "content": "old message"}}, EnableAstraMidTurnSteering: true, OnAstraMidTurnSteering: func(context.Context, AstraSteeringDeliverer) error {
		if phase.Load() == 0 {
			duringCompaction.Add(1)
		}
		select {
		case steered <- struct{}{}:
		default:
		}
		return nil
	}}
	result, err := NewWithAPIKey("test").SendAgentic(ctx, "1+1=", opts)
	if err != nil {
		t.Fatal(err)
	}
	if duringCompaction.Load() != 0 {
		t.Fatal("compaction consumed steering")
	}
	if !opts.EnableAstraMidTurnSteering || opts.OnAstraMidTurnSteering == nil {
		t.Fatal("mutated caller steering options")
	}
	if !result.Compacted || result.InputTokens != 110 || result.OutputTokens != 51 || result.CachedInputTokens != 22 || result.ReasoningTokens != 30 || result.LastContextTokens != 11 {
		t.Fatalf("incorrect usage: %+v", result)
	}
}

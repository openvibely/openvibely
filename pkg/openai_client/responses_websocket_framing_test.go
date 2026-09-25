package openaiclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

// Provider error frames are pretty-printed JSON. Forwarding one as "data: %s"
// without compacting it loses the envelope in the downstream line-based parser.
func TestResponsesWebsocketMultilineEvents(t *testing.T) {
	for _, agentic := range []bool{false, true} {
		for _, failure := range []bool{false, true} {
			name := "send"
			if agentic {
				name = "agentic"
			}
			if failure {
				name += "/error"
			} else {
				name += "/completion"
			}
			t.Run(name, func(t *testing.T) {
				var attempts atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempts.Add(1)
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
					event := `{
       "type": "response.completed",
       "response": {"id":"resp_test", "status":"completed", "model":"gpt-5.6-luna", "output":[{"type":"message","content":[{"type":"output_text","text":"2"}]}]}
     }`
					if failure {
						event = `{
       "type": "error",
       "status": 400,
       "error": {"type":"invalid_request_error","message":"Invalid Value: 'tools'. Function 'web.run' is reserved for use by this model and must match the configured schema."}
     }`
					}
					if err = conn.Write(r.Context(), websocket.MessageText, []byte(event)); err != nil {
						t.Error(err)
					}
				}))
				defer srv.Close()
				original := OpenAIAPIBaseURL
				OpenAIAPIBaseURL = srv.URL
				defer func() { OpenAIAPIBaseURL = original }()
				client := NewWithAPIKey("test-key")
				var err error
				var output string
				if agentic {
					var result *AgenticResponse
					result, err = client.SendAgentic(context.Background(), "1+1=", &AgenticOptions{Model: "gpt-5.6-luna", DisableTools: true, MaxTurns: 1})
					if result != nil {
						output = result.Text
					}
				} else {
					var result *Response
					result, err = client.Send(context.Background(), "1+1=", &SendOptions{Model: "gpt-5.6-luna", Stream: true})
					if result != nil {
						output = result.Text
					}
				}
				if failure {
					var apiErr *APIError
					if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 || apiErr.Type != "invalid_request_error" {
						t.Fatalf("error = %v; want original provider 400", err)
					}
				} else if err != nil || output != "2" {
					t.Fatalf("output=%q error=%v", output, err)
				}
				if attempts.Load() != 1 {
					t.Fatalf("attempts=%d; want one WebSocket request", attempts.Load())
				}
			})
		}
	}
}

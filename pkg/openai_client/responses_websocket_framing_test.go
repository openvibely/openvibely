package openaiclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestResponsesWebsocketLightweightDecodeAndFraming(t *testing.T) {
	cases := []struct {
		name         string
		frame        []byte
		wantType     string
		wantMetadata bool
	}{
		{
			name:         "text delta only reads envelope",
			frame:        []byte(`{"type":"response.output_text.delta","delta":"hello"}`),
			wantType:     "response.output_text.delta",
			wantMetadata: false,
		},
		{
			name:         "output item retains metadata",
			frame:        []byte(`{"type":"response.output_item.done","item":{"type":"reasoning","id":"item_1"}}`),
			wantType:     "response.output_item.done",
			wantMetadata: true,
		},
		{
			name:         "completion retains metadata",
			frame:        []byte(`{"type":"response.completed","response":{"id":"resp_1"}}`),
			wantType:     "response.completed",
			wantMetadata: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eventType, event, validJSON := decodeResponsesWebsocketEvent(tc.frame)
			if !validJSON || eventType != tc.wantType {
				t.Fatalf("decoded type=%q valid=%t, want type=%q valid=true", eventType, validJSON, tc.wantType)
			}
			if (event != nil) != tc.wantMetadata {
				t.Fatalf("decoded metadata presence=%t, want %t", event != nil, tc.wantMetadata)
			}
			var framed bytes.Buffer
			invalidJSON, err := forwardResponsesWebsocketEvent(&framed, tc.frame, validJSON)
			if err != nil || invalidJSON {
				t.Fatalf("forward event invalid=%t error=%v", invalidJSON, err)
			}
			if got, want := framed.String(), "data: "+string(tc.frame)+"\n\n"; got != want {
				t.Fatalf("framed event = %q, want unchanged single-line frame %q", got, want)
			}
		})
	}

	multiline := []byte("{\n  \"type\": \"error\",\n  \"error\": {\"message\": \"provider failure\"}\n}")
	eventType, event, validJSON := decodeResponsesWebsocketEvent(multiline)
	if !validJSON || eventType != "error" || event == nil {
		t.Fatalf("multiline event decode = (%q, %#v, %t)", eventType, event, validJSON)
	}
	var framed bytes.Buffer
	invalidJSON, err := forwardResponsesWebsocketEvent(&framed, multiline, validJSON)
	if err != nil || invalidJSON {
		t.Fatalf("forward multiline event invalid=%t error=%v", invalidJSON, err)
	}
	if got, want := framed.String(), "data: {\"type\":\"error\",\"error\":{\"message\":\"provider failure\"}}\n\n"; got != want {
		t.Fatalf("framed multiline event = %q, want compacted event %q", got, want)
	}

	malformed := []byte(`{"type":"response.completed"`)
	_, _, validJSON = decodeResponsesWebsocketEvent(malformed)
	if validJSON {
		t.Fatal("malformed event was marked valid")
	}
	invalidJSON, err = forwardResponsesWebsocketEvent(&framed, malformed, validJSON)
	if err == nil || !invalidJSON {
		t.Fatalf("malformed event forward invalid=%t error=%v, want invalid JSON error", invalidJSON, err)
	}
}

func TestResponsesWebsocketFramingPreservesManyDeltaOrder(t *testing.T) {
	const deltaCount = 2048
	var framed bytes.Buffer
	var expected strings.Builder
	for i := 0; i < deltaCount; i++ {
		delta := fmt.Sprintf("%04d,", i)
		expected.WriteString(delta)
		frame := []byte(fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q}`, delta))
		eventType, _, validJSON := decodeResponsesWebsocketEvent(frame)
		if eventType != "response.output_text.delta" || !validJSON {
			t.Fatalf("delta %d decoded as type=%q valid=%t", i, eventType, validJSON)
		}
		if _, err := forwardResponsesWebsocketEvent(&framed, frame, validJSON); err != nil {
			t.Fatalf("forward delta %d: %v", i, err)
		}
	}
	completion := []byte(`{"type":"response.completed","response":{"id":"resp_many","status":"completed","model":"gpt-5.6-luna","output":[]}}`)
	_, _, validJSON := decodeResponsesWebsocketEvent(completion)
	if _, err := forwardResponsesWebsocketEvent(&framed, completion, validJSON); err != nil {
		t.Fatalf("forward completion: %v", err)
	}
	want := expected.String()
	stream := framed.String()

	var standardDeltas strings.Builder
	standard, err := parseStreamingResponse(strings.NewReader(stream), func(delta string) { _, _ = standardDeltas.WriteString(delta) }, true)
	if err != nil {
		t.Fatalf("standard parser: %v", err)
	}
	if standard.Text != want || standardDeltas.String() != want {
		t.Fatalf("standard text/callback mismatch: text length=%d callback length=%d want length=%d", len(standard.Text), standardDeltas.Len(), len(want))
	}

	var agenticDeltas strings.Builder
	agentic, err := (&Client{}).parseAgenticStreamWithToolCallbacks(strings.NewReader(stream), func(delta string) { _, _ = agenticDeltas.WriteString(delta) }, nil, nil, nil)
	if err != nil {
		t.Fatalf("agentic parser: %v", err)
	}
	if agentic.text != want || agenticDeltas.String() != want {
		t.Fatalf("agentic text/callback mismatch: text length=%d callback length=%d want length=%d", len(agentic.text), agenticDeltas.Len(), len(want))
	}
}

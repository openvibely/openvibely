package openaiclient

import (
	"bytes"
	"context"
	"encoding/json"
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
	var decoder responsesWebsocketDecoder
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
			eventType, event, validJSON := decoder.decode(tc.frame)
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
	eventType, event, validJSON := decoder.decode(multiline)
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
	_, _, validJSON = decoder.decode(malformed)
	if validJSON {
		t.Fatal("malformed event was marked valid")
	}
	invalidJSON, err = forwardResponsesWebsocketEvent(&framed, malformed, validJSON)
	if err == nil || !invalidJSON {
		t.Fatalf("malformed event forward invalid=%t error=%v, want invalid JSON error", invalidJSON, err)
	}
}

func TestResponsesWebsocketFramingPreservesManyDeltaOrder(t *testing.T) {
	var decoder responsesWebsocketDecoder
	const deltaCount = 2048
	var framed bytes.Buffer
	var expected strings.Builder
	for i := 0; i < deltaCount; i++ {
		delta := fmt.Sprintf("%04d,", i)
		expected.WriteString(delta)
		frame := []byte(fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q}`, delta))
		eventType, _, validJSON := decoder.decode(frame)
		if eventType != "response.output_text.delta" || !validJSON {
			t.Fatalf("delta %d decoded as type=%q valid=%t", i, eventType, validJSON)
		}
		if _, err := forwardResponsesWebsocketEvent(&framed, frame, validJSON); err != nil {
			t.Fatalf("forward delta %d: %v", i, err)
		}
	}
	completion := []byte(`{"type":"response.completed","response":{"id":"resp_many","status":"completed","model":"gpt-5.6-luna","output":[]}}`)
	_, _, validJSON := decoder.decode(completion)
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

func TestResponsesWebsocketReusesReaderWithoutCorruptingSavedItems(t *testing.T) {
	contents := []string{strings.Repeat("private ", 8), strings.Repeat("large private ", 10000)}
	var connections atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		for i, content := range contents {
			if _, _, err := conn.Read(r.Context()); err != nil {
				t.Error(err)
				return
			}
			item := map[string]any{"type": "reasoning", "id": fmt.Sprintf("reasoning_%d", i), "encrypted_content": content}
			events := []any{
				map[string]any{"type": "response.output_item.done", "item": item},
				map[string]any{"type": "response.output_text.delta", "delta": "after saved item: 世界"},
				map[string]any{"type": "response.output_text.delta", "delta": "!"},
				map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("resp_%d", i), "status": "completed", "output": []any{}}},
			}
			for _, event := range events {
				b, err := json.Marshal(event)
				if err != nil {
					t.Error(err)
					return
				}
				if err := conn.Write(r.Context(), websocket.MessageText, b); err != nil {
					t.Error(err)
					return
				}
			}
		}
	}))
	defer srv.Close()
	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL
	defer func() { OpenAIAPIBaseURL = original }()
	client := NewWithAPIKey("test-key")
	defer client.responsesTransportState.Close()
	for _, want := range contents {
		body, err := client.openResponsesWebsocketStream(context.Background(), map[string]any{"model": "gpt-5.3-codex", "input": []any{}}, false, responsesWebsocketStreamOptions{})
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.parseAgenticStreamWithToolCallbacks(body, nil, nil, nil, nil)
		body.Close()
		if err != nil || result.text != "after saved item: 世界!" {
			t.Fatalf("stream result=%v err=%v", result, err)
		}
		state := client.responsesTransportState
		state.mu.Lock()
		var got string
		if len(state.lastBaseline) == 1 {
			got, _ = state.lastBaseline[0].(map[string]any)["encrypted_content"].(string)
		}
		state.mu.Unlock()
		if got != want {
			t.Fatalf("saved item corrupted after buffer reuse: bytes=%d want=%d", len(got), len(want))
		}
	}
	if connections.Load() != 1 {
		t.Fatalf("watchdog cleanup prevented connection reuse: connections=%d", connections.Load())
	}
}

// Scratch state must never leak a preceding event's type or delta into an event
// with missing/null fields, including SSE event-name fallback and scanner reuse.
func TestResponsesStreamScratchDoesNotRepeatPriorDelta(t *testing.T) {
	stream := "event: response.output_text.delta\r\n" +
		"data: {\"delta\":\"first 世界\"}\r\n" +
		"data: {}\r\n" +
		"data: {\"delta\":null}\r\n" +
		"data: {\"delta\":42}\r\n\r\n" +
		"data: {\"delta\":\"not a typed event\"}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\" last\\n\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"done\",\"status\":\"completed\"}}\n\n"
	var received []string
	result, err := (&Client{}).parseAgenticStreamWithToolCallbacks(strings.NewReader(stream), func(s string) { received = append(received, s) }, nil, nil, nil)
	if err != nil || result.text != "first 世界 last\n" || strings.Join(received, "") != result.text {
		t.Fatalf("result=%+v callbacks=%q err=%v", result, received, err)
	}
	var decoder responsesWebsocketDecoder
	for _, frame := range []string{`{"type":"response.completed"}`, `{}`, `null`, `{"type":null}`, `{"type":"response.output_text.delta"}`} {
		typ, _, valid := decoder.decode([]byte(frame))
		if !valid || ((frame == `{}` || frame == `null` || frame == `{"type":null}`) && typ != "") {
			t.Fatalf("frame=%s type=%q valid=%t", frame, typ, valid)
		}
	}
}

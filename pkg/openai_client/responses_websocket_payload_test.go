package openaiclient

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func websocketPayloadFixture(items int) map[string]any {
	input := make([]any, 0, items)
	for i := 0; i < items; i++ {
		if i%3 == 0 {
			input = append(input, agenticInputItem{"type": "message", "role": "user", "content": fmt.Sprintf("hello <%d> & more", i)})
			continue
		}
		input = append(input, json.RawMessage(fmt.Sprintf(`{"type":"function_call_output","call_id":"c%d","output": %q}`, i, strings.Repeat("line <code> & \"x\"\n", 200))))
	}
	return map[string]any{
		"type": "response.create", "model": "gpt-5", "input": input, "instructions": "sys",
		"tools": []ToolDefinition{{Type: "function", Name: "read_file", Description: "read"}},
	}
}

func TestMarshalResponsesWebsocketPayloadMatchesJSON(t *testing.T) {
	for _, payload := range []map[string]any{
		websocketPayloadFixture(30),
		{"type": "response.create"},
		{"input": []any{}},
		{"input": []any(nil)},
		{"input": []any{json.RawMessage(nil), json.RawMessage(" {\"a\" : 1} ")}, "model": "m"},
	} {
		want, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		got, err := marshalResponsesWebsocketPayload(payload)
		if err != nil {
			t.Fatal(err)
		}
		var a, b any
		if err := json.Unmarshal(want, &a); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(got, &b); err != nil {
			t.Fatalf("invalid JSON %s: %v", got, err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("payload differs:\n got %s\nwant %s", got, want)
		}
	}
	if _, err := marshalResponsesWebsocketPayload(map[string]any{"input": []any{json.RawMessage("{bad")}}); err == nil {
		t.Fatal("invalid raw item must fail like json.Marshal")
	}
}

func BenchmarkMarshalResponsesWebsocketPayload(b *testing.B) {
	payload := websocketPayloadFixture(300)
	b.Run("json.Marshal", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := json.Marshal(payload); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("spliced", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := marshalResponsesWebsocketPayload(payload); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestWebsocketRequestReusesEncodedProperties(t *testing.T) {
	payload := websocketPayloadFixture(30)
	payload["client_metadata"] = map[string]string{"session_id": "session"}
	state := &ResponsesTransportState{}
	for _, incremental := range []bool{false, true} {
		if incremental {
			state.lastResponseID = "previous"
			state.lastBaseline = payload["input"].([]any)[:10]
		}
		wire, _, properties := incrementalResponsesWebsocketPayload(payload, state)
		state.lastProperties = properties
		if incremental {
			wire, _, properties = incrementalResponsesWebsocketPayload(payload, state)
		}
		got, err := marshalResponsesWebsocketRequest(wire, properties)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		var a, b any
		if err = json.Unmarshal(got, &a); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(want, &b); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatal("request changed")
		}
	}
}

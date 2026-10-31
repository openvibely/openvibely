package requestjson

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/jsonview"
)

func TestNestedBorrowedFieldsMatchWireSemantics(t *testing.T) {
	borrowed, _ := jsonview.Borrow(json.RawMessage(`"large <>&\u2028 result"`))
	for _, content := range []any{borrowed, json.RawMessage(` [1,{"x":"<>&"}] `), json.RawMessage(nil), jsonview.Value{}, json.RawMessage(`invalid`), json.RawMessage([]byte{'"', 0xff, '"'})} {
		item := map[string]any{"type": "function_call_output", "output": content, "call_id": "id", "metadata": map[string]any{"n": 1}}
		input := map[string]any{"input": []any{item}, "stream": true}
		want, wantErr := json.Marshal(input)
		got, gotErr := MarshalArrayField(input, "input")
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("errors %v / %v", wantErr, gotErr)
		}
		if gotErr != nil {
			continue
		}
		var a, b any
		if err := json.Unmarshal(want, &a); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(got, &b); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s != %s", got, want)
		}
		after, _ := json.Marshal(input)
		if string(after) != string(want) {
			t.Fatal("input mutated")
		}
	}
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	if _, err := MarshalArrayField(map[string]any{"input": []any{map[string]any{"output": borrowed, "cycle": cyclic}}}, "input"); err == nil {
		t.Fatal("cycle accepted")
	}
}

func BenchmarkBorrowedRequestEncoding(b *testing.B) {
	text := strings.Repeat("result <>& text\n", 32768)
	raw, _ := json.Marshal(text)
	view, _ := jsonview.Borrow(raw)
	for _, borrowed := range []bool{false, true} {
		name := "decoded"
		var content any = text
		if borrowed {
			name = "borrowed"
			content = view
		}
		items := make([]any, 10)
		for i := range items {
			items[i] = map[string]any{"type": "function_call_output", "output": content, "call_id": "call"}
		}
		payload := map[string]any{"input": items, "stream": true}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var err error
				if borrowed {
					_, err = MarshalArrayField(payload, "input")
				} else {
					_, err = json.Marshal(payload)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

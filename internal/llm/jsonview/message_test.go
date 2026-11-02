package jsonview

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestMessageProjectionPreservesWireAndBorrowsText(t *testing.T) {
	cases := []string{
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hello\nworld","annotations":[{"x":1}]},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}`,
		`{"role":"assistant","content":[{"type":"output_text","text":"answer"},{"type":"unknown","text":"unchanged"}],"extra":true}`,
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"\u0061"},null,4]}`,
		`{"type":"message","role":"user","content":[]}`,
		`{"type":"message","role":"user","content":"application context"}`,
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			got, err := ToolOutput(json.RawMessage(raw))
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var expected, actual any
			if err = json.Unmarshal([]byte(raw), &expected); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(encoded, &actual); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(expected, actual) {
				t.Fatalf("wire changed: %s", encoded)
			}
		})
	}
	got, err := ToolOutput(json.RawMessage(cases[0]))
	if err != nil {
		t.Fatal(err)
	}
	text := got.(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
	if _, ok := text.(Value); !ok {
		t.Fatalf("text was eagerly decoded: %T", text)
	}
	if Text(text) != "hello\nworld" {
		t.Fatal("borrowed text not readable")
	}
}

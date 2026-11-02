package jsonview

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSavedResponseRawAndEditableViews(t *testing.T) {
	raw := json.RawMessage(`{"type":"message","role":"user","extra":{"unknown":true},"content":[{"type":"input_text","text":"` + strings.Repeat("x", 70000) + `"}]}`)
	item, err := SavedResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := item.(*ResponseItem)
	if !ok {
		t.Fatalf("not raw: %T", item)
	}
	if &saved.RawJSON()[0] != &raw[0] {
		t.Fatal("checkpoint copied")
	}
	if _, ok := saved.Header()["content"]; ok {
		t.Fatal("content materialized in metadata")
	}
	first, err := saved.Object()
	if err != nil {
		t.Fatal(err)
	}
	first["content"].([]any)[0].(map[string]any)["text"] = "edit"
	second, err := saved.Object()
	if err != nil {
		t.Fatal(err)
	}
	if Text(second["content"].([]any)[0].(map[string]any)["text"]) != ""+strings.Repeat("x", 70000) {
		t.Fatal("editing changed raw checkpoint")
	}
}

func TestSavedResponseFallbacks(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":"` + strings.Repeat("x", 70000) + `"}`),
		json.RawMessage(`{"type":"message","content":[]}`),
		json.RawMessage(`{"type":"unknown","content":["` + strings.Repeat("x", 70000) + `"]}`),
		json.RawMessage(`{"type":"message","content":["` + strings.Repeat("x", 70000) + string([]byte{0xff}) + `"]}`),
	} {
		value, err := SavedResponse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := value.(*ResponseItem); ok {
			t.Fatal("unsafe/unhelpful raw fast path")
		}
		expected, err := ToolOutput(raw)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(value)
		b, _ := json.Marshal(expected)
		if string(a) != string(b) {
			t.Fatal("fallback changed content")
		}
	}
	if _, err := SavedResponse(json.RawMessage(`{"type":"message","content":["` + strings.Repeat("x", 70000))); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

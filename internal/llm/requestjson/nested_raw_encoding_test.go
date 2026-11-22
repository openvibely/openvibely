package requestjson

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/jsonview"
)

func TestDirectEncodingNestedBorrowedValues(t *testing.T) {
	raw := json.RawMessage(`"` + strings.Repeat("value", 10000) + `<&>"`)
	view, err := jsonview.Borrow(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{raw, view} {
		item := map[string]any{"parts": []any{map[string]any{"response": map[string]any{"output": value}}}}
		if _, ok := plainSizeHint(item, 0); !ok {
			t.Fatal("nested raw value selected full-message buffer fallback")
		}
		field, err := PrepareItems("contents", []any{item})
		if err != nil {
			t.Fatal(err)
		}
		got, err := field.Marshal(map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(map[string]any{"contents": []any{item}})
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatal("direct encoding changed escaping or nesting")
		}
	}
}

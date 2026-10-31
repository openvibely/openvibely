package requestjson

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestSizeMatchesEncoder(t *testing.T) {
	for _, v := range []any{nil, true, false, "\xff<&é\u2028", 1.1, 1e-7, 1e22, math.Inf(1), map[string]any{"tool": "<&", "arguments": strings.Repeat("\n", 1000), "nested": []any{1., nil, true}}, []any(nil), map[string]any(nil)} {
		raw, err := json.Marshal(v)
		n, e := Size(v)
		if (err == nil) != (e == nil) || err == nil && n != len(raw) {
			t.Fatalf("%#v: %d != %d, %v %v", v, n, len(raw), err, e)
		}
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if _, err := Size(cycle); err == nil {
		t.Fatal("cycle accepted")
	}
}
func TestSelectedSchemasDoNotCacheAuthorization(t *testing.T) {
	original := []any{map[string]any{"name": "read"}, map[string]any{"name": "write"}}
	field, err := PrepareItems("tools", original)
	if err != nil {
		t.Fatal(err)
	}
	for _, indices := range [][]int{{0, 1}, {0}, {1}, {}} {
		selected, err := field.Select(indices, []any{map[string]any{"type": "native"}})
		if err != nil {
			t.Fatal(err)
		}
		var got []any
		if err := json.Unmarshal(selected.JSON(), &got); err != nil {
			t.Fatal(err)
		}
		want := []any{}
		for _, i := range indices {
			want = append(want, original[i])
		}
		want = append(want, map[string]any{"type": "native"})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%v != %v", got, want)
		}
	}
	if _, err := field.Select([]int{-1}, nil); err == nil {
		t.Fatal("invalid index accepted")
	}
}
func TestRawHTTPArrayPreservesNilAndRejectsInvalid(t *testing.T) {
	for _, input := range [][]any{nil, {}, {json.RawMessage(`{"role":"user","content":"hello"}`)}} {
		payload := map[string]any{"input": input, "model": "test"}
		got, err := MarshalArrayField(payload, "input")
		if err != nil {
			t.Fatal(err)
		}
		expected, _ := json.Marshal(payload)
		var a, b any
		_ = json.Unmarshal(got, &a)
		_ = json.Unmarshal(expected, &b)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s != %s", got, expected)
		}
	}
	if _, err := MarshalArrayField(map[string]any{"input": []any{json.RawMessage(`{`)}}, "input"); err == nil {
		t.Fatal("invalid raw accepted")
	}
}

func BenchmarkSizeLargeToolArguments(b *testing.B) {
	value := map[string]any{"type": "function_call", "arguments": strings.Repeat("line<&\n", 1<<16)}
	b.Run("marshal", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := json.Marshal(value); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("count", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := Size(value); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestSelectionOffsetsSurviveMarshal(t *testing.T) {
	field, err := PrepareItems("tools", []any{map[string]any{"name": "same"}, map[string]any{"name": "same"}, map[string]any{"name": "third"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = field.Select([]int{0}, nil); err != nil {
		t.Fatal(err)
	}
	original := field.JSON()
	if _, err = field.Marshal(map[string]any{"model": "test"}); err != nil {
		t.Fatal(err)
	}
	if &original[0] == &field.JSON()[0] {
		t.Fatal("expected rebased field")
	}
	selected, err := field.Select([]int{2, 1, 1}, []any{map[string]any{"type": "native"}})
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]string
	if err = json.Unmarshal(selected.JSON(), &got); err != nil {
		t.Fatal(err)
	}
	want := []map[string]string{{"name": "third"}, {"name": "same"}, {"name": "same"}, {"type": "native"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v != %v", got, want)
	}
	if _, err = field.Select([]int{3}, nil); err == nil {
		t.Fatal("invalid cached index accepted")
	}
	all, err := field.Select([]int{0, 1, 2}, nil)
	if err != nil || all != field {
		t.Fatal("unchanged selection not reused")
	}
}

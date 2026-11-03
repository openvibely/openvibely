package requestjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

type directEncodingMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
	Omitted string `json:"omitted,omitempty"`
}

type directEncodingCustom struct{ Fail bool }

func (v directEncodingCustom) MarshalJSON() ([]byte, error) {
	if v.Fail {
		return nil, errors.New("custom encoding failure")
	}
	return []byte(`{"value":"<>&\u2028"}`), nil
}

func TestDirectItemsPreserveStandardEncoding(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, content := range []any{
		nil, "text <>&\n\u2028\u2029", string([]byte{'x', 0xff}),
		[]any{map[string]any{"image_url": map[string]any{"url": "data:image/png;base64," + strings.Repeat("A", 1<<20)}}},
		[]json.RawMessage{json.RawMessage(` { "text": "<>&" } `)},
		directEncodingCustom{}, directEncodingCustom{Fail: true}, cycle, math.Inf(1), func() {},
	} {
		items := []any{directEncodingMessage{Role: "user", Content: content}, map[string]any{"role": "user", "content": content}, map[string]any{"role": "assistant", "content": "next"}}
		want, wantErr := json.Marshal(map[string]any{"messages": items, "stream": true})
		got, gotErr := MarshalItems(map[string]any{"stream": true}, "messages", items)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%T: error mismatch: got %v, want %v", content, gotErr, wantErr)
		}
		if gotErr != nil {
			continue
		}
		var a, b any
		if err := json.Unmarshal(got, &a); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(want, &b); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%T: output mismatch", content)
		}
	}
}

func TestDirectItemsRetryDoesNotMutateInputOrPriorWire(t *testing.T) {
	raw := json.RawMessage(` { "text": "borrowed <>&" } `)
	original := string(raw)
	messages := []any{map[string]any{"role": "user", "content": raw}, directEncodingMessage{Role: "user", Content: strings.Repeat("image", 1<<18)}}
	field, err := PrepareItems("messages", messages)
	if err != nil {
		t.Fatal(err)
	}
	first, err := field.Marshal(map[string]any{"attempt": 1})
	if err != nil {
		t.Fatal(err)
	}
	saved := string(first)
	second, err := field.Marshal(map[string]any{"attempt": 2})
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != saved || string(raw) != original {
		t.Fatal("encoding retry mutated prior data")
	}
	if !json.Valid(second) || string(second) == saved {
		t.Fatal("retry metadata not applied")
	}
}

func BenchmarkDirectCheckpointEncoding(b *testing.B) {
	for _, count := range []int{1, 10} {
		b.Run(map[int]string{1: "one-large-message", 10: "ten-large-messages"}[count], func(b *testing.B) {
			items := make([]any, count)
			for i := range items {
				items[i] = map[string]any{"role": "user", "content": strings.Repeat("A", (11<<20)/count)}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				field, err := PrepareItems("messages", items)
				if err != nil {
					b.Fatal(err)
				}
				if _, err = field.Marshal(map[string]any{"stream": true}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestDirectPreparedFieldConcurrentRetries(t *testing.T) {
	items := []any{map[string]any{"role": "user", "content": strings.Repeat("image-data", 1<<16)}}
	field, err := PrepareItems("messages", items)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			wire, err := field.Marshal(map[string]any{"attempt": i})
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Attempt  int              `json:"attempt"`
				Messages []map[string]any `json:"messages"`
			}
			if err := json.Unmarshal(wire, &got); err != nil {
				t.Fatal(err)
			}
			if got.Attempt != i || len(got.Messages) != 1 || got.Messages[0]["content"] != items[0].(map[string]any)["content"] {
				t.Fatal("retry changed content or metadata")
			}
		})
	}
}

func TestDirectLargeMessageAvoidsGrowthForEscapedContext(t *testing.T) {
	items := []any{
		map[string]any{"role": "user", "content": strings.Repeat("A", 1<<20)},
		map[string]any{"role": "user", "content": strings.Repeat("line\n\"quoted\" <>&\u2028", 200)},
	}
	field, err := PrepareItems("messages", items)
	if err != nil {
		t.Fatal(err)
	}
	data := field.JSON()
	// Small context appended to a large message must not cause the entire
	// destination to grow merely to accommodate its JSON escapes.
	if cap(data)-len(data) > 32<<10 {
		t.Fatalf("large message buffer grew for small escaped context: len=%d cap=%d", len(data), cap(data))
	}
}

package tokenestimate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestContentBlocksProviderRepresentationParity(t *testing.T) {
	for _, provider := range []struct {
		name, kind string
		tokens     int
	}{
		{"responses", "input_image", 1844},
		{"completions", "image_url", 1844},
		{"compatible", "image_url", 2000},
		{"anthropic", "image", 2000},
	} {
		t.Run(provider.name, func(t *testing.T) {
			// Multiple images and intervening text exercise both accounting and traversal.
			blocks := []map[string]any{
				{"type": provider.kind, "source": map[string]any{"data": strings.Repeat("A", 100000)}},
				{"type": "text", "text": strings.Repeat("ordinary text", 1000)},
				{"type": provider.kind, "source": map[string]any{"data": strings.Repeat("B", 200000)}},
			}
			encoded, err := json.Marshal(blocks)
			if err != nil {
				t.Fatal(err)
			}
			var reloaded []any
			if err := json.Unmarshal(encoded, &reloaded); err != nil {
				t.Fatal(err)
			}
			first, _ := json.Marshal(blocks[0])
			last, _ := json.Marshal(blocks[2])
			want := 2*ByteBudget(provider.tokens) - len(first) - len(last)
			for name, content := range map[string]any{
				"typed": blocks, "reloaded": reloaded, "raw": json.RawMessage(encoded),
				"nested typed": []map[string]any{{"type": "tool_result", "content": blocks}},
				"nested raw":   []any{map[string]any{"type": "tool_result", "content": json.RawMessage(encoded)}},
			} {
				t.Run(name, func(t *testing.T) {
					before, _ := json.Marshal(content)
					got, err := ContentAdjustment(content, provider.kind, provider.tokens)
					if err != nil || got != want {
						t.Fatalf("adjustment=%d want=%d err=%v", got, want, err)
					}
					after, _ := json.Marshal(content)
					if string(before) != string(after) {
						t.Fatal("visitor modified request content")
					}
				})
			}
		})
	}
}

func TestContentBlocksDoNotParseTextOrToolArguments(t *testing.T) {
	for _, kind := range []string{"input_image", "image_url", "image"} {
		image := map[string]any{"type": kind, "data": strings.Repeat("A", 100000)}
		raw, _ := json.Marshal(image)
		for _, content := range []any{
			string(raw), image, json.RawMessage(raw),
			[]any{map[string]any{"type": "tool_result", "content": image}},
			[]map[string]any{{"type": "text", "text": string(raw)}},
			[]map[string]any{{"type": "tool_use", "input": image}},
			[]map[string]any{{"type": "document", "source": image}},
		} {
			got, err := ContentAdjustment(content, kind, 2000)
			if err != nil || got != 0 {
				t.Fatalf("non-image content discounted: %d, %v", got, err)
			}
		}
	}
}

func TestContentBlockRawSizePreservesWireFormatting(t *testing.T) {
	raw := json.RawMessage(` [ { "type": "image", "source": { "data": "A\u0041" } } ] `)
	block := `{ "type": "image", "source": { "data": "A\u0041" } }`
	if got := contentImageAdjustment(raw, "image", 2000); got != ByteBudget(2000)-len(block) {
		t.Fatalf("wire size changed: %d", got)
	}
	// Re-encoding RawMessage compacts whitespace but preserves escape sequences.
	encoded, _ := json.Marshal(json.RawMessage(block))
	if got, err := ContentAdjustment(raw, "image", 2000); err != nil || got != ByteBudget(2000)-len(encoded) {
		t.Fatalf("normalized size changed: %d, %v", got, err)
	}
}

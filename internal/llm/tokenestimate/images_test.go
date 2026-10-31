package tokenestimate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestImageBudgetUsesFixedCostWithoutChangingPayload(t *testing.T) {
	for _, imageType := range []string{"image", "image_url"} {
		for _, imageTokens := range []int{1844, 2000} {
			block := map[string]any{"type": imageType, "source": map[string]any{"data": strings.Repeat("A", 100000)}}
			encodedBlock, _ := json.Marshal(block)
			encoded, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"content": []any{block}}}})
			before := string(encoded)
			want := FromByteCount(len(encoded)-len(encodedBlock)) + imageTokens
			got, err := FromMessagesJSON(encoded, imageType, imageTokens)
			if err != nil || got != want || string(encoded) != before {
				t.Fatalf("type=%s tokens=%d want=%d err=%v", imageType, got, want, err)
			}
		}
	}
}

func TestImageBudgetPreservesTextToolsAndDocuments(t *testing.T) {
	for _, content := range []any{
		strings.Repeat("image data is text here ", 1000),
		[]any{map[string]any{"type": "text", "text": strings.Repeat("x", 10000)}},
		[]any{map[string]any{"type": "document", "source": map[string]any{"data": strings.Repeat("x", 10000)}}},
		[]any{map[string]any{"type": "tool_use", "input": map[string]any{"type": "image", "data": strings.Repeat("x", 10000)}}},
	} {
		encoded, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"content": content}}, "tools": []any{map[string]any{"type": "image", "description": strings.Repeat("x", 10000)}}})
		before := string(encoded)
		got, err := FromMessagesJSON(encoded, "image", 2000)
		if err != nil || got != FromByteCount(len(encoded)) || string(encoded) != before {
			t.Fatalf("non-image content changed: tokens=%d err=%v", got, err)
		}
	}
}

func TestPreparedArrayMatchesLegacyEstimator(t *testing.T) {
	cases := []string{
		`null`, `[]`, `[null]`,
		`[{"content":"text with image words"}]`,
		`[{"content":[{"type":"image","source":{"data":"AAAA"}}]}]`,
		`[{"CONTENT":[{"TYPE":"tool_result","Content":[{"type":"image_url","url":"AAAA"}]}]}]`,
		`[{"content":"old","content":[{"type":"image","type":null}]}]`,
		`[{"content":[1,null,"text",{"type":1},{"type":"image","content":"ignored"}]}]`,
		`[{"content":[{"type":"tool_use","input":{"type":"image"}}]}]`,
	}
	for _, raw := range cases {
		for _, imageType := range []string{"image", "image_url"} {
			wrapper := []byte(`{"messages":` + raw + `,"system":"hello"}`)
			want, err := FromMessagesJSON(wrapper, imageType, 2000)
			if err != nil {
				t.Fatal(err)
			}
			adjust, err := MessageArrayAdjustment([]byte(raw), imageType, 2000)
			if err != nil {
				t.Fatal(err)
			}
			if got := FromByteCount(len(wrapper) + adjust); got != want {
				t.Fatalf("%s %s: %d != %d", imageType, raw, got, want)
			}
		}
	}
}

package anthropicclient

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestAnthropicScreenshotBudget(t *testing.T) {
	blocks := []map[string]interface{}{}
	for _, size := range []int{794795, 713753} {
		blocks = append(blocks, map[string]interface{}{"type": "image", "source": map[string]interface{}{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(make([]byte, size))}})
	}
	opts := &AgenticOptions{ContextWindow: 200000, MaxTokens: 8192}
	for _, content := range []any{blocks, []map[string]interface{}{{"type": "tool_result", "tool_use_id": "call1", "content": blocks}}} {
		if err := ensureAnthropicAgenticRequestFits([]agenticMessage{{Role: "user", Content: content}}, nil, opts); err != nil {
			t.Fatal(err)
		}
	}
	opts.ContextWindow = 12000
	if err := ensureAnthropicAgenticRequestFits([]agenticMessage{{Role: "user", Content: blocks}}, nil, opts); err == nil {
		t.Fatal("images must still count against small context windows")
	}
}

func TestNativeCompactionImageBudget(t *testing.T) {
	for _, nested := range []bool{false, true} {
		for _, size := range []int{100, 1000000} {
			blocks := []any{map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": strings.Repeat("A", size)}}}
			if nested {
				blocks = []any{map[string]any{"type": "tool_result", "tool_use_id": "image_call", "content": blocks}}
			}
			summary := "summary"
			state := nativeCompactionState{Version: 1, Messages: []agenticMessage{{Role: "assistant", Content: []compactionBlockJSON{{Type: "compaction", Content: &summary}}}, {Role: "user", Content: blocks}}}
			raw, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			got := EstimateNativeCompactionTokens(string(raw))
			if got < 2000 || got > 2200 {
				t.Fatalf("nested=%t size=%d: image estimated at %d tokens", nested, size, got)
			}
		}
	}
	// The estimator must not discount ordinary text in a compaction block.
	raw, _ := json.Marshal(map[string]any{"type": "compaction", "content": strings.Repeat("x", 100000)})
	if got := EstimateNativeCompactionTokens(string(raw)); got < 25000 {
		t.Fatalf("large summary undercounted: %d", got)
	}
	if got := EstimateNativeCompactionTokens(""); got != 0 {
		t.Fatalf("empty checkpoint: %d", got)
	}
}

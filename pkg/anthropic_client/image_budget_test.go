package anthropicclient

import (
	"encoding/base64"
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

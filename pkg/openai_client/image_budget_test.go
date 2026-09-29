package openaiclient

import (
	"encoding/base64"
	"testing"
)

func TestCompletionsScreenshotBudget(t *testing.T) {
	blocks := []map[string]interface{}{}
	for _, size := range []int{794795, 713753} {
		blocks = append(blocks, map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, size))}})
	}
	for _, firstParty := range []bool{false, true} {
		opts := &CompletionsOptions{ContextWindow: 128000, MaxOutputTokens: 16384, FirstPartyOpenAI: firstParty}
		if err := ensureCompletionsRequestFits([]completionsMessage{{Role: "user", Content: blocks}}, nil, opts); err != nil {
			t.Fatal(err)
		}
		opts.ContextWindow = 20000
		if err := ensureCompletionsRequestFits([]completionsMessage{{Role: "user", Content: blocks}}, nil, opts); err == nil {
			t.Fatal("images must still count against small context windows")
		}
	}
}

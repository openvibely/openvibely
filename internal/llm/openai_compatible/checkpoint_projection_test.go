package openai_compatible

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/preparedhistory"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestCheckpointProjectionReusesLargeImage(t *testing.T) {
	raw := `[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + strings.Repeat("a", 2<<20) + `"}}]}]`
	history := []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: raw}}}}
	scope := preparedhistory.NewReusableState()
	scope.BindHistory(history)
	first := buildPreparedClientHistoryContext(scope.Context(context.Background()), history)
	require.Len(t, first, 1)
	exported := first[0].DecodedMessages()
	exported[0].ImageContent[0]["image_url"].(map[string]interface{})["url"] = "changed"
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 3 {
		next := buildPreparedClientHistoryContext(scope.Context(context.Background()), history)
		got := next[0].DecodedMessages()[0].ImageContent[0]["image_url"].(map[string]interface{})["url"].(string)
		if len(got) != 2<<20 {
			t.Fatal("checkpoint mutated")
		}
	}
	runtime.ReadMemStats(&after)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(256<<10), "repeated preparation must not decode the large image again")
}

package jsonview

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageArraySizeMatchesEncoding(t *testing.T) {
	for _, blocks := range [][]map[string]any{nil, {}, {nil}, {{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,abc<&\u2028"}}, {"type": "text", "text": "hello\n世界"}}} {
		encoded, err := json.Marshal(blocks)
		require.NoError(t, err)
		size, err := Size(blocks)
		require.NoError(t, err)
		require.Equal(t, len(encoded), size)
	}
	cycle := map[string]any{}
	cycle["content"] = []map[string]any{cycle}
	_, err := Size([]map[string]any{cycle})
	require.Error(t, err)
}

func TestImageArraySizeDoesNotEncodePayload(t *testing.T) {
	blocks := []map[string]any{{"type": "image_url", "image_url": map[string]any{"url": strings.Repeat("a", 2<<20)}}}
	_, err := Size(blocks)
	require.NoError(t, err)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 4 {
		_, err = Size(blocks)
		if err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(256<<10))
}

package openaiclient

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestHistoryImageContentUsesAPIShapeAndReadsLegacy(t *testing.T) {
	for _, raw := range []string{
		`{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}]}`,
		`{"role":"user","content":"","image_content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}]}`,
	} {
		var message CompletionsHistoryMessage
		require.NoError(t, json.Unmarshal([]byte(raw), &message))
		require.Len(t, message.ImageContent, 1)
		encoded, err := json.Marshal(message)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "image_content")
		var wire map[string]any
		require.NoError(t, json.Unmarshal(encoded, &wire))
		require.IsType(t, []any{}, wire["content"])
		require.Contains(t, string(encoded), "data:image/png;base64,abc")
	}
}

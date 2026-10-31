package anthropicclient

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/streamdecode"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestRecoveryTextMatchesDeliveredText(t *testing.T) {
	for _, parts := range [][]string{{"hello ", "world"}, {"before ", "<tool_call>", "hidden", "</tool_call>", " after"}, {"before ", "<tool_", "call>", "hidden", "</tool_call>", " after"}} {
		for _, complete := range []bool{false, true} {
			events := []string{`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`}
			for _, part := range parts {
				raw, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": part}})
				events = append(events, string(raw))
			}
			if complete {
				events = append(events, `{"type":"content_block_stop","index":0}`, `{"type":"message_stop"}`)
			}
			var capture streamdecode.Text
			var delivered strings.Builder
			client := new(Client)
			_, err := client.parseAgenticStreamCapturing(strings.NewReader(buildSSE(events)), func(s string) { delivered.WriteString(s) }, nil, nil, nil, &capture)
			if complete {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, delivered.String(), capture.String())
			require.NotContains(t, capture.String(), "hidden")
		}
	}
}

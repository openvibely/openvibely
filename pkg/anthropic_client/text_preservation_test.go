package anthropicclient

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTextBlocksPreserveVerbatimContent(t *testing.T) {
	for _, parts := range [][]string{
		{}, {""}, {" \n\t "},
		{"<tool_call>hidden</tool_call>"},
		{" Before\n", "<tool_", "call>", "instructions", "</tool_call>", "\nAfter "},
	} {
		expected := strings.Join(parts, "")
		t.Run(expected, func(t *testing.T) {
			events := []string{`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`}
			for _, part := range parts {
				raw, err := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": part}})
				require.NoError(t, err)
				events = append(events, string(raw))
			}
			events = append(events, `{"type":"content_block_stop","index":0}`, `{"type":"message_stop"}`)
			for _, callback := range []bool{false, true} {
				var delivered strings.Builder
				var onText func(string)
				if callback {
					onText = func(s string) { delivered.WriteString(s) }
				}
				result, err := (&Client{}).parseAgenticStream(strings.NewReader(buildSSE(events)), onText, nil)
				require.NoError(t, err)
				require.Equal(t, []agenticBlock{{Type: "text", Text: expected}}, result.contentBlocks)
				if callback {
					require.Equal(t, expected, delivered.String())
				}
				// The same block encoding is used for durable replay and live requests.
				raw, err := json.Marshal(result.contentBlocks)
				require.NoError(t, err)
				var blocks []map[string]any
				require.NoError(t, json.Unmarshal(raw, &blocks))
				require.Contains(t, blocks[0], "text")
				require.Equal(t, expected, blocks[0]["text"])
				replay := Message{Role: "assistant", ContentBlocks: raw}
				messages := appendHistoryMessage(nil, replay)
				prepared, err := marshalAgenticMessage(messages[0])
				require.NoError(t, err)
				var restored struct {
					Content []map[string]any `json:"content"`
				}
				require.NoError(t, json.Unmarshal(prepared, &restored))
				require.Equal(t, blocks, restored.Content)
			}
		})
	}
}

func TestAgenticBlockTextFieldOnlyOnText(t *testing.T) {
	for _, block := range []agenticBlock{
		{Type: "text"},
		{Type: "tool_use", ID: "call", Name: "shell"},
		{Type: "thinking", Thinking: "reason", Signature: "signature"},
	} {
		raw, err := json.Marshal(block)
		require.NoError(t, err)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(raw, &fields))
		if block.Type == "text" {
			require.Equal(t, "", fields["text"])
			require.Contains(t, fields, "text")
		} else {
			require.NotContains(t, fields, "text")
		}
		if block.Type == "tool_use" {
			require.Equal(t, map[string]any{}, fields["input"])
		}
	}
}

package anthropicclient

import (
	"encoding/json"
	"io"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"

	"github.com/openvibely/openvibely/internal/models"
)

// Adapters for tests of the production entry points.

func decodeNativeCompactionState(raw string) ([]agenticMessage, error) {
	return decodeNativeCompactionReplay(models.ExecutionReplayMessage{TranscriptJSON: raw})
}

// parseAgenticStream parses a streaming response, handling text, tool_use, and compaction blocks.
func (c *Client) parseAgenticStream(body io.Reader, onText func(string), onThinking func(string)) (*turnResult, error) {
	return c.parseAgenticStreamWithCallbacks(body, onText, onThinking, nil, nil)
}

// parseAgenticStreamWithCallbacks parses a streaming response, handling text,
// tool use, and compaction blocks while optionally emitting tool callbacks.
func (c *Client) parseAgenticStreamWithCallbacks(
	body io.Reader,
	onText func(string),
	onThinking func(string),
	onToolUse func(string, json.RawMessage),
	onToolResult func(string, string, bool),
	compactionCallbacks ...func(llmcontracts.CompactionProgress),
) (*turnResult, error) {
	return c.parseAgenticStreamCapturing(body, onText, onThinking, onToolUse, onToolResult, nil, compactionCallbacks...)
}

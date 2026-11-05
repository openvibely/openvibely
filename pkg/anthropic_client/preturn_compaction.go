package anthropicclient

import (
	"context"
	"fmt"
	"strings"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
)

// compactHistoryBeforeTurn uses the existing threshold-compaction protocol in
// pause mode. It never includes the pending user message or executes tools. The
// API's minimum trigger still applies; a response without a usable compaction
// block is a native failure, allowing the service to choose textual compaction.
func (c *Client) compactHistoryBeforeTurn(ctx context.Context, history []agenticMessage, tools []ToolDefinition, opts *AgenticOptions) ([]agenticMessage, *turnResult, error) {
	compactOpts := *opts
	compactOpts.compactOnly = true
	compactOpts.CompactionTokenThreshold = MinCompactionThreshold
	compactOpts.OnText = nil
	compactOpts.OnThinking = nil
	compactOpts.OnToolUse = nil
	compactOpts.OnToolResult = nil
	compactOpts.responseText = nil
	// A final user message avoids assistant prefill on models that reject it.
	// It is only a compaction instruction, never the pending user request.
	messages := append([]agenticMessage(nil), history...)
	messages = append(messages, agenticMessage{Role: "user", Content: "Compact the preceding conversation for continuation. Do not perform any task or call tools."})
	messages = repairInterruptedToolCalls(messages)
	response, err := c.sendAgenticTurn(ctx, messages, tools, &compactOpts)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if llmcontracts.ErrorIs(err, llmcontracts.ErrorNativeCompactionUnsupported) {
			return nil, nil, err
		}
		return nil, nil, llmcontracts.NewCategorizedError(llmcontracts.ErrorNativeCompactionFailed, "Anthropic pre-turn compaction", err)
	}
	if response.compaction == nil || response.compaction.content == nil || strings.TrimSpace(*response.compaction.content) == "" {
		return nil, response, llmcontracts.NewCategorizedError(llmcontracts.ErrorNativeCompactionFailed, "Anthropic pre-turn compaction", fmt.Errorf("provider returned no usable compaction block"))
	}
	block := compactionBlockJSON{Type: "compaction", Content: response.compaction.content, EncryptedContent: response.compaction.encryptedContent}
	return []agenticMessage{{Role: "assistant", Content: []any{block}}}, response, nil
}

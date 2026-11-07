package openaiclient

import (
	"io"
	"strings"
)

// Adapters for tests of the production entry points.

func trimCompactionInputItemsToFitContextWindow(inputItems []any, tools []ToolDefinition, instructions, model string, configuredContextWindow ...int) ([]any, error) {
	return trimCompactionInputWithOverhead(inputItems, compactionRequestFixedTokenOverhead(tools, instructions), model, configuredContextWindow...)
}

func (c *Client) parseCompletionsStream(body io.Reader, onText func(string)) (*completionsTurnResult, error) {
	return c.parseCompletionsStreamCapturing(body, onText, nil)
}

func (c *Client) parseCompletionsStreamCapturing(body io.Reader, onText func(string), textBuilder *strings.Builder) (*completionsTurnResult, error) {
	return c.parseCompletionsStreamWithMode(body, onText, textBuilder, false)
}

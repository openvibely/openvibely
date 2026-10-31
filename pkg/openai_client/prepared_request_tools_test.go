package openaiclient

import (
	"github.com/openvibely/openvibely/internal/llm/requestjson"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPreparedResponsesToolsPreservePreflight(t *testing.T) {
	tools := []ToolDefinition{{Type: "function", Name: "read", Description: "é <tool>"}}
	for _, window := range []int{100, 100000} {
		opts := &AgenticOptions{Model: "unknown", System: "instructions", ContextWindow: window, MaxOutputTokens: 20}
		input := []any{map[string]any{"type": "message", "role": "user", "content": "hello"}}
		original := ensureOpenAIAgenticRequestFits(input, tools, opts)
		var err error
		opts.requestTools, err = requestjson.Prepare("tools", tools)
		require.NoError(t, err)
		prepared := ensureOpenAIAgenticRequestFits(input, tools, opts)
		require.Equal(t, original == nil, prepared == nil)
		if original != nil {
			require.Equal(t, original.Error(), prepared.Error())
		}
	}
}

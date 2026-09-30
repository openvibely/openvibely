package service

import (
	"context"
	"errors"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	"github.com/stretchr/testify/require"
)

func TestAnthropicSteeringFallbackRetainsCurrentConversation(t *testing.T) {
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, ExecID: "active", Message: "original task",
		Agent:       models.LLMConfig{Provider: models.ProviderAnthropic, Model: "claude-opus-4-6", ContextWindow: 200000},
		ChatHistory: []models.Execution{{PromptSent: "stale history", Status: models.ExecCompleted}}}
	calls := 0
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		calls++
		switch calls {
		case 1:
			return llmcontracts.AgentResult{}, &anthropicclient.ConversationError{
				Err:          llmcontracts.NewCategorizedError(llmcontracts.ErrorContextWindowExceeded, "Anthropic", errors.New("context window exceeded")),
				MessagesJSON: `[{"role":"user","content":"original task"},{"role":"assistant","content":[{"type":"tool_use","id":"write1","name":"write_file","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"write1","content":"file already written"},{"type":"text","text":"actually only review"}]}]`,
			}
		case 2:
			require.Equal(t, llmcontracts.OperationDirect, req.Operation)
			require.False(t, llmcontracts.HistoryContinuationFromContext(req.Ctx))
			require.Contains(t, req.Message, "file already written")
			require.Contains(t, req.Message, "actually only review")
			require.NotContains(t, req.Message, "stale history")
			return llmcontracts.AgentResult{Output: "file already written; do not repeat"}, nil
		case 3:
			require.Empty(t, req.Message)
			require.True(t, llmcontracts.HistoryContinuationFromContext(req.Ctx))
			history := buildLocalSummaryCompactionPrompt(req.ChatHistory)
			require.Contains(t, history, "actually only review", "steer must survive even when summary omits it")
			require.Contains(t, history, "file already written")
		default:
			t.Fatal("unexpected retry")
		}
		return llmcontracts.AgentResult{Output: "done"}, nil
	})
	_, err := (&LLMService{}).callProviderWithCompaction(adapter, req)
	require.NoError(t, err)
	require.Equal(t, 3, calls)
}

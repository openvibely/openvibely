package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	"github.com/stretchr/testify/require"
)

func TestAnthropicSteeringRecoveryStopsWithoutTruncatingToolResults(t *testing.T) {
	for _, summarySucceeds := range []bool{false, true} {
		t.Run(map[bool]string{false: "summary fails", true: "compacted retry overflows"}[summarySucceeds], func(t *testing.T) {
			req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Message: "original task",
				Agent:       models.LLMConfig{Provider: models.ProviderAnthropic, Model: "claude-opus-4-6", ContextWindow: 20000},
				ChatHistory: []models.Execution{{PromptSent: "old", Output: "old answer", Status: models.ExecCompleted}}}
			raw, err := json.Marshal([]any{
				map[string]any{"role": "user", "content": "original task"},
				map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "write1", "name": "write_file", "input": map[string]any{}}}},
				map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "write1", "content": "FILE_ALREADY_WRITTEN " + strings.Repeat("large result ", 50000)},
					map[string]any{"type": "text", "text": "actually only review"}}},
			})
			require.NoError(t, err)
			failure := errors.New("summarizer unavailable")
			overflow := llmcontracts.NewCategorizedError(llmcontracts.ErrorContextWindowExceeded, "test", errors.New("too many tokens"))
			calls := 0
			adapter := providerAdapterFunc(func(r llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				calls++
				switch calls {
				case 1:
					return llmcontracts.AgentResult{}, &anthropicclient.ConversationError{Err: overflow, MessagesJSON: string(raw)}
				case 2:
					require.Equal(t, llmcontracts.OperationDirect, r.Operation)
					if !summarySucceeds {
						return llmcontracts.AgentResult{}, failure
					}
					return llmcontracts.AgentResult{Output: "File already written. Do not repeat."}, nil
				case 3:
					require.True(t, summarySucceeds, "must not retry generation after failed summarization")
					return llmcontracts.AgentResult{}, overflow
				default:
					t.Fatal("unexpected last-resort generation request")
				}
				return llmcontracts.AgentResult{}, nil
			})
			_, err = (&LLMService{}).callProviderWithCompaction(adapter, req)
			if summarySucceeds {
				require.ErrorIs(t, err, overflow)
				require.Equal(t, 3, calls)
			} else {
				require.ErrorIs(t, err, failure)
				require.Equal(t, 2, calls)
			}
		})
	}
}

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

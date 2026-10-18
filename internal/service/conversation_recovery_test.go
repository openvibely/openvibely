package service

import (
	"context"
	"errors"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOrdinaryTurnRecoveryRetainsCompletedTools(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderOpenAICompatible, models.ProviderOllama} {
		t.Run(string(provider), func(t *testing.T) {
			req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, ExecID: "active", Message: "edit file", Agent: models.LLMConfig{Provider: provider, Model: "gpt-6-sol", ContextWindow: 272000}, ChatHistory: []models.Execution{{PromptSent: "earlier request", Status: models.ExecCompleted}}}
			calls := 0
			adapter := providerAdapterFunc(func(r llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				calls++
				switch calls {
				case 1:
					overflow := llmcontracts.NewCategorizedError(llmcontracts.ErrorContextWindowExceeded, "test", errors.New("context length exceeded"))
					if provider == models.ProviderOpenAI {
						llmcontracts.RecordResponsesRecovery(r.Ctx, `{"responses_input":[{"type":"message","role":"user","content":"edit file"},{"type":"function_call","name":"edit_file","call_id":"done","arguments":"{}"},{"type":"function_call_output","call_id":"done","output":"ALREADY_EDITED"}]}`)
						return llmcontracts.AgentResult{}, overflow
					}
					return llmcontracts.AgentResult{}, &openaiclient.ConversationError{Err: overflow, MessagesJSON: `[{"role":"user","content":"edit file"},{"role":"assistant","content":"","tool_calls":[{"id":"done","type":"function","function":{"name":"edit_file","arguments":"{}"}}]},{"role":"tool","tool_call_id":"done","content":"ALREADY_EDITED"}]`}
				case 2:
					require.Equal(t, llmcontracts.OperationDirect, r.Operation)
					require.Contains(t, r.Message, "ALREADY_EDITED")
					require.Contains(t, r.Message, "edit_file")
					// Summarization must not replace the active turn's recovery snapshot.
					llmcontracts.RecordResponsesRecovery(r.Ctx, `{"responses_input":[{"type":"message","role":"user","content":"BAD_SUMMARY_STATE"}]}`)
					return llmcontracts.AgentResult{Output: "ALREADY_EDITED; do not repeat the edit"}, nil
				case 3:
					require.Empty(t, r.Message)
					require.True(t, llmcontracts.HistoryContinuationFromContext(r.Ctx))
					require.Contains(t, buildLocalSummaryCompactionPrompt(r.ChatHistory), "ALREADY_EDITED")
					require.NotContains(t, buildLocalSummaryCompactionPrompt(r.ChatHistory), "BAD_SUMMARY_STATE")
					return llmcontracts.AgentResult{Output: "done"}, nil
				default:
					t.Fatal("unexpected retry")
				}
				return llmcontracts.AgentResult{}, nil
			})
			_, err := (&LLMService{}).callProviderWithCompaction(adapter, req)
			require.NoError(t, err)
			require.Equal(t, 3, calls)
		})
	}
}

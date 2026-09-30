package service

import (
	"context"
	"errors"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestSteeringCompactionFallbackUsesCurrentConversation(t *testing.T) {
	for _, secondOverflow := range []bool{false, true} {
		t.Run(map[bool]string{false: "summary retry", true: "last resort retry"}[secondOverflow], func(t *testing.T) {
			ctx := llmcontracts.WithInitialSteeringCommit(context.Background(), func(context.Context, []any) error { return nil })
			ctx = llmcontracts.WithLocalSteeringCallback(ctx, func(context.Context) (llmcontracts.LocalSteeringInput, error) {
				return llmcontracts.LocalSteeringInput{Text: "new steer", Commit: func(context.Context, []any) error { return nil }}, nil
			})
			req := llmcontracts.AgentRequest{Ctx: ctx, Operation: llmcontracts.OperationStreaming, ExecID: "active", Message: "late steer", Agent: models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex", ContextWindow: 272000}, ChatHistory: []models.Execution{{PromptSent: "old history", Status: models.ExecCompleted}}}
			calls := 0
			adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				calls++
				switch calls {
				case 1:
					commit := llmcontracts.InitialSteeringCommitFromContext(req.Ctx)
					require.NoError(t, commit(req.Ctx, []any{map[string]any{"type": "message", "role": "user", "content": "late steer"}}))
					local, err := llmcontracts.LocalSteeringCallbackFromContext(req.Ctx)(req.Ctx)
					require.NoError(t, err)
					require.NoError(t, local.Commit(req.Ctx, []any{
						map[string]any{"type": "message", "role": "user", "content": "late steer"},
						map[string]any{"type": "function_call", "call_id": "done", "name": "write", "arguments": "{}"},
						map[string]any{"type": "function_call_output", "call_id": "done", "output": "file already written"},
						map[string]any{"type": "message", "role": "user", "content": "new steer"},
					}))
					return llmcontracts.AgentResult{}, llmcontracts.NewCategorizedError(llmcontracts.ErrorNativeCompactionFailed, "test", errors.New("native compaction failed"))
				case 2:
					require.Equal(t, llmcontracts.OperationDirect, req.Operation)
					require.Nil(t, llmcontracts.InitialSteeringCommitFromContext(req.Ctx), "summarization must not consume live steering")
					require.Contains(t, req.Message, "file already written")
					require.Contains(t, req.Message, "new steer")
					return llmcontracts.AgentResult{Output: "file already written; follow new steer"}, nil
				case 3:
					require.Empty(t, req.Message, "do not replay the consumed prompt")
					require.True(t, llmcontracts.HistoryContinuationFromContext(req.Ctx))
					require.Contains(t, buildLocalSummaryCompactionPrompt(req.ChatHistory), "new steer")
					if secondOverflow {
						commit := llmcontracts.InitialSteeringCommitFromContext(req.Ctx)
						require.NoError(t, commit(req.Ctx, []any{map[string]any{"type": "message", "role": "assistant", "content": "retry progress"}}))
						return llmcontracts.AgentResult{}, llmcontracts.NewCategorizedError(llmcontracts.ErrorContextWindowExceeded, "retry", errors.New("context length exceeded"))
					}
				case 4:
					require.Contains(t, buildLocalSummaryCompactionPrompt(req.ChatHistory), "retry progress")
					require.Empty(t, req.Message)
				default:
					t.Fatal("unexpected recovery request")
				}
				return llmcontracts.AgentResult{Output: "done"}, nil
			})
			svc := &LLMService{}
			_, err := svc.callProviderWithCompaction(adapter, req)
			require.NoError(t, err)
			require.Equal(t, map[bool]int{false: 3, true: 4}[secondOverflow], calls)
		})
	}
}

func TestSteeringRecoveryIgnoresFailedCommit(t *testing.T) {
	ctx := llmcontracts.WithInitialSteeringCommit(context.Background(), func(context.Context, []any) error { return errors.New("save failed") })
	req := trackSteeringRecovery(llmcontracts.AgentRequest{Ctx: ctx, Message: "not consumed", Agent: models.LLMConfig{Provider: models.ProviderOpenAI}})
	err := llmcontracts.InitialSteeringCommitFromContext(req.Ctx)(req.Ctx, []any{map[string]any{"type": "message", "content": "not consumed"}})
	require.Error(t, err)
	restored := restoreCurrentSteeringForRecovery(req)
	require.Equal(t, "not consumed", restored.Message)
	require.False(t, llmcontracts.HistoryContinuationFromContext(restored.Ctx))
}

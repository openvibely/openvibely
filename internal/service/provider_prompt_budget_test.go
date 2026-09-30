package service

import (
	"context"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
)

func TestProviderPromptBudgetUsesProviderBase(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic} {
		for _, followup := range []bool{false, true} {
			req := llmcontracts.AgentRequest{Ctx: context.Background(), Agent: models.LLMConfig{Provider: provider, Model: "gpt-6-astra"}, Operation: llmcontracts.OperationTask, ProjectInstructions: "PROJECT", ChatSystemContext: "CHAT", WorkDir: "/tmp/.worktrees/task_123", Followup: followup}
			want := llmprompt.BuildCodexAgentSystemPrompt(req.Agent.Model, req.ProjectInstructions, req.WorkDir)
			if provider == models.ProviderAnthropic {
				want = llmprompt.BuildAnthropicAgentSystemPrompt(req.ProjectInstructions, req.WorkDir)
			}
			if followup {
				req.Operation = llmcontracts.OperationStreaming
				want = llmprompt.BuildCodexChatSystemPrompt(req.Agent.Model, true, req.ChatMode, req.ChatSystemContext, false)
				if provider == models.ProviderAnthropic {
					want = llmprompt.BuildAnthropicChatSystemPrompt(true, req.ChatMode, req.ChatSystemContext, false)
				}
				want = llmprompt.AppendWorktreeContextPrompt(want, req.WorkDir)
			}
			if got := estimateProviderSystemPromptTokens(req); got != estimatedUTF8Tokens(want) {
				t.Fatalf("%s followup=%v: got %d want %d", provider, followup, got, estimatedUTF8Tokens(want))
			}
		}
	}
}

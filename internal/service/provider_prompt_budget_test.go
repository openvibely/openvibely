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
				want = llmprompt.BuildAnthropicAgentSystemPrompt(req.Agent.Model, req.ProjectInstructions, req.WorkDir)
			}
			if followup {
				req.Operation = llmcontracts.OperationStreaming
				want = llmprompt.BuildCodexChatSystemPrompt(req.Agent.Model, true, req.ChatMode, req.ChatSystemContext, false)
				if provider == models.ProviderAnthropic {
					want = llmprompt.BuildAnthropicChatSystemPrompt(req.Agent.Model, true, req.ChatMode, req.ChatSystemContext, false)
				}
				want = llmprompt.AppendWorktreeContextPrompt(want, req.WorkDir)
			}
			if got := estimateProviderSystemPromptTokens(req); got != estimatedUTF8Tokens(want) {
				t.Fatalf("%s followup=%v: got %d want %d", provider, followup, got, estimatedUTF8Tokens(want))
			}
		}
	}
}

func TestProviderPromptBudgetIncludesRuntimeActionMode(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic} {
		for _, auth := range []models.AuthMethod{models.AuthMethodAPIKey, models.AuthMethodOAuth} {
			for _, followup := range []bool{false, true} {
				for _, mode := range []models.ChatMode{models.ChatModeOrchestrate, models.ChatModePlan} {
					for _, rt := range []*llmcontracts.RuntimeTools{nil, {Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "create_task"}, {Name: "list_tasks"}}}} {
						req := llmcontracts.AgentRequest{
							Ctx:       llmcontracts.WithRuntimeTools(context.Background(), rt),
							Agent:     models.LLMConfig{Provider: provider, AuthMethod: auth, Model: "gpt-6-astra"},
							Operation: llmcontracts.OperationStreaming, Followup: followup, ChatMode: mode,
							ChatSystemContext: "CHAT", WorkDir: "/tmp/.worktrees/task_123",
						}
						base := llmprompt.BuildCodexChatSystemPrompt(req.Agent.Model, followup, mode, req.ChatSystemContext, false)
						if provider == models.ProviderAnthropic {
							base = llmprompt.BuildAnthropicChatSystemPrompt(req.Agent.Model, followup, mode, req.ChatSystemContext, false)
						}
						base = llmprompt.AppendWorktreeContextPrompt(base, req.WorkDir)
						if provider == models.ProviderOpenAI && auth == models.AuthMethodOAuth && !followup {
							base = llmprompt.BuildOpenAIOAuthSystemPrompt(base)
						}
						want := base
						if mode == models.ChatModeOrchestrate {
							want = llmprompt.ApplyChatActionToolMode(base, rt.DefinitionNames(), followup)
						}
						if got := estimateProviderSystemPromptTokens(req); got != estimatedUTF8Tokens(want) {
							t.Fatalf("%s/%s followup=%v mode=%s tools=%v: got %d want %d", provider, auth, followup, mode, rt.DefinitionNames(), got, estimatedUTF8Tokens(want))
						}
					}
				}
			}
		}
	}
}

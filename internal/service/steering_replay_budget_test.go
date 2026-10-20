package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestOpenAIReplayLoadedBeforeBudgetAndTrimming(t *testing.T) {
	storeEnv := teststores.New(t)
	ctx := context.Background()
	task := &models.Task{ProjectID: "default", Title: "steering replay", Category: models.CategoryBacklog, Status: models.StatusCompleted}
	require.NoError(t, storeEnv.Stores.Tasks.Create(ctx, task))
	repo := storeEnv.Stores.Executions
	require.NoError(t, repo.Create(ctx, &models.Execution{ID: "steered", TaskID: task.ID, Status: models.ExecCompleted, PromptSent: "small prompt"}))
	replay := []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"message","role":"user","content":"` + strings.Repeat("x", 200000) + `"}]}`}}
	require.NoError(t, repo.ReplaceReasoningReplay(ctx, "steered", "", replay))
	svc := NewLLMService(nil, repo, nil, nil, nil, nil)
	req := llmcontracts.AgentRequest{Ctx: ctx, Agent: models.LLMConfig{Provider: models.ProviderOpenAI, ContextWindow: 10000}, Message: "follow up", ChatHistory: []models.Execution{{ID: "steered", PromptSent: "small prompt"}}}
	before := calculateRequestBudget(req)
	loaded, err := svc.loadProviderReplayHistory(req)
	require.NoError(t, err)
	require.Equal(t, replay, loaded.ChatHistory[0].ReplayMessages)
	require.Greater(t, calculateRequestBudget(loaded).HistoryTokens, before.HistoryTokens+40000)
	require.Nil(t, req.ChatHistory[0].ReplayMessages, "loading must not mutate caller history")
	summaryCalls := 0
	_, err = svc.localSummaryCompaction(providerAdapterFunc(func(r llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		summaryCalls++
		require.Contains(t, summaryInputText(r.SummaryInput), strings.Repeat("x", 200000))
		return llmcontracts.AgentResult{}, errors.New("context length exceeded")
	}), loaded, loaded.ChatHistory)
	require.True(t, llmcontracts.ErrorIs(err, llmcontracts.ErrorCompactionInputInfeasible))
	require.Equal(t, 1, summaryCalls, "send full replay before accepting the provider's overflow decision")
	// The actual provider entry point must count persisted replay, not just
	// the small prompt supplied by its caller.
	guarded := req
	guarded.Ctx = withoutContextCompactionFallback(ctx)
	called := false
	_, err = svc.callProviderWithCompaction(providerAdapterFunc(func(llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		called = true
		return llmcontracts.AgentResult{}, nil
	}), guarded)
	require.Error(t, err)
	require.False(t, called, "oversized saved replay must be caught before the provider request")

	imageReplay := []models.ExecutionReplayMessage{{TranscriptJSON: `[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,RETAINED"}}]}]`}}
	require.NoError(t, repo.ReplaceReasoningReplay(ctx, "steered", "saved reasoning", imageReplay))
	for _, provider := range []models.LLMProvider{models.ProviderOpenAICompatible, models.ProviderOllama} {
		req.Agent.Provider = provider
		loaded, err := svc.loadProviderReplayHistory(req)
		require.NoError(t, err)
		require.Equal(t, imageReplay, loaded.ChatHistory[0].ReplayMessages)
		require.Equal(t, "saved reasoning", loaded.ChatHistory[0].ReasoningContent)
		for _, execution := range buildCompactedReplacementHistory(loaded.ChatHistory, "summary") {
			require.Empty(t, execution.ReplayMessages, "text compaction must discard media replay")
		}
	}
}

func TestOpenAIRequestBudgetUsesEffectiveReplay(t *testing.T) {
	latest := models.Execution{ID: "latest", PromptSent: strings.Repeat("duplicate", 10000), ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"current"},{"type":"input_image","detail":"auto","image_url":"data:image/png;base64,` + strings.Repeat("A", 1000000) + `"}]}]}`}}}
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Agent: models.LLMConfig{Provider: models.ProviderOpenAI, ContextWindow: 20000}, Message: "follow up", ChatHistory: []models.Execution{{PromptSent: strings.Repeat("obsolete", 10000)}, latest}}
	budget := calculateRequestBudget(req)
	require.InDelta(t, 1844, budget.HistoryTokens, 100)
	canonical := req
	canonical.ChatHistory = []models.Execution{latest}
	require.Equal(t, budget, calculateRequestBudget(canonical))
	require.Equal(t, estimateModelVisibleRequestTokens(req), estimateModelVisibleRequestTokens(canonical))
	input, err := summaryHistoryInput(req.Agent.Provider, req.ChatHistory)
	require.NoError(t, err)
	expected, err := summaryHistoryInput(req.Agent.Provider, canonical.ChatHistory)
	require.NoError(t, err)
	require.Equal(t, expected, input, "keep the effective image checkpoint for summarization")
}

func TestAnthropicEnvironmentReplayLoadedBeforeBudget(t *testing.T) {
	db := teststores.New(t)
	ctx := context.Background()
	task := &models.Task{ProjectID: "default", Title: "environment snapshot", Category: models.CategoryBacklog, Status: models.StatusCompleted}
	require.NoError(t, db.Tasks.Create(ctx, task))
	err := db.Executions.Create(ctx, &models.Execution{ID: "environment", TaskID: task.ID, Status: models.ExecCompleted, PromptSent: "question"})
	require.NoError(t, err)
	repo := db.Executions
	replay := []models.ExecutionReplayMessage{{UserContent: "question", AssistantContent: "answer", TranscriptJSON: `{"anthropic_execution_environment":"saved environment"}`}}
	require.NoError(t, repo.ReplaceReasoningReplay(ctx, "environment", "", replay))
	svc := NewLLMService(nil, repo, nil, nil, nil, nil)
	req := llmcontracts.AgentRequest{Ctx: ctx, Agent: models.LLMConfig{Provider: models.ProviderAnthropic}, ChatHistory: []models.Execution{{ID: "environment", PromptSent: "question"}}}
	loaded, err := svc.loadProviderReplayHistory(req)
	require.NoError(t, err)
	require.Equal(t, replay, loaded.ChatHistory[0].ReplayMessages)
	require.Nil(t, req.ChatHistory[0].ReplayMessages)
	// Explicitly trimmed replay must not be reloaded.
	req.ChatHistory[0].ReplayMessages = []models.ExecutionReplayMessage{}
	loaded, err = svc.loadProviderReplayHistory(req)
	require.NoError(t, err)
	require.Empty(t, loaded.ChatHistory[0].ReplayMessages)
}

package service

import (
	"context"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestOpenAIReplayLoadedBeforeBudgetAndTrimming(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	task := &models.Task{ProjectID: "default", Title: "steering replay", Category: models.CategoryBacklog, Status: models.StatusCompleted}
	require.NoError(t, repository.NewTaskRepo(db, nil).Create(ctx, task))
	_, err := db.ExecContext(ctx, `INSERT INTO executions (id, task_id, status, prompt_sent) VALUES ('steered', ?, 'completed', 'small prompt')`, task.ID)
	require.NoError(t, err)
	repo := repository.NewExecutionRepo(db)
	replay := []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"message","role":"user","content":"` + strings.Repeat("x", 200000) + `"}]}`}}
	require.NoError(t, repo.ReplaceReasoningReplay(ctx, "steered", "", replay))
	svc := NewLLMService(nil, repo, nil, nil, nil, nil)
	req := llmcontracts.AgentRequest{Ctx: ctx, Agent: models.LLMConfig{Provider: models.ProviderOpenAI, ContextWindow: 10000}, Message: "follow up", ChatHistory: []models.Execution{{ID: "steered", PromptSent: "small prompt"}}}
	before := calculateRequestBudget(req)
	loaded, err := svc.loadOpenAIReplayHistory(req)
	require.NoError(t, err)
	require.Equal(t, replay, loaded.ChatHistory[0].ReplayMessages)
	require.Greater(t, calculateRequestBudget(loaded).HistoryTokens, before.HistoryTokens+40000)
	require.Nil(t, req.ChatHistory[0].ReplayMessages, "loading must not mutate caller history")
	trimmed := historyWithinRequestBudget(loaded, loaded.ChatHistory)
	require.Len(t, trimmed, 1)
	require.Empty(t, trimmed[0].ReplayMessages)
	require.Equal(t, "small prompt", trimmed[0].PromptSent)
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

	req.Agent.Provider = models.ProviderOpenAICompatible
	compatible, err := svc.loadOpenAIReplayHistory(req)
	require.NoError(t, err)
	require.Nil(t, compatible.ChatHistory[0].ReplayMessages, "compatible providers keep their existing history path")
}

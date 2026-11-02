package service

import (
	"fmt"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestOrderedReplayHistoryStopsAtContextReset(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		t.Run(string(provider), func(t *testing.T) {
			svc, store, req, cp := checkpointResumeFixture(t, provider, 1, 1)
			svc.execRepo = store.ExecutionStore
			oldID := req.ChatHistory[0].ID
			require.NoError(t, store.UpsertChatCompactionCheckpoint(req.Ctx, cp))
			var resetID string
			for i := 0; i < 127; i++ {
				e := models.Execution{TaskID: cp.ScopeID, Status: models.ExecCompleted, PromptSent: fmt.Sprintf("reset-turn-%d", i), StartsNewContext: i == 0}
				require.NoError(t, store.Create(req.Ctx, &e))
				if i == 0 {
					resetID = e.ID
				}
				req.ChatHistory = append(req.ChatHistory, e)
			}
			current := models.Execution{TaskID: cp.ScopeID, Status: models.ExecRunning}
			require.NoError(t, store.Create(req.Ctx, &current))
			req.ExecID = current.ID
			req.ChatHistory = req.ChatHistory[len(req.ChatHistory)-20:]
			// Restoration must discover the reset even when it lies outside the
			// supplied window, before ordinary fallback expands the history.
			boundedRestore := svc.restoreCompactionCheckpoint(req)
			require.False(t, resumeHasSummary(boundedRestore.ChatHistory))
			require.Empty(t, boundedRestore.RestoredCompactionSourceExecutionID)
			expanded, err := svc.completeTaskReplayHistory(req)
			require.NoError(t, err)
			require.Len(t, expanded.ChatHistory, 127)
			require.Equal(t, resetID, expanded.ChatHistory[0].ID)
			for _, e := range expanded.ChatHistory {
				require.NotEqual(t, oldID, e.ID)
			}
			restored := svc.restoreCompactionCheckpoint(expanded)
			require.False(t, resumeHasSummary(restored.ChatHistory), "checkpoint predating reset must not return")
			loaded, err := svc.loadProviderReplayHistory(restored)
			require.NoError(t, err)
			require.Equal(t, "reset-turn-0", loaded.ChatHistory[0].PromptSent)
		})
	}
}

func TestOrderedReplayHistoryDoesNotExpandCrossTaskOrSyntheticInput(t *testing.T) {
	svc, store, req, cp := checkpointResumeFixture(t, models.ProviderOpenAI, 1, 26)
	current := models.Execution{TaskID: cp.ScopeID, Status: models.ExecRunning}
	require.NoError(t, store.Create(req.Ctx, &current))
	req.ExecID = current.ID
	req.ChatHistory = req.ChatHistory[6:]
	original := req.ChatHistory
	req.ChatHistory[0].ContextKind = "summary"
	got, err := svc.completeTaskReplayHistory(req)
	require.NoError(t, err)
	require.Equal(t, original, got.ChatHistory)
	req.ChatHistory[0].ContextKind = ""
	req.ChatHistory[1].TaskID = "different-task"
	got, err = svc.completeTaskReplayHistory(req)
	require.NoError(t, err)
	require.Equal(t, req.ChatHistory, got.ChatHistory)
}

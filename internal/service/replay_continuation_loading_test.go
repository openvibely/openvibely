package service

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestContinuationSelectionSharedAcrossProviders(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		t.Run(string(provider), func(t *testing.T) {
			svc, store, req, _ := checkpointResumeFixture(t, models.ProviderOpenAI, 4096, 4)
			svc.execRepo = store.ExecutionStore
			req.Agent.Provider = provider
			// The last execution is incremental, while the previous one is a full
			// Responses snapshot. Selection follows storage format, not target provider.
			tailID := req.ChatHistory[3].ID
			require.NoError(t, store.ReplaceReasoningReplay(req.Ctx, tailID, "", []models.ExecutionReplayMessage{{TranscriptJSON: `[{"role":"assistant","content":"newer delta"}]`}}))
			got, err := svc.loadProviderReplayHistory(req)
			require.NoError(t, err)
			require.Empty(t, got.ChatHistory[0].ReplayMessages)
			require.Empty(t, got.ChatHistory[1].ReplayMessages)
			require.NotEmpty(t, got.ChatHistory[2].ReplayMessages)
			require.NotEmpty(t, got.ChatHistory[3].ReplayMessages)
			require.Len(t, models.EffectiveReplayHistory(got.ChatHistory), 2)
			// Execution text remains available for cross-provider recovery fallback.
			require.Equal(t, req.ChatHistory[0].PromptSent, got.ChatHistory[0].PromptSent)
		})
	}
}

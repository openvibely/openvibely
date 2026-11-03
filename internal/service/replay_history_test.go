package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestOrderedReplayHistorySharedAcrossProviders(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		for _, checkpoint := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/checkpoint=%v", provider, checkpoint), func(t *testing.T) {
				svc, store, req, cp := checkpointResumeFixture(t, provider, 1, 26)
				svc.execRepo = store.ExecutionStore
				for i, e := range req.ChatHistory {
					item := map[string]any{"role": "user", "content": fmt.Sprintf("ORDERED_TURN_%02d", i)}
					var value any = []any{item}
					if provider == models.ProviderOpenAI {
						key := "responses_delta"
						if i == 0 {
							key = "responses_input"
						}
						value = map[string]any{key: []any{item}}
					} else if provider == models.ProviderAnthropic {
						value = map[string]any{"messages": []any{item}}
					}
					raw, err := json.Marshal(value)
					require.NoError(t, err)
					require.NoError(t, store.ReplaceReasoningReplay(req.Ctx, e.ID, "", []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}))
				}
				current := models.Execution{TaskID: cp.ScopeID, Status: models.ExecRunning}
				require.NoError(t, store.Create(req.Ctx, &current))
				req.ExecID = current.ID
				if checkpoint {
					cp.SourceExecutionID = req.ChatHistory[23].ID
					require.NoError(t, store.UpsertChatCompactionCheckpoint(req.Ctx, cp))
				}
				req.ChatHistory = req.ChatHistory[6:] // The UI's last-20 window is not a model-history boundary.
				called := false
				_, err := svc.callProviderWithCompaction(providerAdapterFunc(func(got contracts.AgentRequest) (contracts.AgentResult, error) {
					called = true
					var text strings.Builder
					for _, e := range got.ChatHistory {
						for _, r := range e.ReplayMessages {
							text.WriteString(r.FullTranscriptJSON())
						}
					}
					start := 0
					if checkpoint {
						start = 24
					}
					for i := 0; i < 26; i++ {
						marker := fmt.Sprintf("ORDERED_TURN_%02d", i)
						if i < start {
							require.NotContains(t, text.String(), marker)
						} else {
							require.Equal(t, 1, strings.Count(text.String(), marker), marker)
						}
					}
					return contracts.AgentResult{}, nil
				}), req)
				require.NoError(t, err)
				require.True(t, called)
			})
		}
	}
}

func TestOrderedReplayHistoryLegacyFallback(t *testing.T) {
	svc, store, req, cp := checkpointResumeFixture(t, models.ProviderOpenAICompatible, 1, 26)
	svc.execRepo = store.ExecutionStore
	first := req.ChatHistory[0]
	require.NoError(t, store.ReplaceReasoningReplay(req.Ctx, first.ID, "", nil))
	current := models.Execution{TaskID: cp.ScopeID, Status: models.ExecRunning}
	require.NoError(t, store.Create(req.Ctx, &current))
	req.ExecID = current.ID
	req.ChatHistory = req.ChatHistory[6:]
	expanded, err := svc.completeReplayHistory(req)
	require.NoError(t, err)
	require.Len(t, expanded.ChatHistory, 26)
	require.Empty(t, expanded.ChatHistory[0].PromptSent, "paging must not load payloads")
	loaded, err := svc.loadProviderReplayHistory(expanded)
	require.NoError(t, err)
	require.Equal(t, first.PromptSent, loaded.ChatHistory[0].PromptSent)
}

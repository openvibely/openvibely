package service

import (
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/stretchr/testify/require"
)

type checkpointPageStore struct {
	persistence.ExecutionStore
	older, bounded int
}

func (s *checkpointPageStore) ListByTaskChronologicalBefore(ctx context.Context, task, before string, limit int) ([]models.Execution, error) {
	rows, err := s.ExecutionStore.ListByTaskChronologicalBefore(ctx, task, before, limit)
	s.older += len(rows)
	return rows, err
}
func (s *checkpointPageStore) ListByTaskChronologicalBetween(ctx context.Context, task, from, before string, limit int) ([]models.Execution, error) {
	rows, err := s.ExecutionStore.ListByTaskChronologicalBetween(ctx, task, from, before, limit)
	s.bounded += len(rows)
	return rows, err
}
func TestCheckpointPagingSkipsCoveredMetadata(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		for _, mode := range []string{"covered", "within-window", "outside-window", "missing", "incompatible"} {
			t.Run(string(provider)+"/"+mode, func(t *testing.T) {
				svc, store, req, cp := checkpointResumeFixture(t, provider, 1, 226)
				if provider == models.ProviderOpenAI {
					for _, e := range req.ChatHistory {
						rows, err := store.ExecutionStore.(interface {
							ReplayRowsByExecutionIDs(context.Context, []string) (map[string][]models.ExecutionReplayMessage, error)
						}).ReplayRowsByExecutionIDs(req.Ctx, []string{e.ID})
						require.NoError(t, err)
						raw := strings.Replace(rows[e.ID][0].FullTranscriptJSON(), "responses_input", "responses_delta", 1)
						require.NoError(t, store.ReplaceReasoningReplay(req.Ctx, e.ID, "", []models.ExecutionReplayMessage{{TranscriptJSON: raw}}))
					}
				}
				source := 225
				switch mode {
				case "within-window":
					source = 215
				case "outside-window":
					source = 25
				}
				cp.SourceExecutionID = req.ChatHistory[source].ID
				if mode == "incompatible" {
					cp.CompatibilityKey = "other"
					cp.Strategy = "openai_responses"
					cp.ProviderStateJSON = `[{"type":"compaction","encrypted_content":"opaque"}]`
				}
				if mode != "missing" {
					require.NoError(t, store.UpsertChatCompactionCheckpoint(req.Ctx, cp))
				}
				current := models.Execution{TaskID: cp.ScopeID, Status: models.ExecRunning}
				require.NoError(t, store.Create(req.Ctx, &current))
				req.ExecID = current.ID
				req.ChatHistory = req.ChatHistory[206:]
				counter := &checkpointPageStore{ExecutionStore: store}
				svc.execRepo = counter
				called := false
				_, err := svc.callProviderWithCompaction(providerAdapterFunc(func(got contracts.AgentRequest) (contracts.AgentResult, error) {
					called = true
					if mode == "missing" || mode == "incompatible" {
						require.Len(t, got.ChatHistory, 226)
					} else {
						var ids []string
						for _, e := range got.ChatHistory {
							if e.ID != "" {
								ids = append(ids, e.ID)
							}
						}
						require.Len(t, ids, 225-source)
						require.True(t, resumeHasSummary(got.ChatHistory))
					}
					return contracts.AgentResult{}, nil
				}), req)
				require.NoError(t, err)
				require.True(t, called)
				if mode == "missing" || mode == "incompatible" {
					require.Equal(t, 206, counter.older)
					require.Zero(t, counter.bounded)
				} else {
					require.Zero(t, counter.older)
					want := 0
					if mode == "outside-window" {
						want = 181
					}
					require.Equal(t, want, counter.bounded)
				}
			})
		}
	}
}

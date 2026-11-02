package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

type resumeReadStore struct {
	persistence.ExecutionStore
	reads           []string
	checkpointError bool
}

func (r *resumeReadStore) ReplayRowsByExecutionIDs(ctx context.Context, ids []string) (map[string][]models.ExecutionReplayMessage, error) {
	r.reads = append(r.reads, ids...)
	return r.ExecutionStore.(interface {
		ReplayRowsByExecutionIDs(context.Context, []string) (map[string][]models.ExecutionReplayMessage, error)
	}).ReplayRowsByExecutionIDs(ctx, ids)
}
func (r *resumeReadStore) GetChatCompactionCheckpoint(ctx context.Context, typ, id string) (*models.ChatCompactionCheckpoint, error) {
	if r.checkpointError {
		return nil, errors.New("checkpoint unavailable")
	}
	return r.ExecutionStore.GetChatCompactionCheckpoint(ctx, typ, id)
}

func checkpointResumeFixture(t testing.TB, provider models.LLMProvider, size, turns int) (*LLMService, *resumeReadStore, llmcontracts.AgentRequest, models.ChatCompactionCheckpoint) {
	t.Helper()
	ctx := context.Background()
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "resume", Category: models.CategoryBacklog, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	agent := models.LLMConfig{ID: "resume-model", Provider: provider, Model: "fixture", ContextWindow: 100000000, ProviderContextWindow: 100000000}
	req := llmcontracts.AgentRequest{Ctx: withoutContextCompactionFallback(ctx), Agent: agent, TransportScope: "task:" + task.ID, Message: "continue", Operation: llmcontracts.OperationStreaming}
	for i := 0; i < turns; i++ {
		e := models.Execution{TaskID: task.ID, Status: models.ExecCompleted, PromptSent: fmt.Sprintf("prompt-%d", i)}
		require.NoError(t, stores.Executions.Create(ctx, &e))
		text := fmt.Sprintf("reply-%d:", i) + strings.Repeat("x", size)
		var replay any = []any{map[string]any{"role": "assistant", "content": text}}
		if provider == models.ProviderOpenAI {
			replay = map[string]any{"responses_input": []any{map[string]any{"type": "message", "role": "assistant", "content": text}}}
		}
		if provider == models.ProviderAnthropic {
			replay = map[string]any{"messages": []any{map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}}}}
		}
		raw, err := json.Marshal(replay)
		require.NoError(t, err)
		require.NoError(t, stores.Executions.ReplaceReasoningReplay(ctx, e.ID, "", []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}))
		req.ChatHistory = append(req.ChatHistory, e)
	}
	cp := models.ChatCompactionCheckpoint{ScopeType: "task", ScopeID: task.ID, ModelConfigID: agent.ID, CompatibilityKey: providerCompatibilityKey(agent), SourceExecutionID: req.ChatHistory[len(req.ChatHistory)-1].ID, Strategy: "local_summary", History: []models.Execution{{ContextKind: "summary", PromptSent: "saved summary"}}}
	read := &resumeReadStore{ExecutionStore: stores.Executions}
	return &LLMService{execRepo: read}, read, req, cp
}

func TestCheckpointResumeLoadsOnlyUncoveredReplay(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		for _, mode := range []string{"covered", "suffix", "missing", "incompatible", "read-error", "context-reset", "native-covered", "native-suffix"} {
			t.Run(string(provider)+"/"+mode, func(t *testing.T) {
				if strings.HasPrefix(mode, "native-") && provider != models.ProviderOpenAI && provider != models.ProviderAnthropic {
					t.Skip("provider has no native checkpoint")
				}
				svc, store, req, cp := checkpointResumeFixture(t, provider, 1024, 3)
				expected := []string{}
				switch mode {
				case "suffix", "native-suffix":
					cp.SourceExecutionID = req.ChatHistory[1].ID
					expected = []string{req.ChatHistory[2].ID}
				case "missing", "incompatible", "read-error", "context-reset":
					for _, e := range req.ChatHistory {
						expected = append(expected, e.ID)
					}
				}
				if strings.HasPrefix(mode, "native-") {
					setResumeNativeCheckpoint(t, &cp, provider, 1024)
				}
				if mode == "incompatible" {
					cp.CompatibilityKey = "different"
					cp.Strategy = "openai_responses"
					cp.ProviderStateJSON = `[{"type":"compaction","encrypted_content":"opaque"}]`
				}
				if mode == "context-reset" {
					req.ChatHistory[0].StartsNewContext = true
					cp.SourceExecutionID = "older-context"
				}
				if mode != "missing" {
					require.NoError(t, store.UpsertChatCompactionCheckpoint(req.Ctx, cp))
				}
				store.checkpointError = mode == "read-error"
				called := false
				_, err := svc.callProviderWithCompaction(providerAdapterFunc(func(got llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
					called = true
					if mode == "covered" || (mode == "suffix" && provider != models.ProviderOpenAI) {
						require.True(t, resumeHasSummary(got.ChatHistory))
					}
					if strings.HasPrefix(mode, "native-") {
						require.True(t, got.HasNativeState())
					}
					if mode == "suffix" || mode == "native-suffix" {
						require.NotEmpty(t, got.ChatHistory[len(got.ChatHistory)-1].ReplayMessages)
					}
					return llmcontracts.AgentResult{}, nil
				}), req)
				require.NoError(t, err)
				require.True(t, called)
				require.ElementsMatch(t, expected, store.reads)
			})
		}
	}
}

// Keep the ordinary streaming benchmarks. This additional workload models a
// long thread whose twenty saved replay snapshots are each ten MiB. The adapter
// verifies the selected history but avoids model/network latency. Run with
// -benchtime=1x to measure fresh request preparation without repeated large reads.
func BenchmarkCheckpointResume(b *testing.B) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		for _, mode := range []string{"covered", "suffix", "missing", "incompatible", "native-covered", "native-suffix"} {
			b.Run(string(provider)+"/"+mode, func(b *testing.B) {
				if strings.HasPrefix(mode, "native-") && provider != models.ProviderOpenAI && provider != models.ProviderAnthropic {
					b.Skip("provider has no native checkpoint")
				}
				svc, store, req, cp := checkpointResumeFixture(b, provider, 10<<20, 20)
				if mode == "suffix" || mode == "native-suffix" {
					cp.SourceExecutionID = req.ChatHistory[18].ID
				}
				if strings.HasPrefix(mode, "native-") {
					setResumeNativeCheckpoint(b, &cp, provider, 10<<20)
				}
				if mode == "incompatible" {
					cp.CompatibilityKey = "different"
					cp.Strategy = "openai_responses"
					cp.ProviderStateJSON = `[{"type":"compaction","encrypted_content":"opaque"}]`
				}
				if mode != "missing" {
					require.NoError(b, store.UpsertChatCompactionCheckpoint(req.Ctx, cp))
				}
				adapter := providerAdapterFunc(func(got llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
					if mode == "covered" || (mode == "suffix" && provider != models.ProviderOpenAI) {
						require.True(b, resumeHasSummary(got.ChatHistory))
					}
					return llmcontracts.AgentResult{}, nil
				})
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_, err := svc.callProviderWithCompaction(adapter, req)
					require.NoError(b, err)
				}
				b.StopTimer()
				b.ReportMetric(float64(len(store.reads))/float64(b.N), "replays/op")
			})
		}
	}
}

func resumeHasSummary(history []models.Execution) bool {
	for _, e := range history {
		if e.ContextKind == "summary" && e.PromptSent == "saved summary" {
			return true
		}
	}
	return false
}

func setResumeNativeCheckpoint(t testing.TB, cp *models.ChatCompactionCheckpoint, provider models.LLMProvider, size int) {
	t.Helper()
	cp.History = nil
	text := strings.Repeat("x", size)
	var state any = []any{map[string]any{"type": "message", "role": "assistant", "content": text}}
	cp.Strategy = "openai_responses"
	if provider == models.ProviderAnthropic {
		cp.Strategy = "anthropic_context_management"
		state = map[string]any{"version": 1, "messages": []any{map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}}}}
	}
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	cp.ProviderStateJSON = string(raw)
}

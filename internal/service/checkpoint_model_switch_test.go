package service

import (
	"context"
	"encoding/json"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

// Portable checkpoint history must survive selecting a different saved model,
// including a different configuration for the same provider. Native state is
// deliberately absent: this test does not authorize replaying opaque state to
// an incompatible provider.
func TestPortableCheckpointAcrossSavedModelSelections(t *testing.T) {
	providers := []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible}
	for _, strategy := range []string{"local_summary", "provider_recovery"} {
		for _, from := range providers {
			// Recovery checkpoints are created after leaving OpenAI, on the destination provider.
			if strategy == "provider_recovery" && from == models.ProviderOpenAI {
				continue
			}
			destinations := append([]models.LLMProvider{from}, providers...)
			for index, to := range destinations {
				name := string(from) + "_to_" + string(to)
				if index == 0 {
					name += "_same_config"
				} else {
					name += "_different_config"
				}
				t.Run(strategy+"/"+name, func(t *testing.T) {
					ctx := context.Background()
					stores := teststores.New(t).Stores
					modelName := func(p models.LLMProvider) string {
						switch p {
						case models.ProviderOpenAI:
							return "gpt-5.3-codex"
						case models.ProviderAnthropic:
							return "claude-opus-5-5"
						default:
							return "test"
						}
					}
					sourceModel := models.LLMConfig{Name: "Source model", Provider: from, Model: modelName(from), ContextWindow: 200000}
					require.NoError(t, stores.LLMConfigs.Create(ctx, &sourceModel))
					require.NotEmpty(t, sourceModel.ID)
					targetModel := sourceModel
					if index != 0 {
						targetModel = models.LLMConfig{Name: "Destination model", Provider: to, Model: modelName(to), ContextWindow: 200000}
						require.NoError(t, stores.LLMConfigs.Create(ctx, &targetModel))
						require.NotEmpty(t, targetModel.ID)
						require.NotEqual(t, sourceModel.ID, targetModel.ID)
					}
					task := &models.Task{ProjectID: "default", Title: "checkpoint switch", Category: models.CategoryBacklog, Status: models.StatusCompleted}
					require.NoError(t, stores.Tasks.Create(ctx, task))
					old := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "SUPERSEDED_REQUEST"}
					require.NoError(t, stores.Executions.Create(ctx, old))
					require.NoError(t, stores.Executions.Complete(ctx, old.ID, models.ExecCompleted, "SUPERSEDED_ANSWER", "", 0, 0))
					old.Status, old.Output = models.ExecCompleted, "SUPERSEDED_ANSWER"
					current := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "NEW_FOLLOWUP"}
					require.NoError(t, stores.Executions.Create(ctx, current))
					req := llmcontracts.AgentRequest{Ctx: ctx, ExecID: current.ID, Followup: true, Operation: llmcontracts.OperationStreaming, TransportScope: "task:" + task.ID, Agent: sourceModel, ChatHistory: []models.Execution{*old}, Message: "NEW_FOLLOWUP"}
					svc := &LLMService{execRepo: stores.Executions}
					compacted := req
					compacted.ChatHistory = buildCompactedReplacementHistory(nil, "PORTABLE_SUMMARY")
					svc.persistCompactionCheckpoint(req, compacted, "PORTABLE_SUMMARY", strategy)
					checkpoint, err := stores.Executions.GetChatCompactionCheckpoint(ctx, "task", task.ID)
					require.NoError(t, err)
					require.NotNil(t, checkpoint)
					require.Equal(t, sourceModel.ID, checkpoint.ModelConfigID)
					require.Empty(t, checkpoint.ProviderStateJSON)
					req.Agent = targetModel
					// A fresh service forces checkpoint restoration from SQLite.
					svc = &LLMService{execRepo: stores.Executions}
					calls := 0
					_, err = svc.callProviderWithCompaction(providerAdapterFunc(func(actual llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
						calls++
						require.Empty(t, actual.SummaryInput, "a small portable checkpoint needs no new summary")
						input, e := summaryHistoryInput(to, actual.ChatHistory)
						require.NoError(t, e)
						raw, e := json.Marshal(input)
						require.NoError(t, e)
						require.Contains(t, string(raw), "PORTABLE_SUMMARY", "saved summary must reach the selected provider")
						require.NotContains(t, string(raw), "SUPERSEDED_ANSWER", "checkpoint must replace covered history")
						require.Equal(t, "NEW_FOLLOWUP", actual.Message)
						return llmcontracts.AgentResult{Output: "done"}, nil
					}), req)
					require.NoError(t, err)
					require.Equal(t, 1, calls)
				})
			}
		}
	}
}

func TestProviderSpecificCheckpointStillRequiresCompatibility(t *testing.T) {
	for _, field := range []string{"provider_state", "session_state"} {
		for _, change := range []string{"none", "config_id", "endpoint"} {
			t.Run(field+"/"+change, func(t *testing.T) {
				ctx := context.Background()
				stores := teststores.New(t).Stores
				source := models.LLMConfig{ID: "source-model", Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex", BaseURL: "https://one.example/v1"}
				checkpoint := models.ChatCompactionCheckpoint{ScopeType: "chat_project", ScopeID: "compatibility", ModelConfigID: source.ID, CompatibilityKey: providerCompatibilityKey(source), History: buildCompactedReplacementHistory(nil, "NATIVE_CHECKPOINT_HISTORY")}
				if field == "provider_state" {
					checkpoint.ProviderStateJSON = `[{"type":"compaction","encrypted_content":"opaque"}]`
				} else {
					checkpoint.ProviderSessionStateJSON = `{"session":"provider-specific"}`
				}
				require.NoError(t, stores.Executions.UpsertChatCompactionCheckpoint(ctx, checkpoint))
				if field == "session_state" {
					require.NoError(t, stores.Executions.UpsertChatProviderSessionState(ctx, checkpoint))
				}
				target := source
				if change == "config_id" {
					target.ID = "other-model"
				}
				if change == "endpoint" {
					target.BaseURL = "https://two.example/v1"
				}
				req := llmcontracts.AgentRequest{Ctx: ctx, TransportScope: "chat:project:compatibility", Agent: target, ChatHistory: []models.Execution{{PromptSent: "original"}}}
				svc := &LLMService{execRepo: stores.Executions}
				got := svc.restoreCompactionCheckpoint(req)
				if change != "none" {
					require.Equal(t, req.ChatHistory, got.ChatHistory)
					require.Empty(t, got.NativeCompactionStateJSON)
					require.Empty(t, got.ProviderSessionStateJSON)
				} else {
					require.Equal(t, checkpoint.ProviderStateJSON, got.NativeCompactionStateJSON)
					require.Equal(t, checkpoint.ProviderSessionStateJSON, got.ProviderSessionStateJSON)
					require.Equal(t, checkpoint.History[0].PromptSent, got.ChatHistory[0].PromptSent)
				}
			})
		}
	}
}

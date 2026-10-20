package service

import (
	"context"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestNativeCheckpointFollowupCompactionBudget(t *testing.T) {
	for _, scenario := range []string{"image_checkpoint", "replaced_checkpoint", "overlapping_text_checkpoint", "checkpoint_and_new_text", "genuinely_large_history"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			stores := teststores.New(t).Stores
			agent := models.LLMConfig{Name: "budget model", Provider: models.ProviderOpenAI, Model: "gpt-6-astra", ContextWindow: 40000}
			require.NoError(t, stores.LLMConfigs.Create(ctx, &agent))
			task := &models.Task{ProjectID: "default", Title: "checkpoint budget", Category: models.CategoryBacklog, Status: models.StatusCompleted}
			require.NoError(t, stores.Tasks.Create(ctx, task))
			source := models.Execution{TaskID: task.ID, Status: models.ExecCompleted, PromptSent: "old"}
			require.NoError(t, stores.Executions.Create(ctx, &source))
			native := `[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,` + strings.Repeat("A", 1000000) + `"}]}]`
			if scenario == "overlapping_text_checkpoint" {
				native = `[{"type":"message","role":"user","content":"` + strings.Repeat("x", 80000) + `"}]`
			}
			req := llmcontracts.AgentRequest{Ctx: ctx, Agent: agent, TransportScope: "task:" + task.ID, Followup: true, Message: "continue", ChatHistory: []models.Execution{source}}
			svc := &LLMService{execRepo: stores.Executions}
			svc.persistNativeProviderStateCheckpoint(req, "openai_responses", native)
			if scenario != "image_checkpoint" {
				newer := models.Execution{TaskID: task.ID, Status: models.ExecCompleted, PromptSent: "new question", Output: "answer"}
				require.NoError(t, stores.Executions.Create(ctx, &newer))
				if scenario != "checkpoint_and_new_text" {
					text := "small replacement"
					if scenario == "overlapping_text_checkpoint" {
						text = strings.Repeat("x", 80000)
					}
					if scenario == "genuinely_large_history" {
						text = strings.Repeat("large ", 40000)
					}
					replay := `{"responses_input":[{"type":"message","role":"user","content":"` + text + `"}]}`
					require.NoError(t, stores.Executions.ReplaceReasoningReplay(ctx, newer.ID, "", []models.ExecutionReplayMessage{{TranscriptJSON: replay}}))
				}
				req.ChatHistory = append(req.ChatHistory, newer)
			}
			// A fresh service loads the real checkpoint and replay from SQLite.
			svc = &LLMService{execRepo: stores.Executions}
			calls := 0
			_, err := svc.callProviderWithCompaction(providerAdapterFunc(func(actual llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				calls++
				require.Equal(t, scenario == "genuinely_large_history", actual.ForceNativeCompaction, "only genuinely large model input should force compaction")
				budget := calculateRequestBudget(actual)
				if scenario == "replaced_checkpoint" || scenario == "overlapping_text_checkpoint" {
					require.Zero(t, budget.NativeStateTokens)
				}
				if scenario == "image_checkpoint" || scenario == "checkpoint_and_new_text" {
					require.InDelta(t, 1844, budget.NativeStateTokens, 100)
				}
				return llmcontracts.AgentResult{}, nil
			}), req)
			require.NoError(t, err)
			require.Equal(t, 1, calls)
		})
	}
}

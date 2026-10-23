package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestProviderSwitchRecoversSteeringAndPreservesReadableTail(t *testing.T) {
	for _, p := range []models.LLMProvider{models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		t.Run(string(p), func(t *testing.T) {
			ctx := context.Background()
			stores := teststores.New(t).Stores
			task := &models.Task{ProjectID: "default", Title: "recovery", Category: models.CategoryBacklog, Status: models.StatusCompleted}
			require.NoError(t, stores.Tasks.Create(ctx, task))
			older := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "old run"}
			require.NoError(t, stores.Executions.Create(ctx, older))
			require.NoError(t, stores.Executions.Complete(ctx, older.ID, models.ExecCompleted, "older fact", "", 0, 0))
			exec := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "inspect"}
			require.NoError(t, stores.Executions.Create(ctx, exec))
			for _, state := range []string{"old", "visible", "pending", "cancelled"} {
				input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, ExpectedTurnID: exec.ID, Content: state + " steering"}
				require.NoError(t, stores.ThreadInputs.CreateSteeringForActiveExecution(ctx, input, exec.ID))
				switch state {
				case "old", "visible":
					require.NoError(t, stores.ThreadInputs.MarkApplied(ctx, input.ID, exec.ID, exec.ID))
				case "cancelled":
					_, err := stores.ThreadInputs.CancelPending(ctx, input.ID)
					require.NoError(t, err)
				}
			}
			transcript := `{"responses_input":[{"type":"compaction","encrypted_content":"opaque"},
 {"type":"message","role":"user","content":[{"type":"input_text","text":"visible steering"},{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="}]},
 {"type":"function_call","call_id":"a","name":"read_file","arguments":"{}"},
 {"type":"function_call","call_id":"b","name":"read_file","arguments":"{}"},
 {"type":"function_call_output","call_id":"a","output":"result A"},
 {"type":"function_call_output","call_id":"b","output":"result B"},
 {"type":"message","role":"assistant","content":"tail answer"}]}`
			require.NoError(t, stores.Executions.ReplaceReasoningReplay(ctx, exec.ID, "", []models.ExecutionReplayMessage{{TranscriptJSON: transcript}}))
			require.NoError(t, stores.Executions.Complete(ctx, exec.ID, models.ExecCompleted, "[Tool read_file done]\nBACKGROUND_TOOL_FACT\n[/Tool]\ntail answer", "", 0, 0))
			current := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
			require.NoError(t, stores.Executions.Create(ctx, current))
			// Reload from storage as a new service instance would after restarting.
			saved, err := stores.Executions.GetByID(ctx, exec.ID)
			require.NoError(t, err)
			rows, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{exec.ID})
			require.NoError(t, err)
			saved.ReplayMessages = rows[exec.ID]
			_, tail := portableRecoveryParts([]models.Execution{*saved})
			expected, err := summaryHistoryInput(p, tail)
			require.NoError(t, err)
			svc := &LLMService{execRepo: stores.Executions, threadInputRepo: stores.ThreadInputs}
			calls := 0
			_, err = svc.callProviderWithCompaction(providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				require.Empty(t, req.SummaryInput)
				calls++
				actual, e := summaryHistoryInput(p, req.ChatHistory)
				require.NoError(t, e)
				raw, _ := json.Marshal(actual)
				require.Contains(t, string(raw), "old steering")
				require.Contains(t, string(raw), "BACKGROUND_TOOL_FACT")
				require.NotContains(t, string(raw), "pending steering")
				require.NotContains(t, string(raw), "cancelled steering")
				require.Equal(t, expected, actual[len(actual)-len(expected):], "readable tail survives truncation retries")
				if calls == 1 {
					return llmcontracts.AgentResult{}, fmt.Errorf("maximum context length exceeded")
				}
				require.Len(t, actual, len(expected)+1)
				return llmcontracts.AgentResult{Output: "done"}, nil
			}), llmcontracts.AgentRequest{Ctx: ctx, ExecID: current.ID, TransportScope: "task:" + task.ID, Followup: true, Message: "continue", Agent: models.LLMConfig{ID: string(p), Provider: p, Model: "test", ContextWindow: 200000}, ChatHistory: []models.Execution{*saved}})
			require.NoError(t, err)
			require.Equal(t, 2, calls)
			rows, err = stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{exec.ID})
			require.NoError(t, err)
			require.Equal(t, transcript, rows[exec.ID][0].TranscriptJSON, "recovery never rewrites saved history")
		})
	}
}

package service

import (
	"context"
	"encoding/json"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestProviderSwitchLoadsExistingTranscriptFromEarlierExecution(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "archive", Category: models.CategoryBacklog, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	old := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "inspect", Output: "[Tool read_file done]\nOLD_TOOL_FACT\n[/Tool]"}
	require.NoError(t, stores.Executions.Create(ctx, old))
	require.NoError(t, stores.Executions.Complete(ctx, old.ID, models.ExecCompleted, old.Output, "", 0, 0))

	current := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, stores.Executions.Create(ctx, current))
	transcript := `{"responses_input":[{"type":"compaction","encrypted_content":"opaque"},{"type":"message","role":"user","content":"recent"},{"type":"message","role":"assistant","content":"recent answer"}]}`
	for _, provider := range []models.LLMProvider{models.ProviderOpenAICompatible, models.ProviderAnthropic, models.ProviderOllama} {
		t.Run(string(provider), func(t *testing.T) {
			s := &LLMService{execRepo: stores.Executions}
			req := llmcontracts.AgentRequest{Ctx: ctx, ExecID: current.ID, TransportScope: "task:" + task.ID, Followup: true, Message: "next", Agent: models.LLMConfig{Provider: provider, Model: "test", ContextWindow: 200000},
				ChatHistory: []models.Execution{{Status: models.ExecCompleted, PromptSent: "recent", Output: "recent answer", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: transcript}}}}}
			called := false
			_, err := s.callProviderWithCompaction(providerAdapterFunc(func(sent llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				called = true
				input, err := summaryHistoryInput(provider, sent.ChatHistory)
				require.NoError(t, err)
				raw, _ := json.Marshal(input)
				require.Contains(t, string(raw), "OLD_TOOL_FACT")
				require.Contains(t, string(raw), "recent answer")
				require.NotContains(t, string(raw), "encrypted_content")
				require.NotContains(t, string(raw), "responses_compaction_sources")
				return llmcontracts.AgentResult{Output: "ok"}, nil
			}), req)
			require.NoError(t, err)
			require.True(t, called)
			require.Equal(t, transcript, req.ChatHistory[0].ReplayMessages[0].TranscriptJSON)
		})
	}
}

func TestProviderSwitchRejectsUnrecoverableCompaction(t *testing.T) {
	h := []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"compaction","encrypted_content":"legacy"}]}`}}}}
	for _, p := range []models.LLMProvider{models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		_, err := (&LLMService{}).callProviderWithCompaction(providerAdapterFunc(func(llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
			t.Fatal("must not send history after losing opaque context")
			return llmcontracts.AgentResult{}, nil
		}), llmcontracts.AgentRequest{Ctx: context.Background(), Agent: models.LLMConfig{Provider: p, ContextWindow: 200000}, ChatHistory: h})
		require.ErrorContains(t, err, "readable history")
		_, err = summaryHistoryInput(p, h)
		require.ErrorContains(t, err, "readable history")
	}
	_, err := summaryHistoryInput(models.ProviderOpenAI, h)
	require.NoError(t, err, "native OpenAI can still use its opaque state")
}

func TestProviderSwitchRepairsInterruptedParallelCalls(t *testing.T) {
	transcript := `{"responses_input":[
		{"type":"function_call","call_id":"a","name":"read_file","arguments":"{}"},
		{"type":"function_call","call_id":"b","name":"read_file","arguments":"{}"},
		{"type":"function_call_output","call_id":"b","output":"COMPLETED_RESULT"}
	]}`
	for _, p := range []models.LLMProvider{models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		for _, mode := range []string{"summary", "followup"} {
			t.Run(string(p)+"/"+mode, func(t *testing.T) {
				h := []models.Execution{{Status: models.ExecFailed, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: transcript}}}}
				check := func(h []models.Execution) {
					input, err := summaryHistoryInput(p, h)
					require.NoError(t, err)
					raw, _ := json.Marshal(input)
					require.Contains(t, string(raw), "COMPLETED_RESULT")
					require.Contains(t, string(raw), "aborted")
					if p == models.ProviderAnthropic {
						require.Len(t, input, 3)
						var result struct {
							Blocks []map[string]any `json:"content_blocks"`
						}
						require.NoError(t, json.Unmarshal(input[1], &result))
						require.Len(t, result.Blocks, 2)
						require.Equal(t, "b", result.Blocks[0]["tool_use_id"])
						require.Equal(t, "a", result.Blocks[1]["tool_use_id"])
					} else {
						require.Len(t, input, 4)
						var result map[string]any
						require.NoError(t, json.Unmarshal(input[2], &result))
						require.Equal(t, "a", result["tool_call_id"])
						require.Equal(t, "aborted", result["content"])
					}
				}
				if mode == "summary" {
					check(h)
				} else {
					called := false
					_, err := (&LLMService{}).callProviderWithCompaction(providerAdapterFunc(func(r llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
						called = true
						check(r.ChatHistory)
						return llmcontracts.AgentResult{Output: "ok"}, nil
					}), llmcontracts.AgentRequest{Ctx: context.Background(), Followup: true, Message: "continue", Agent: models.LLMConfig{Provider: p, ContextWindow: 200000}, ChatHistory: h})
					require.NoError(t, err)
					require.True(t, called)
				}
				require.Equal(t, transcript, h[0].ReplayMessages[0].TranscriptJSON)
			})
		}
	}
}

func TestProviderSwitchTranscriptRecoveryPagesAndRespectsNewContext(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "paging", Category: models.CategoryBacklog, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	create := func(prompt, output string, boundary bool) *models.Execution {
		e := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: prompt, Output: output, StartsNewContext: boundary}
		require.NoError(t, stores.Executions.Create(ctx, e))
		require.NoError(t, stores.Executions.Complete(ctx, e.ID, models.ExecCompleted, output, "", 0, 0))
		e.Status = models.ExecCompleted
		return e
	}
	create("excluded", "BEFORE_NEW_CONTEXT", false)
	create("new context", "FIRST_INCLUDED_FACT", true)
	for i := 0; i < 101; i++ {
		create("read", "saved tool output", false)
	}
	latest := create("last prompt", "LAST_INCLUDED_FACT", false)
	opaque := `{"responses_input":[{"type":"compaction","encrypted_content":"opaque"}]}`
	// Steering overwrites the provider context, but leaves the existing output intact.
	require.NoError(t, stores.ThreadInputs.CommitLocalSteering(ctx, persistence.LocalSteeringCommit{ExecutionID: latest.ID, ResponsesHistory: []any{map[string]any{"type": "compaction", "encrypted_content": "opaque"}}}))
	latest.ReplayMessages = []models.ExecutionReplayMessage{{TranscriptJSON: opaque}}
	current := create("next", "", false)
	svc := &LLMService{execRepo: stores.Executions}
	for _, provider := range []models.LLMProvider{models.ProviderAnthropic, models.ProviderOpenAICompatible} {
		recovered, err := svc.recoverPortableProviderHistory(llmcontracts.AgentRequest{Ctx: ctx, ExecID: current.ID, TransportScope: "task:" + task.ID, ChatHistory: []models.Execution{*latest}, Agent: models.LLMConfig{Provider: provider}})
		require.NoError(t, err)
		require.Len(t, recovered.ChatHistory, 103)
		input, err := summaryHistoryInput(provider, recovered.ChatHistory)
		require.NoError(t, err)
		raw, err := json.Marshal(input)
		require.NoError(t, err)
		require.Contains(t, string(raw), "FIRST_INCLUDED_FACT")
		require.Contains(t, string(raw), "LAST_INCLUDED_FACT")
		require.NotContains(t, string(raw), "BEFORE_NEW_CONTEXT")
		require.NotContains(t, string(raw), "encrypted_content")
	}
	rows, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{latest.ID})
	require.NoError(t, err)
	require.Contains(t, rows[latest.ID][0].TranscriptJSON, "encrypted_content")
	require.NotContains(t, rows[latest.ID][0].TranscriptJSON, "responses_compaction_sources")
}

func TestReadableCompactionRecoveryPreservesLaterStructuredTurns(t *testing.T) {
	later := models.Execution{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `[{"role":"assistant","tool_calls":[{"id":"call","type":"function","function":{"name":"read_file","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call","content":"tool result"}]`}}}
	history := []models.Execution{{PromptSent: "earlier", Output: "OLD_FACT", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"compaction","encrypted_content":"opaque"}]}`}}}, later}
	recovered, err := readableCompactedHistory(history)
	require.NoError(t, err)
	require.Equal(t, later, recovered[1])
	require.Contains(t, recovered[0].ReplayMessages[0].TranscriptJSON, "OLD_FACT")
	require.Contains(t, history[0].ReplayMessages[0].TranscriptJSON, "encrypted_content")
}

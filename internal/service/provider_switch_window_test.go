package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	"github.com/stretchr/testify/require"
)

func TestProviderSwitchWindowFitsBelowTrigger(t *testing.T) {
	for _, p := range []models.LLMProvider{models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		t.Run(string(p), func(t *testing.T) {
			req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Followup: true, Message: "PENDING", ProjectInstructions: "Keep these instructions", Agent: models.LLMConfig{Provider: p, Model: "test", ContextWindow: 200000, CompactionThreshold: 50000}}
			raw, _ := json.Marshal([]map[string]any{
				{"role": "user", "content": strings.Repeat("old ", 210000)},
				{"role": "user", "content": "recent question"},
				{"role": "assistant", "tool_calls": []any{map[string]any{"id": "a", "type": "function", "function": map[string]any{"name": "read_file", "arguments": "{}"}}, map[string]any{"id": "b", "type": "function", "function": map[string]any{"name": "read_file", "arguments": "{}"}}}},
				{"role": "tool", "tool_call_id": "a", "content": "first result"},
				{"role": "tool", "tool_call_id": "b", "content": "second result"},
				{"role": "assistant", "content": "newest answer"},
			})
			history := []models.Execution{{Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}}}
			input, err := summaryHistoryInput(p, history)
			require.NoError(t, err)
			selected := selectRecoveryWindow(req, input)
			text := summaryInputText(selected)
			require.NotContains(t, text, "old old")
			require.Contains(t, text, "recent question")
			require.Contains(t, text, "first result")
			require.Contains(t, text, "second result")
			req.ChatHistory = recoveryWindowHistory(p, selected)
			budget := calculateRequestBudget(req)
			require.Less(t, budget.TotalInputTokens(), compactionLimitsForAgent(req.Agent).TriggerLimit)
			require.False(t, historyNeedsCompaction(budget, compactionLimitsForAgent(req.Agent).TriggerLimit))
			// A linked group that cannot fit is omitted whole, keeping only newer text.
			for i := range input {
				if len(summaryToolIDs(input[i])) > 0 {
					var m map[string]any
					require.NoError(t, json.Unmarshal(input[i], &m))
					if p == models.ProviderAnthropic {
						blocks, _ := m["content_blocks"].([]any)
						m["content_blocks"] = append(blocks, map[string]any{"type": "text", "text": strings.Repeat("huge ", 210000)})
					} else {
						m["content"] = strings.Repeat("huge ", 210000)
					}
					input[i], _ = json.Marshal(m)
					break
				}
			}
			selected = selectRecoveryWindow(req, input)
			require.Contains(t, summaryInputText(selected), "newest answer")
			for _, message := range selected {
				require.Empty(t, summaryToolIDs(message))
			}
		})
	}
}

func TestProviderSwitchOverflowTrimsWithoutSummary(t *testing.T) {
	for _, p := range []models.LLMProvider{models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		t.Run(string(p), func(t *testing.T) {
			req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Followup: true, Message: "PENDING", Agent: models.LLMConfig{Provider: p, Model: "test", ContextWindow: 200000}, ChatHistory: []models.Execution{{Status: models.ExecCompleted, PromptSent: "old", Output: "answer"}, {Status: models.ExecCompleted, PromptSent: "new", Output: "recent"}}}
			expectedInput, e := summaryHistoryInput(p, req.ChatHistory)
			require.NoError(t, e)
			calls := 0
			var last int
			_, err := (&LLMService{}).callProviderWithTruncatedRecovery(providerAdapterFunc(func(sent llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				calls++
				require.Equal(t, "PENDING", sent.Message)
				require.Empty(t, sent.SummaryInput)
				require.Equal(t, llmcontracts.OperationStreaming, sent.Operation)
				require.False(t, sent.ForceNativeCompaction)
				input, e := summaryHistoryInput(p, sent.ChatHistory)
				require.NoError(t, e)
				if calls > 1 {
					require.Less(t, len(input), last)
				}
				last = len(input)
				return llmcontracts.AgentResult{}, errors.New("context length exceeded")
			}), req, req)
			require.Error(t, err)
			require.Equal(t, len(expectedInput)+1, calls)
			require.Zero(t, last)
		})
	}
	// Exercise the same destination-window recovery through the real compatible
	// adapter, with overflow delivered inside HTTP 200 rather than a mocked error.
	for _, outcome := range []string{"overflow_once", "overflow_exhausted"} {
		t.Run("litellm_sse/"+outcome, func(t *testing.T) {
			t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
			var sizes []int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					http.Error(w, "read", 500)
					return
				}
				var body struct {
					Messages []json.RawMessage `json:"messages"`
				}
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Error(err)
					http.Error(w, "json", 400)
					return
				}
				sizes = append(sizes, len(body.Messages))
				if !strings.Contains(string(raw), "PENDING") {
					t.Error("lost pending user request")
				}
				if strings.Contains(string(raw), "CONTEXT CHECKPOINT COMPACTION") {
					t.Error("switch-window recovery must not generate a summary")
				}
				if len(sizes) > 1 && sizes[len(sizes)-1] >= sizes[len(sizes)-2] {
					t.Error("overflow recovery did not shrink history")
				}
				if outcome == "overflow_once" && len(sizes) > 1 {
					compactionServerReply(w, models.ProviderOpenAICompatible, "RECOVERED", "")
					return
				}
				compactionMatrixError(w, true, 400, "context_length_exceeded", "context window exceeded")
			}))
			defer server.Close()
			svc := NewLLMService(nil, nil, nil, nil, nil, nil)
			req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Followup: true, Message: "PENDING", Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "test", ContextWindow: 200000, AuthMethod: models.AuthMethodAPIKey, APIKey: "test", BaseURL: server.URL + "/v1", PresetSlug: "vllm"}, ChatHistory: []models.Execution{{Status: models.ExecCompleted, PromptSent: "old", Output: "answer"}, {Status: models.ExecCompleted, PromptSent: "new", Output: "recent"}}}
			input, err := summaryHistoryInput(req.Agent.Provider, req.ChatHistory)
			require.NoError(t, err)
			result, err := svc.callProviderWithTruncatedRecovery(svc.providerAdapters[req.Agent.Provider], req, req)
			if outcome == "overflow_once" {
				require.NoError(t, err)
				require.Equal(t, "RECOVERED", result.Output)
				require.Len(t, sizes, 2)
			} else {
				require.Error(t, err)
				require.Len(t, sizes, len(input)+1, "overflow retries must stop after history is exhausted")
			}
		})
	}

}

func TestProviderSwitchDoesNotReplayExecutedTools(t *testing.T) {
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Message: "do work", Agent: models.LLMConfig{Provider: models.ProviderAnthropic, Model: "test", ContextWindow: 200000}, ChatHistory: []models.Execution{{PromptSent: "earlier"}}}
	calls := 0
	_, err := (&LLMService{}).callProviderWithTruncatedRecovery(providerAdapterFunc(func(sent llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		calls++
		return llmcontracts.AgentResult{}, &anthropicclient.ConversationError{Err: errors.New("context length exceeded"), MessagesJSON: `[{"role":"assistant","content":[{"type":"tool_use","id":"new-call"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"new-call"}]}]`}
	}), req, req)
	require.Error(t, err)
	require.Equal(t, 1, calls)
}

func TestProviderSwitchLargeOpaqueHistorySendsFollowupDirectly(t *testing.T) {
	for _, p := range []models.LLMProvider{models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		t.Run(string(p), func(t *testing.T) {
			req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Followup: true, Message: "PENDING", ContextTokenEstimate: 6000000, ForceNativeCompaction: true, Agent: models.LLMConfig{Provider: p, Model: "test", ContextWindow: 200000, CompactionThreshold: 50000}, ChatHistory: []models.Execution{
				{PromptSent: "OLD", Output: strings.Repeat("ancient ", 500000)},
				{PromptSent: "recent question", Output: "recent answer", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"compaction","encrypted_content":"opaque"},{"type":"message","role":"user","content":"recent question"},{"type":"message","role":"assistant","content":"recent answer"}]}`}}},
			}}
			calls := 0
			_, err := (&LLMService{}).callProviderWithCompaction(providerAdapterFunc(func(sent llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				calls++
				require.Empty(t, sent.SummaryInput)
				require.Equal(t, "PENDING", sent.Message)
				require.Equal(t, llmcontracts.OperationStreaming, sent.Operation)
				require.False(t, sent.ForceNativeCompaction)
				require.Zero(t, sent.ContextTokenEstimate)
				require.False(t, historyNeedsCompaction(calculateRequestBudget(sent), compactionLimitsForAgent(sent.Agent).TriggerLimit))
				input, e := summaryHistoryInput(p, sent.ChatHistory)
				require.NoError(t, e)
				text := summaryInputText(input)
				require.NotContains(t, text, "ancient")
				require.NotContains(t, text, "encrypted_content")
				require.Contains(t, text, "recent answer")
				return llmcontracts.AgentResult{Output: "done"}, nil
			}), req)
			require.NoError(t, err)
			require.Equal(t, 1, calls)
		})
	}
}

func TestProviderSwitchPersistsOnlySuccessfulWindow(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		for _, outcome := range []string{"overflow", "auth", "cancel", "success", "newer_checkpoint"} {
			t.Run(string(provider)+"/"+outcome, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				stores := teststores.New(t).Stores
				svc := &LLMService{execRepo: stores.Executions}
				checkpoint := models.ChatCompactionCheckpoint{ScopeType: "chat_project", ScopeID: "window-test", ModelConfigID: "source", SourceExecutionID: "old", Strategy: "openai_responses", ProviderStateJSON: `[{"type":"compaction","encrypted_content":"preserve-me"}]`}
				require.NoError(t, stores.Executions.UpsertChatCompactionCheckpoint(ctx, checkpoint))
				before, err := stores.Executions.GetChatCompactionCheckpoint(ctx, "chat_project", "window-test")
				require.NoError(t, err)
				req := llmcontracts.AgentRequest{Ctx: ctx, ExecID: "current", TransportScope: "chat:project:window-test", Operation: llmcontracts.OperationStreaming, Followup: true, Message: "PENDING", Agent: models.LLMConfig{ID: "target", Provider: provider, Model: "test", ContextWindow: 200000}, ChatHistory: []models.Execution{{ID: "old", Status: models.ExecCompleted, PromptSent: "older question", Output: "older answer"}, {ID: "tail", Status: models.ExecCompleted, PromptSent: "recent question", Output: "recent answer"}}}
				calls := 0
				var accepted []models.Execution
				_, err = svc.callProviderWithTruncatedRecovery(providerAdapterFunc(func(sent llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
					calls++
					saved, e := stores.Executions.GetChatCompactionCheckpoint(ctx, "chat_project", "window-test")
					require.NoError(t, e)
					require.Equal(t, before, saved, "no attempt may persist its candidate window")
					switch outcome {
					case "auth":
						return llmcontracts.AgentResult{}, errors.New("unauthorized")
					case "cancel":
						cancel()
						return llmcontracts.AgentResult{}, context.Canceled
					case "overflow":
						return llmcontracts.AgentResult{}, errors.New("context length exceeded")
					}
					if calls == 1 {
						return llmcontracts.AgentResult{}, errors.New("context length exceeded")
					}
					accepted = sent.ChatHistory
					if outcome == "newer_checkpoint" {
						newer := checkpoint
						newer.SourceExecutionID = req.ExecID
						newer.ProviderStateJSON = `[{"type":"compaction","encrypted_content":"newer"}]`
						require.NoError(t, stores.Executions.UpsertChatCompactionCheckpoint(ctx, newer))
					}
					return llmcontracts.AgentResult{Output: "done"}, nil
				}), req, req)
				saved, e := stores.Executions.GetChatCompactionCheckpoint(context.Background(), "chat_project", "window-test")
				require.NoError(t, e)
				if outcome == "overflow" || outcome == "auth" || outcome == "cancel" {
					require.Error(t, err)
					require.Equal(t, before, saved)
					if outcome == "overflow" {
						require.Greater(t, calls, 1)
					} else {
						require.Equal(t, 1, calls)
					}
					return
				}
				require.NoError(t, err)
				require.Equal(t, 2, calls)
				if outcome == "newer_checkpoint" {
					require.Equal(t, req.ExecID, saved.SourceExecutionID)
					require.Contains(t, saved.ProviderStateJSON, "newer")
					return
				}
				require.Equal(t, "provider_recovery", saved.Strategy)
				require.Equal(t, "tail", saved.SourceExecutionID)
				require.Empty(t, saved.ProviderStateJSON)
				expectedJSON, e := json.Marshal(accepted)
				require.NoError(t, e)
				savedJSON, e := json.Marshal(saved.History)
				require.NoError(t, e)
				require.JSONEq(t, string(expectedJSON), string(savedJSON))
				restored := svc.restoreCompactionCheckpoint(req)
				restoredJSON, e := json.Marshal(restored.ChatHistory)
				require.NoError(t, e)
				require.JSONEq(t, string(expectedJSON), string(restoredJSON), "next followup must use the accepted window")
			})
		}
	}
}

func TestProviderSwitchNativeCompactionFallback(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "previously rejected"}[cached], func(t *testing.T) {
			ctx := context.Background()
			stores := teststores.New(t).Stores
			svc := &LLMService{execRepo: stores.Executions}
			agent := models.LLMConfig{ID: "switch-native-fallback", Provider: models.ProviderAnthropic, Model: "claude-opus-4-6", AuthMethod: models.AuthMethodAPIKey, ContextWindow: 200000}
			key := nativeCompactionSessionKey(agent)
			knownUnsupportedNativeCompaction.Delete(key)
			t.Cleanup(func() { knownUnsupportedNativeCompaction.Delete(key) })
			if cached {
				knownUnsupportedNativeCompaction.Store(key, true)
			}
			req := llmcontracts.AgentRequest{Ctx: ctx, ExecID: "current", TransportScope: "chat:project:switch-native-fallback", Operation: llmcontracts.OperationStreaming, Followup: true, Message: "continue", Agent: agent, ChatHistory: []models.Execution{{ID: "previous", Status: models.ExecCompleted, PromptSent: "earlier question", Output: "earlier answer", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"compaction","encrypted_content":"opaque"}]}`}}}}}
			calls := 0
			result, err := svc.callProviderWithCompaction(providerAdapterFunc(func(sent llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				calls++
				if !cached && calls == 1 {
					require.False(t, sent.Agent.DisableNativeCompaction)
					return llmcontracts.AgentResult{}, errors.New("unsupported context management compaction")
				}
				require.True(t, sent.Agent.DisableNativeCompaction)
				require.True(t, sent.DisableNativeCompaction)
				if !cached && calls == 2 {
					require.Equal(t, llmcontracts.OperationDirect, sent.Operation)
					require.NotEmpty(t, sent.SummaryInput)
					return llmcontracts.AgentResult{Output: "Conversation summary for continuation."}, nil
				}
				require.Equal(t, llmcontracts.OperationStreaming, sent.Operation)
				require.Equal(t, "continue", sent.Message)
				return llmcontracts.AgentResult{Output: "done"}, nil
			}), req)
			require.NoError(t, err)
			require.Equal(t, "done", result.Output)
			require.True(t, knownNativeCompactionUnsupported(agent))
			checkpoint, err := stores.Executions.GetChatCompactionCheckpoint(ctx, "chat_project", "switch-native-fallback")
			require.NoError(t, err)
			require.NotNil(t, checkpoint)
			require.Equal(t, "previous", checkpoint.SourceExecutionID)
			if cached {
				require.Equal(t, 1, calls)
				require.Equal(t, "provider_recovery", checkpoint.Strategy)
			} else {
				require.Equal(t, 3, calls)
				require.Equal(t, "local_summary", checkpoint.Strategy)
				require.Contains(t, checkpoint.Summary, "Conversation summary")
			}
		})
	}
}

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmopenai "github.com/openvibely/openvibely/internal/llm/openai"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"github.com/openvibely/openvibely/pkg/executionenv"
	"github.com/openvibely/openvibely/pkg/managedcontext"
	"github.com/stretchr/testify/require"
)

func TestSummaryReplacementPersistsContextAndUserMetadata(t *testing.T) {
	store := teststores.New(t)
	svc := NewLLMService(nil, store.Stores.Executions, nil, nil, nil, nil)
	ctx := managedcontext.With(context.Background(), "permitted index")
	ctx = attachmentcontext.With(ctx, "/uploads/report.txt")
	rawUser := `{"type":"message","id":"user-1","role":"user","content":[{"type":"input_text","text":"keep my request","annotations":[{"source":"reference"}]}],"internal_chat_message_metadata":{"example":"retained"}}`
	history := []models.Execution{{ID: "source", PromptSent: "keep my request", Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[` + rawUser + `]}`}}}}
	req := llmcontracts.AgentRequest{Ctx: ctx, Operation: llmcontracts.OperationStreaming, TransportScope: "task:parity", WorkDir: t.TempDir(), Agent: models.LLMConfig{Provider: models.ProviderOpenAI, ContextWindow: 200000}, ChatHistory: history}
	compacted, err := svc.compactRequestHistoryWithLocalSummary(providerAdapterFunc(func(llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		return llmcontracts.AgentResult{Output: "completed work"}, nil
	}), req)
	require.NoError(t, err)
	require.Len(t, compacted.ChatHistory, 5)
	for i, text := range []string{executionenv.Context(req.WorkDir), managedcontext.Text(ctx), attachmentcontext.Text(ctx)} {
		require.Equal(t, text, compacted.ChatHistory[i].PromptSent)
		require.Equal(t, "application", compacted.ChatHistory[i].ContextKind)
	}
	require.JSONEq(t, rawUser, string(compacted.ChatHistory[3].RetainedUserMessage))
	require.Equal(t, "summary", compacted.ChatHistory[4].ContextKind)
	require.Empty(t, compacted.ChatHistory[4].Output)
	svc.persistCompactionCheckpoint(req, compacted, "completed work", "local_summary")
	restored := svc.restoreCompactionCheckpoint(req)
	require.Equal(t, compacted.ChatHistory, restored.ChatHistory)
	hydrated, err := svc.loadProviderReplayHistory(restored)
	require.NoError(t, err)
	require.Equal(t, restored.ChatHistory, hydrated.ChatHistory)
	wire := llmopenai.HistoryInputItems(hydrated.ChatHistory)
	encoded, err := json.Marshal(wire)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"id":"user-1"`)
	require.Contains(t, string(encoded), `"annotations":[{"source":"reference"}]`)
	require.Contains(t, string(encoded), `"role":"user"`)

	// The second compaction retains actual user intent, not the previous summary
	// or stale application context. Current context is rebuilt exactly once.
	second := buildCompactedReplacementHistory(restored.ChatHistory, "new summary")
	require.Len(t, second, 2)
	second = restoreCompactedApplicationContext(restored, second)
	require.Len(t, second, 5)
	require.Equal(t, "keep my request", second[3].PromptSent)
	require.Contains(t, second[4].PromptSent, "new summary")
}

func TestSummaryReplacementCountsSavedContextOnce(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		t.Run(string(provider), func(t *testing.T) {
			ctx := managedcontext.With(context.Background(), "current index")
			ctx = attachmentcontext.With(ctx, "/uploads/file")
			req := llmcontracts.AgentRequest{Ctx: ctx, Agent: models.LLMConfig{Provider: provider, ContextWindow: 200000}, DisableTools: true, WorkDir: t.TempDir(), ChatHistory: buildCompactedReplacementHistory(nil, "summary")}
			before := calculateRequestBudget(req)
			req.ChatHistory = restoreCompactedApplicationContext(req, req.ChatHistory)
			after := calculateRequestBudget(req)
			// Message serialization has a small per-message cost on some providers.
			delta := after.TotalInputTokens() - before.TotalInputTokens()
			require.InDelta(t, 0, delta, 100)
			require.Less(t, after.FixedTokens, before.FixedTokens)
			input, err := summaryHistoryInput(provider, req.ChatHistory)
			require.NoError(t, err)
			for _, marker := range []string{executionenv.Marker, managedcontext.Marker, attachmentcontext.Marker} {
				// JSON escapes '<'; count decoded messages to avoid encoding assumptions.
				count := 0
				for _, raw := range input {
					var m struct{ Content string }
					if json.Unmarshal(raw, &m) == nil && strings.HasPrefix(m.Content, marker+"\n") {
						count++
					}
				}
				require.Equal(t, 1, count, marker)
			}
			var last struct{ Role, Content string }
			require.NoError(t, json.Unmarshal(input[len(input)-1], &last))
			require.Equal(t, "user", last.Role)
			require.True(t, llmprompt.IsCompactionContext(last.Content))
		})
	}
}

func TestSummaryReplacementPreservesOversizedSummaryAcrossProviders(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		t.Run(string(provider), func(t *testing.T) {
			req := llmcontracts.AgentRequest{Ctx: context.Background(), Agent: models.LLMConfig{Provider: provider, ContextWindow: 10000}, ChatHistory: []models.Execution{{PromptSent: "old", Output: "done", Status: models.ExecCompleted}}}
			summary := "ALREADY_COMPLETED " + strings.Repeat("large summary ", 10000)
			svc := &LLMService{}
			compacted, err := svc.compactRequestHistoryWithLocalSummary(providerAdapterFunc(func(llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				return llmcontracts.AgentResult{Output: summary}, nil
			}), req)
			require.NoError(t, err)
			require.Equal(t, strings.TrimSpace(summary), strings.TrimSpace(historyCompactionSummary(compacted.ChatHistory)))
			calls := 0
			_, err = svc.callCompactedRetry(providerAdapterFunc(func(llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				calls++
				return llmcontracts.AgentResult{}, nil
			}), req, compacted, errors.New("overflow"))
			require.True(t, recognizedContextLengthError(err))
			require.Zero(t, calls, "must not resume using pre-summary history")
		})
	}
}

func TestSummaryReturnsExhaustedClientFailureWithoutRetry(t *testing.T) {
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, ContextWindow: 20000}}
	history := []models.Execution{{PromptSent: "request", Output: "completed", Status: models.ExecCompleted}}
	calls := 0
	summary, err := (&LLMService{}).localSummaryCompaction(providerAdapterFunc(func(r llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		calls++
		return llmcontracts.AgentResult{}, io.ErrUnexpectedEOF
	}), req, history)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.True(t, llmcontracts.ErrorIs(err, llmcontracts.ErrorLocalCompactionFailed))
	require.Empty(t, summary)
	require.Equal(t, 1, calls, "the client owns transient retries")
	ctx, cancel := context.WithCancel(context.Background())
	req.Ctx = ctx
	calls = 0
	_, err = (&LLMService{}).localSummaryCompaction(providerAdapterFunc(func(llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		calls++
		cancel()
		return llmcontracts.AgentResult{}, io.ErrUnexpectedEOF
	}), req, history)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, calls)
}

func TestSummaryFailureStopsCompactionAcrossProviders(t *testing.T) {
	for _, agent := range []models.LLMConfig{
		{Provider: models.ProviderOpenAI, Model: "gpt-6-sol"},
		{Provider: models.ProviderAnthropic, Model: "claude-opus-5-5"},
		{Provider: models.ProviderOpenAICompatible, Model: "test"},
		{Provider: models.ProviderOllama, Model: "test"},
	} {
		agent.AuthMethod = models.AuthMethodAPIKey
		agent.ContextWindow = 200000
		for _, path := range []string{"proactive", "context_overflow", "native_failure"} {
			if path == "native_failure" && !providerSupportsNativeCompaction(agent) {
				continue
			}
			for _, failure := range []string{"exhausted_client_retries", "authentication", "empty_summary", "overflow_exhausted"} {
				t.Run(string(agent.Provider)+"/"+path+"/"+failure, func(t *testing.T) {
					req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Message: "continue", Agent: agent, ChatHistory: []models.Execution{{PromptSent: "request", Output: "completed", Status: models.ExecCompleted}}}
					original, err := json.Marshal(req.ChatHistory)
					require.NoError(t, err)
					wantGenerationCalls := 1
					if path == "proactive" {
						req.ContextTokenEstimate = 190000
						req.DisableNativeCompaction = true
						wantGenerationCalls = 0
					}
					generationCalls, summaryCalls, previousInputSize := 0, 0, 0
					cause := io.ErrUnexpectedEOF
					if failure == "authentication" {
						cause = errors.New("authentication failed")
					}
					_, err = (&LLMService{}).callProviderWithCompaction(providerAdapterFunc(func(r llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
						if r.Operation != llmcontracts.OperationDirect {
							generationCalls++
							require.LessOrEqual(t, generationCalls, wantGenerationCalls, "must not resume with truncated history")
							if path == "native_failure" {
								return llmcontracts.AgentResult{}, errors.New("pre-turn compaction: failed")
							}
							return llmcontracts.AgentResult{}, errors.New("context length exceeded")
						}
						summaryCalls++
						if failure == "overflow_exhausted" {
							if previousInputSize > 0 {
								require.Less(t, len(r.SummaryInput), previousInputSize)
							}
							previousInputSize = len(r.SummaryInput)
							return llmcontracts.AgentResult{}, errors.New("context length exceeded")
						}
						if failure == "empty_summary" {
							return llmcontracts.AgentResult{}, nil
						}
						return llmcontracts.AgentResult{}, cause
					}), req)
					if failure == "overflow_exhausted" {
						require.True(t, llmcontracts.ErrorIs(err, llmcontracts.ErrorCompactionInputInfeasible), "%v", err)
						require.Greater(t, summaryCalls, 1)
					} else {
						require.True(t, llmcontracts.ErrorIs(err, llmcontracts.ErrorLocalCompactionFailed), "%v", err)
						require.Equal(t, 1, summaryCalls, "the client owns transient retries")
						if failure != "empty_summary" {
							require.ErrorIs(t, err, cause)
						}
					}
					require.Equal(t, wantGenerationCalls, generationCalls)
					after, err := json.Marshal(req.ChatHistory)
					require.NoError(t, err)
					require.JSONEq(t, string(original), string(after))
				})
			}
		}
	}
}

func TestSummaryRestoredContextRefreshesOrKeepsSavedReferences(t *testing.T) {
	ctx := managedcontext.With(context.Background(), "old index")
	ctx = attachmentcontext.With(ctx, "/uploads/file")
	req := llmcontracts.AgentRequest{Ctx: ctx}
	history := restoreCompactedApplicationContext(req, buildCompactedReplacementHistory(nil, "summary"))
	req.Ctx = context.Background()
	restored := restoreCompactedApplicationContext(req, history)
	require.Equal(t, history, restored, "missing sources must not erase saved references")
	req.Ctx = managedcontext.With(req.Ctx, "")
	restored = restoreCompactedApplicationContext(req, history)
	text := summaryHistoryText(t, models.ProviderOpenAICompatible, restored)
	require.NotContains(t, text, "old index")
	require.Contains(t, text, "/uploads/file")
	req.Ctx = managedcontext.With(req.Ctx, "updated index")
	restored = restoreCompactedApplicationContext(req, history)
	text = summaryHistoryText(t, models.ProviderOpenAICompatible, restored)
	require.NotContains(t, text, "old index")
	require.Contains(t, text, "updated index")
	require.Contains(t, text, "/uploads/file")
}

func TestSummaryTruncatedUserMessageKeepsIdentity(t *testing.T) {
	input := `{"responses_input":[{"id":"source-message","type":"message","role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("keep this text ", 100) + `","annotations":[{"start":100,"end":500}]}]}]}`
	retained := retainedUserMessageHistory([]models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: input}}}}, 40)
	require.Len(t, retained, 1)
	var wire struct {
		ID      string
		Content []struct {
			Text        string
			Annotations json.RawMessage
		}
	}
	require.NoError(t, json.Unmarshal(retained[0].RetainedUserMessage, &wire))
	require.Equal(t, "source-message", wire.ID)
	require.Equal(t, retained[0].PromptSent, wire.Content[0].Text)
	require.Empty(t, wire.Content[0].Annotations)
	require.LessOrEqual(t, estimatedUTF8Tokens(wire.Content[0].Text), 40)
}

func TestRetainedUserSteeringProviderMatrix(t *testing.T) {
	fixtures := map[string]string{
		"openai":            `{"responses_input":[{"type":"message","role":"user","content":"START_WORK"},{"type":"function_call","call_id":"a","name":"read_file","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"TOOL_EVIDENCE"},{"type":"message","role":"user","content":[{"type":"input_text","text":"DO_NOT_CHANGE_DATABASE"},{"type":"input_image","image_url":"data:image/png;base64,IMAGE_DATA"}]}]}`,
		"openai_compatible": `[{"role":"user","content":"START_WORK"},{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"read_file","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":"TOOL_EVIDENCE"},{"role":"user","content":[{"type":"text","text":"DO_NOT_CHANGE_DATABASE"},{"type":"image_url","image_url":{"url":"data:image/png;base64,IMAGE_DATA"}}]}]`,
		"anthropic_text":    `{"messages":[{"role":"user","content":"START_WORK"},{"role":"assistant","content":"working"},{"role":"user","content":"DO_NOT_CHANGE_DATABASE"}]}`,
		"anthropic_blocks":  `{"messages":[{"role":"user","content":"START_WORK"},{"role":"assistant","content_blocks":[{"type":"tool_use","id":"a","name":"read_file","input":{}}]},{"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"a","content":"TOOL_EVIDENCE"},{"type":"text","text":"DO_NOT_CHANGE_DATABASE"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"IMAGE_DATA"}}]}]}`,
	}
	for name, raw := range fixtures {
		t.Run(name, func(t *testing.T) {
			history := []models.Execution{{ID: "source", Status: models.ExecCompleted, PromptSent: "START_WORK", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: raw}}}}
			before, err := json.Marshal(history)
			require.NoError(t, err)
			replacement := buildCompactedReplacementHistory(history, "Summary deliberately omits steering")
			encoded, err := json.Marshal(replacement)
			require.NoError(t, err)
			require.Contains(t, string(encoded), "START_WORK")
			require.Contains(t, string(encoded), "DO_NOT_CHANGE_DATABASE")
			require.NotContains(t, string(encoded), "TOOL_EVIDENCE")
			require.NotContains(t, string(encoded), "IMAGE_DATA")
			after, err := json.Marshal(history)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "retention must not mutate saved replay")
		})
	}
}

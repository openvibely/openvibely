package service

import (
	"context"
	"errors"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
)

// Exercise the service boundary with the same recovery snapshots the clients
// publish after executing tools. Client HTTP/WebSocket tests separately exercise
// native compaction and transport retries; this matrix tests fallback decisions.
func TestCompactionFlowMatrix(t *testing.T) {
	for _, agent := range []models.LLMConfig{
		{Provider: models.ProviderOpenAI, Model: "gpt-6-sol"},
		{Provider: models.ProviderAnthropic, Model: "claude-opus-5-5"},
		{Provider: models.ProviderOpenAICompatible, Model: "test"},
	} {
		agent.AuthMethod = models.AuthMethodAPIKey
		agent.ContextWindow = 200000
		require.Equal(t, agent.Provider != models.ProviderOpenAICompatible, providerSupportsNativeCompaction(agent), "fixture must exercise the intended native capability")
		for _, path := range []string{"preturn_summary", "preturn_overflow", "preturn_native_failure", "inturn_overflow", "inturn_native_failure"} {
			native := providerSupportsNativeCompaction(agent)
			if path == "inturn_native_failure" && agent.Provider == models.ProviderOpenAI {
				continue
			}
			if (path == "preturn_native_failure" || path == "inturn_native_failure") && !native {
				continue
			}
			for _, outcome := range []string{"success", "summary_overflow_then_success", "summary_retries_exhausted", "empty_summary", "summary_overflow_exhausted", "continuation_overflow"} {
				t.Run(string(agent.Provider)+"/"+path+"/"+outcome, func(t *testing.T) {
					req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, ExecID: "active", Message: "CURRENT_REQUEST", Agent: agent, ChatHistory: []models.Execution{{PromptSent: "OLD_REQUEST", Output: "OLD_ANSWER", Status: models.ExecCompleted}}}
					proactive := path == "preturn_summary"
					inturn := path == "inturn_overflow" || path == "inturn_native_failure"
					if proactive {
						req.ContextTokenEstimate = 190000
						req.DisableNativeCompaction = true
					}
					overflow := errors.New("context length exceeded")
					exhausted := errors.New("transport retries exhausted")
					var events []string
					summaries, generations, previousSize := 0, 0, 0
					_, err := (&LLMService{}).callProviderWithCompaction(providerAdapterFunc(func(r llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
						require.Less(t, len(events), 20, "fallback must terminate")
						if len(r.SummaryInput) > 0 {
							events = append(events, "summary")
							summaries++
							require.True(t, r.DisableTools)
							require.True(t, r.DisableNativeCompaction)
							require.Empty(t, r.ExecID)
							if summaries == 1 && inturn {
								require.Contains(t, summaryInputText(r.SummaryInput), "TOOL_FINISHED")
								require.Contains(t, summaryInputText(r.SummaryInput), "write_file")
							}
							if summaries > 1 {
								require.Less(t, len(r.SummaryInput), previousSize, "overflow must shrink input")
							}
							previousSize = len(r.SummaryInput)
							switch outcome {
							case "summary_retries_exhausted":
								return llmcontracts.AgentResult{}, exhausted
							case "empty_summary":
								return llmcontracts.AgentResult{}, nil
							case "summary_overflow_exhausted":
								return llmcontracts.AgentResult{}, overflow
							case "summary_overflow_then_success":
								if summaries == 1 {
									return llmcontracts.AgentResult{}, overflow
								}
							}
							return llmcontracts.AgentResult{Output: "SUMMARY: TOOL_FINISHED; do not repeat"}, nil
						}
						generations++
						if !proactive && generations == 1 {
							events = append(events, "initial")
							cause := error(overflow)
							if path == "preturn_native_failure" || path == "inturn_native_failure" {
								cause = llmcontracts.NewCategorizedError(llmcontracts.ErrorNativeCompactionFailed, "native compaction", errors.New("failed"))
								if inturn {
									cause = llmcontracts.NewCategorizedError(llmcontracts.ErrorNativeCompactionFailed, "Anthropic context management", errors.New("failed"))
								}
							}
							if inturn {
								return llmcontracts.AgentResult{}, matrixInturnFailure(r, cause)
							}
							return llmcontracts.AgentResult{}, cause
						}
						events = append(events, "continuation")
						require.Contains(t, historyCompactionSummary(r.ChatHistory), "TOOL_FINISHED")
						if inturn {
							require.Empty(t, r.Message, "consumed prompt must not be submitted again")
							require.True(t, llmcontracts.HistoryContinuationFromContext(r.Ctx))
						} else {
							require.Equal(t, "CURRENT_REQUEST", r.Message)
						}
						if native && !proactive {
							require.True(t, r.DisableNativeCompaction)
						}
						if outcome == "continuation_overflow" {
							return llmcontracts.AgentResult{}, overflow
						}
						return llmcontracts.AgentResult{Output: "done"}, nil
					}), req)
					want := []string{}
					if !proactive {
						want = append(want, "initial")
					}
					for i := 0; i < summaries; i++ {
						want = append(want, "summary")
					}
					switch outcome {
					case "success", "summary_overflow_then_success", "continuation_overflow":
						want = append(want, "continuation")
						if outcome == "continuation_overflow" {
							require.Error(t, err)
							require.True(t, recognizedContextLengthError(err))
						} else {
							require.NoError(t, err)
						}
					case "summary_retries_exhausted":
						require.ErrorIs(t, err, exhausted)
					case "empty_summary":
						require.True(t, llmcontracts.ErrorIs(err, llmcontracts.ErrorLocalCompactionFailed), "%v", err)
					case "summary_overflow_exhausted":
						require.True(t, llmcontracts.ErrorIs(err, llmcontracts.ErrorCompactionInputInfeasible), "%v", err)
					}
					require.Equal(t, want, events)
					switch outcome {
					case "summary_overflow_then_success":
						require.Equal(t, 2, summaries)
					case "summary_overflow_exhausted":
						require.Greater(t, summaries, 1)
					default:
						require.Equal(t, 1, summaries)
					}
				})
			}
		}
	}
}

func matrixInturnFailure(req llmcontracts.AgentRequest, cause error) error {
	switch req.Agent.Provider {
	case models.ProviderOpenAI:
		llmcontracts.RecordResponsesRecovery(req.Ctx, `{"responses_input":[{"type":"message","role":"user","content":"CURRENT_REQUEST"},{"type":"function_call","call_id":"write","name":"write_file","arguments":"{}"},{"type":"function_call_output","call_id":"write","output":"TOOL_FINISHED"}]}`)
		return cause
	case models.ProviderAnthropic:
		return &anthropicclient.ConversationError{Err: cause, MessagesJSON: `[{"role":"user","content":"CURRENT_REQUEST"},{"role":"assistant","content":[{"type":"tool_use","id":"write","name":"write_file","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"write","content":"TOOL_FINISHED"}]}]`}
	default:
		return &openaiclient.ConversationError{Err: cause, MessagesJSON: `[{"role":"user","content":"CURRENT_REQUEST"},{"role":"assistant","tool_calls":[{"id":"write","type":"function","function":{"name":"write_file","arguments":"{}"}}]},{"role":"tool","tool_call_id":"write","content":"TOOL_FINISHED"}]`}
	}
}

// OpenAI deliberately returns this terminal error after its own mid-turn
// compaction retries. It must not restart the turn through summary fallback.
func TestCompactionFlowMatrixOpenAIMidturnTerminal(t *testing.T) {
	for _, cause := range []error{errors.New("context length exceeded"), errors.New("transport retries exhausted"), errors.New("unsupported compaction")} {
		t.Run(cause.Error(), func(t *testing.T) {
			terminal := llmcontracts.NewCategorizedError(llmcontracts.ErrorMidTurnCompactionFailed, "OpenAI mid-turn compaction", cause)
			calls := 0
			_, err := (&LLMService{}).callProviderWithCompaction(providerAdapterFunc(func(r llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				calls++
				require.Empty(t, r.SummaryInput)
				return llmcontracts.AgentResult{}, matrixInturnFailure(r, terminal)
			}), llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, ExecID: "active", Message: "CURRENT_REQUEST", Agent: models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-6-sol", AuthMethod: models.AuthMethodAPIKey, ContextWindow: 200000}, ChatHistory: []models.Execution{{PromptSent: "old", Output: "done", Status: models.ExecCompleted}}})
			require.ErrorIs(t, err, cause)
			require.True(t, llmcontracts.ErrorIs(err, llmcontracts.ErrorMidTurnCompactionFailed))
			require.Equal(t, 1, calls, "terminal in-turn failure must not invoke summary or repeat generation")
		})
	}
}

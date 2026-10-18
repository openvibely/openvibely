package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"github.com/openvibely/openvibely/pkg/managedcontext"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
)

func summaryInputText(input []json.RawMessage) string {
	raw, _ := json.Marshal(input)
	return string(raw)
}

func summaryHistoryText(t *testing.T, provider models.LLMProvider, history []models.Execution) string {
	t.Helper()
	input, err := summaryHistoryInput(provider, history)
	require.NoError(t, err)
	return summaryInputText(input)
}

func TestSummaryRemovesWholeLinkedMessages(t *testing.T) {
	for name, raw := range map[string]string{
		"responses":                  `[{"type":"function_call","call_id":"a"},{"type":"message","role":"user","content":"keep"},{"type":"function_call_output","call_id":"a","output":"done"}]`,
		"completions parallel calls": `[{"role":"assistant","tool_calls":[{"id":"a"},{"id":"b"}]},{"role":"tool","tool_call_id":"a","content":"done"},{"role":"user","content":"keep"},{"role":"tool","tool_call_id":"b","content":"done"}]`,
		"anthropic connected blocks": `[{"role":"assistant","content_blocks":[{"type":"tool_use","id":"a"}]},{"role":"assistant","content_blocks":[{"type":"tool_use","id":"b"}]},{"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"a"},{"type":"tool_result","tool_use_id":"b"}]},{"role":"user","content":"keep"}]`,
		"plain message":              `[{"role":"user","content":"old"},{"role":"user","content":"keep"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			var input []json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(raw), &input))
			before := summaryInputText(input)
			remaining := removeOldestSummaryMessage(input)
			require.Len(t, remaining, 1)
			require.JSONEq(t, `{"role":"user","content":"keep"}`, strings.ReplaceAll(string(remaining[0]), `"type":"message",`, ""))
			require.Equal(t, before, summaryInputText(input), "retries must not mutate durable history")
		})
	}
}

func TestSummaryRequestBudgetsInjectedContextAcrossProviders(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		t.Run(string(provider), func(t *testing.T) {
			req := llmcontracts.AgentRequest{Ctx: context.Background(), RawDirectPrompt: true, DisableTools: true, Agent: models.LLMConfig{Provider: provider, Model: "claude-opus-5-5", ContextWindow: 20000}}
			history := []models.Execution{{PromptSent: strings.Repeat("old ", 1000), Output: "recent completed work " + strings.Repeat("x", 40000), Status: models.ExecCompleted}}
			// History and the summary instruction fit on their own. Inject enough
			// context to overflow by just 100 tokens: the original failure case.
			probe := req
			probe.Message = localContextCompactionInstruction
			var err error
			probe.SummaryInput, err = summaryHistoryInput(provider, history)
			require.NoError(t, err)
			budget := calculateRequestBudget(probe)
			require.NoError(t, ensureRequestFitsWithBudget(probe, budget, "without injected context"))
			guidanceTokens := budget.SafeInputLimit - budget.TotalInputTokens() + 100
			req.Ctx = managedcontext.With(req.Ctx, strings.Repeat("x", guidanceTokens*4))
			req.Ctx = attachmentcontext.With(req.Ctx, "/uploads/report.txt")
			probe.Ctx = req.Ctx
			require.Error(t, ensureRequestFitsWithBudget(probe, calculateRequestBudget(probe), "with injected context"))
			calls := 0
			result, err := (&LLMService{}).localSummaryCompaction(providerAdapterFunc(func(r llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				calls++
				require.Equal(t, localContextCompactionInstruction, r.Message)
				require.Empty(t, r.ChatHistory)
				require.Len(t, r.SummaryInput, 1)
				require.Contains(t, summaryInputText(r.SummaryInput), "recent completed work")
				require.NotEmpty(t, managedcontext.Text(r.Ctx))
				require.NotEmpty(t, attachmentcontext.Text(r.Ctx))
				budget := calculateRequestBudget(r)
				require.Positive(t, budget.FixedTokens)
				require.NoError(t, ensureRequestFitsWithBudget(r, budget, "test"))
				require.True(t, r.DisableTools)
				require.True(t, r.DisableNativeCompaction)
				return llmcontracts.AgentResult{Output: "summary"}, nil
			}), req, history)
			require.NoError(t, err)
			require.Equal(t, "summary", result)
			require.Equal(t, 1, calls)
		})
	}
}

func TestSummaryOverflowRetriesMessagesWithinOneExecution(t *testing.T) {
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Agent: models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex"}, ChatHistory: []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"function_call","call_id":"a","name":"read_file","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"old result"},{"type":"message","role":"user","content":"retain this instruction"},{"type":"message","role":"assistant","content":"recent work"}]}`}}}}}
	calls := 0
	result, err := (&LLMService{}).compactRequestHistoryWithLocalSummary(providerAdapterFunc(func(r llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		calls++
		if calls == 1 {
			require.Len(t, r.SummaryInput, 4)
			return llmcontracts.AgentResult{}, llmcontracts.NewCategorizedError(llmcontracts.ErrorContextWindowExceeded, "test", errors.New("overflow"))
		}
		require.Equal(t, 2, calls)
		require.Len(t, r.SummaryInput, 2)
		require.NotContains(t, summaryInputText(r.SummaryInput), "call_id")
		return llmcontracts.AgentResult{Output: "recent work summary"}, nil
	}), req)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, "retain this instruction", result.ChatHistory[0].PromptSent)
	require.Contains(t, result.ChatHistory[len(result.ChatHistory)-1].Output, "recent work summary")
}

func TestSummaryExhaustionAndNonContextErrors(t *testing.T) {
	req := llmcontracts.AgentRequest{Ctx: context.Background(), Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, ContextWindow: 20000}}
	history := []models.Execution{{PromptSent: "one", Output: "two", Status: models.ExecCompleted}}
	for _, overflow := range []bool{true, false} {
		calls := 0
		failure := errors.New("authentication failed")
		if overflow {
			failure = llmcontracts.NewCategorizedError(llmcontracts.ErrorContextWindowExceeded, "test", errors.New("overflow"))
		}
		_, err := (&LLMService{}).localSummaryCompaction(providerAdapterFunc(func(r llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
			calls++
			require.NotEmpty(t, r.SummaryInput)
			return llmcontracts.AgentResult{}, failure
		}), req, history)
		require.ErrorIs(t, err, failure)
		if overflow {
			require.Equal(t, 2, calls)
		} else {
			require.Equal(t, 1, calls)
		}
	}
	req.Ctx = managedcontext.With(req.Ctx, strings.Repeat("fixed ", 20000))
	_, err := (&LLMService{}).localSummaryCompaction(providerAdapterFunc(func(llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		t.Fatal("fixed context alone cannot fit; do not send")
		return llmcontracts.AgentResult{}, nil
	}), req, history)
	require.True(t, llmcontracts.ErrorIs(err, llmcontracts.ErrorCompactionInputInfeasible))
}

func TestSummaryStructuredHistoryReachesProviderWire(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	for _, route := range []struct {
		provider models.LLMProvider
		auth     models.AuthMethod
	}{
		{models.ProviderOpenAI, models.AuthMethodAPIKey},
		{models.ProviderOpenAI, models.AuthMethodOAuth},
		{models.ProviderAnthropic, models.AuthMethodAPIKey},
		{models.ProviderOpenAICompatible, models.AuthMethodAPIKey},
		{models.ProviderOllama, models.AuthMethodAPIKey},
	} {
		provider := route.provider
		for _, operation := range []llmcontracts.Operation{llmcontracts.OperationTask, llmcontracts.OperationStreaming} {
			t.Run(string(provider)+"/"+string(route.auth)+"/"+string(operation), func(t *testing.T) {
				var payload map[string]any
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Upgrade") != "" {
						http.Error(w, "HTTP only", http.StatusUpgradeRequired)
						return
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
						http.Error(w, "bad request", 400)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					switch provider {
					case models.ProviderOpenAI:
						response := `{"id":"summary","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"summary done"}]}]}`
						if payload["stream"] == true {
							fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
						} else {
							w.Header().Set("Content-Type", "application/json")
							fmt.Fprint(w, response)
						}
					case models.ProviderAnthropic:
						for _, event := range []string{
							`{"type":"message_start","message":{"id":"summary","usage":{"input_tokens":10}}}`,
							`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
							`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"summary done"}}`,
							`{"type":"content_block_stop","index":0}`,
							`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
							`{"type":"message_stop"}`,
						} {
							fmt.Fprintf(w, "data: %s\n\n", event)
						}
					default:
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"summary done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					}
				}))
				defer server.Close()
				oldOpenAI, oldAnthropic, oldOAuth := openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost, openaiclient.OpenAIChatGPTAPIBaseURL
				openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost, openaiclient.OpenAIChatGPTAPIBaseURL = server.URL+"/v1/", server.URL, server.URL
				defer func() {
					openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost, openaiclient.OpenAIChatGPTAPIBaseURL = oldOpenAI, oldAnthropic, oldOAuth
				}()
				model := "custom"
				replay := `[{"role":"user","content":"inspect file"},{"role":"assistant","tool_calls":[{"id":"read1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},{"role":"tool","tool_call_id":"read1","content":"FILE_CONTENT"},{"role":"assistant","content":"file inspected"}]`
				if provider == models.ProviderOpenAI {
					model = "gpt-5.3-codex"
					if route.auth == models.AuthMethodOAuth {
						model = "gpt-6-sol"
					}
					replay = `{"responses_input":[{"type":"message","role":"user","content":"inspect file"},{"type":"function_call","call_id":"read1","name":"read_file","arguments":"{}"},{"type":"function_call_output","call_id":"read1","output":"FILE_CONTENT"},{"type":"message","role":"assistant","content":"file inspected"}]}`
				}
				if provider == models.ProviderAnthropic {
					model = "claude-opus-5-5"
					replay = `{"messages":[{"role":"user","content":"inspect file"},{"role":"assistant","content_blocks":[{"type":"tool_use","id":"read1","name":"read_file","input":{}}]},{"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"read1","content":"FILE_CONTENT"}]},{"role":"assistant","content":"file inspected"}]}`
				}
				ctx := managedcontext.With(context.Background(), "CURRENT_SKILL_INDEX")
				ctx = attachmentcontext.With(ctx, "/uploads/current.txt")
				req := llmcontracts.AgentRequest{Ctx: ctx, Operation: operation, ChatMode: models.ChatModeOrchestrate, ProjectInstructions: "PROJECT_RULE", ChatSystemContext: "CHAT_RULE", AgentDefinition: &models.Agent{SystemPrompt: "AGENT_RULE"}, Agent: models.LLMConfig{Provider: provider, Model: model, AuthMethod: route.auth, APIKey: "fixture", OAuthAccessToken: "fixture", OAuthExpiresAt: time.Now().Add(24 * time.Hour).UnixMilli(), BaseURL: server.URL + "/v1", PresetSlug: "vllm", OllamaBaseURL: server.URL, ContextWindow: 200000}}
				svc := NewLLMService(nil, nil, nil, nil, nil, nil)
				summary, err := svc.localSummaryCompaction(svc.providerAdapters[provider], req, []models.Execution{{Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: replay}}}})
				require.NoError(t, err)
				require.Equal(t, "summary done", summary)
				require.Empty(t, payload["tools"])
				require.Empty(t, payload["context_management"])
				key := "messages"
				if provider == models.ProviderOpenAI {
					key = "input"
				}
				messages := payload[key].([]any)
				var system any
				switch provider {
				case models.ProviderOpenAI:
					system = payload["instructions"]
					// Responses Lite moves instructions into a developer input message.
					if system == nil {
						for _, raw := range messages {
							message := raw.(map[string]any)
							if message["role"] == "developer" && message["type"] == "message" {
								system = message["content"]
								break
							}
						}
					}
				case models.ProviderAnthropic:
					system = payload["system"]
				default:
					for _, raw := range messages {
						message := raw.(map[string]any)
						if message["role"] == "system" || message["role"] == "developer" {
							system = message["content"]
							break
						}
					}
				}
				encodedSystem, err := json.Marshal(system)
				require.NoError(t, err)
				require.Contains(t, string(encodedSystem), "AGENT_RULE")
				rule := "PROJECT_RULE"
				if operation == llmcontracts.OperationStreaming {
					rule = "CHAT_RULE"
				}
				require.Contains(t, string(encodedSystem), rule)
				require.NotContains(t, string(encodedSystem), "CONTEXT CHECKPOINT COMPACTION")

				require.GreaterOrEqual(t, len(messages), 5)
				raw, err := json.Marshal(messages)
				require.NoError(t, err)
				for _, text := range []string{"read1", "FILE_CONTENT", "CURRENT_SKILL_INDEX", "/uploads/current.txt"} {
					require.Contains(t, string(raw), text)
				}
				// The instruction is a separate last user message, not a rendered transcript.
				last, _ := json.Marshal(messages[len(messages)-1])
				require.Contains(t, string(last), "CONTEXT CHECKPOINT COMPACTION")
				require.NotContains(t, string(last), "FILE_CONTENT")
				require.NotContains(t, string(last), "read1")
			})
		}
	}
}

func TestSummaryImageBudgetDoesNotCountBase64AsText(t *testing.T) {
	for provider, template := range map[models.LLMProvider]string{
		models.ProviderAnthropic:        `[{"role":"user","content_blocks":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"%s"}}]}]`,
		models.ProviderOpenAICompatible: `[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,%s"}}]}]`,
		models.ProviderOllama:           `[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,%s"}}]}]`,
	} {
		t.Run(string(provider), func(t *testing.T) {
			var input []json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(template, strings.Repeat("a", 100000))), &input))
			tokens := estimateSummaryInputTokens(provider, input)
			require.GreaterOrEqual(t, tokens, 2000)
			require.Less(t, tokens, 2100)
		})
	}
}

func TestSummaryKeepsSystemInstructionsWhenTrimmingHistory(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		t.Run(string(provider), func(t *testing.T) {
			// A direct lifecycle request uses these exact system instructions, making
			// the budget boundary independent of changes to built-in prompt wording.
			instructions := "KEEP_SYSTEM_RULE " + strings.Repeat("x", 20000)
			req := llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationDirect, LifecycleHookCall: true, ProjectInstructions: instructions, Agent: models.LLMConfig{Provider: provider, ContextWindow: 20000}}
			history := []models.Execution{{PromptSent: strings.Repeat("old ", 11000), Output: "recent work", Status: models.ExecCompleted}}
			calls := 0
			_, err := (&LLMService{}).localSummaryCompaction(providerAdapterFunc(func(r llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				calls++
				preserved, ok := llmcontracts.SummarySystemPromptFromContext(r.Ctx)
				require.True(t, ok)
				require.Equal(t, instructions, preserved)
				require.Equal(t, estimatedUTF8Tokens(instructions), calculateRequestBudget(r).FixedTokens)
				require.Len(t, r.SummaryInput, 1)
				require.Contains(t, summaryInputText(r.SummaryInput), "recent work")
				require.NoError(t, ensureRequestFitsWithBudget(r, calculateRequestBudget(r), "summary with system instructions"))
				return llmcontracts.AgentResult{Output: "summary"}, nil
			}), req, history)
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, instructions, req.ProjectInstructions)
			_, overridden := llmcontracts.SummarySystemPromptFromContext(req.Ctx)
			require.False(t, overridden, "summary overrides must not leak into the continuing request")
		})
	}
}

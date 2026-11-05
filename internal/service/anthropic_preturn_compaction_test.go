package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"github.com/openvibely/openvibely/pkg/managedcontext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnthropicPreturnNativeThenSummaryFallback(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "unsupported", "no_summary", "null_summary", "empty_summary", "summary_failure"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := compactionTestContext()
			calls := []string{}
			const transcript = `{"messages":[{"role":"user","content":"OLD_QUESTION"},{"role":"assistant","content_blocks":[{"type":"compaction","content":"PREVIOUS_NATIVE_FACT","encrypted_content":"opaque"},{"type":"text","text":"OLD_ANSWER"}]}]}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				text := string(raw)
				var request struct {
					Messages []struct {
						Role    string          `json:"role"`
						Content json.RawMessage `json:"content"`
					} `json:"messages"`
					ContextManagement *struct {
						Edits []struct {
							Pause bool `json:"pause_after_compaction"`
						} `json:"edits"`
					} `json:"context_management"`
				}
				assert.NoError(t, json.Unmarshal(raw, &request))
				native := request.ContextManagement != nil && len(request.ContextManagement.Edits) > 0 && request.ContextManagement.Edits[0].Pause
				if native {
					assert.Contains(t, text, `"tool_choice":{"type":"none"}`)
					calls = append(calls, "native")
					assert.Equal(t, []string{"native"}, calls)
					assert.NotContains(t, text, "PENDING_FOLLOWUP")
					assert.Contains(t, text, "PREVIOUS_NATIVE_FACT")
					assert.Contains(t, r.Header.Get("anthropic-beta"), anthropicclient.CompactionBetaHeader)
					switch outcome {
					case "failure":
						compactionServerError(w, 400, "invalid_request_error", "native compaction failed")
					case "unsupported":
						compactionServerError(w, 400, "unsupported_beta", "compaction unsupported")
					case "no_summary":
						compactionServerReply(w, models.ProviderAnthropic, "Not compacted", "")
					default:
						summary := `"FRESH_NATIVE_SUMMARY"`
						if outcome == "null_summary" || outcome == "summary_failure" {
							summary = "null"
						}
						if outcome == "empty_summary" {
							summary = `" "`
						}
						w.Header().Set("Content-Type", "text/event-stream")
						for _, event := range []string{
							`{"type":"message_start","message":{"id":"native","usage":{"input_tokens":100,"cache_creation_input_tokens":7,"cache_read_input_tokens":9}}}`,
							`{"type":"content_block_start","index":0,"content_block":{"type":"compaction","content":` + summary + `}}`,
							`{"type":"content_block_stop","index":0}`,
							`{"type":"message_delta","delta":{"stop_reason":"compaction"},"usage":{"output_tokens":10}}`,
							`{"type":"message_stop"}`,
						} {
							fmt.Fprintf(w, "data: %s\n\n", event)
						}
					}
					return
				}
				if strings.Contains(text, "CONTEXT CHECKPOINT COMPACTION") {
					calls = append(calls, "summary")
					assert.Equal(t, []string{"native", "summary"}, calls)
					assert.Nil(t, request.ContextManagement)
					assert.NotContains(t, text, `"type":"compaction"`)
					assert.NotContains(t, text, "opaque")
					found := false
					for _, message := range request.Messages {
						if strings.Contains(string(message.Content), "PREVIOUS_NATIVE_FACT") {
							found = true
							assert.Equal(t, "user", message.Role)
							assert.Contains(t, string(message.Content), "Another language model started")
						}
						if strings.Contains(string(message.Content), "OLD_ANSWER") {
							assert.Equal(t, "assistant", message.Role)
						}
					}
					assert.True(t, found)

					assert.Contains(t, text, "PREVIOUS_NATIVE_FACT")
					assert.NotContains(t, text, "PENDING_FOLLOWUP")
					if outcome == "summary_failure" {
						compactionServerError(w, 400, "invalid_request_error", "summary rejected")
					} else {
						compactionServerReply(w, models.ProviderAnthropic, "FALLBACK_SUMMARY", "")
					}
					return
				}
				calls = append(calls, "turn")
				assert.Contains(t, text, "PENDING_FOLLOWUP")
				if outcome == "success" {
					assert.Contains(t, text, "FRESH_NATIVE_SUMMARY")
					assert.Contains(t, text, `"type":"compaction"`)
					assert.NotContains(t, text, "OLD_QUESTION")
				} else {
					assert.Contains(t, text, "FALLBACK_SUMMARY")
					assert.NotContains(t, text, `"type":"compaction"`)
				}
				compactionServerReply(w, models.ProviderAnthropic, "Done", "")
			}))
			defer server.Close()
			originalHost := anthropicclient.AnthropicAPIHost
			anthropicclient.AnthropicAPIHost = server.URL
			defer func() { anthropicclient.AnthropicAPIHost = originalHost }()
			history := []models.Execution{{Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: transcript}}}}
			svc := NewLLMService(nil, nil, nil, nil, nil, nil)
			req := llmcontracts.AgentRequest{Ctx: ctx, Operation: llmcontracts.OperationStreaming, Followup: true, Message: "PENDING_FOLLOWUP", WorkDir: t.TempDir(), ContextTokenEstimate: 190000, ChatHistory: history,
				Agent: models.LLMConfig{ID: "preturn-" + outcome, Provider: models.ProviderAnthropic, Model: "claude-sonnet-4-6", AuthMethod: models.AuthMethodAPIKey, APIKey: "test-" + outcome, ContextWindow: 200000}}
			t.Cleanup(func() { knownUnsupportedNativeCompaction.Delete(nativeCompactionSessionKey(req.Agent)) })
			result, err := svc.callProviderWithCompaction(svc.providerAdapters[models.ProviderAnthropic], req)
			if outcome == "summary_failure" {
				require.Error(t, err)
				require.Equal(t, []string{"native", "summary"}, calls)
				require.Equal(t, 110, result.Usage.TotalTokens)
				require.Equal(t, 16, result.Usage.CachedInputTokens)
				return
			}
			require.NoError(t, err)
			require.Contains(t, result.Output, "Done")
			if outcome == "success" {
				require.Equal(t, []string{"native", "turn"}, calls)
				require.Contains(t, result.NativeCompactionStateJSON, "FRESH_NATIVE_SUMMARY")
				require.GreaterOrEqual(t, result.Usage.TotalTokens, 115)
			} else {
				require.Equal(t, []string{"native", "summary", "turn"}, calls)
				switch outcome {
				case "no_summary":
					require.Equal(t, 50, result.Usage.TotalTokens)
				case "null_summary", "empty_summary":
					require.Equal(t, 120, result.Usage.InputTokens)
					require.Equal(t, 15, result.Usage.OutputTokens)
					require.Equal(t, 135, result.Usage.TotalTokens)
					require.Equal(t, 16, result.Usage.CachedInputTokens)
					require.Equal(t, 7, result.Usage.ProviderRaw["cache_creation_input_tokens"])
					require.Equal(t, 9, result.Usage.ProviderRaw["cache_read_input_tokens"])
					require.Equal(t, 25, result.Usage.LastContextTokens)
					require.Empty(t, result.NativeCompactionStateJSON)
				}

			}
			require.Equal(t, transcript, history[0].ReplayMessages[0].TranscriptJSON)
		})
	}
}

func TestAnthropicPreturnUsageSurvivesContinuationFailure(t *testing.T) {
	for _, summaryFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("summary_fails=%v", summaryFails), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				raw, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				switch calls {
				case 1:
					assert.Contains(t, string(raw), `"pause_after_compaction":true`)
					w.Header().Set("Content-Type", "text/event-stream")
					for _, event := range []string{
						`{"type":"message_start","message":{"id":"native","usage":{"input_tokens":100,"cache_creation_input_tokens":7,"cache_read_input_tokens":9}}}`,
						`{"type":"content_block_start","index":0,"content_block":{"type":"compaction","content":"COMPACTED_FACT"}}`,
						`{"type":"content_block_stop","index":0}`,
						`{"type":"message_delta","delta":{"stop_reason":"compaction"},"usage":{"output_tokens":10}}`,
						`{"type":"message_stop"}`,
					} {
						fmt.Fprintf(w, "data: %s\n\n", event)
					}
				case 2:
					assert.Contains(t, string(raw), "COMPACTED_FACT")
					assert.Contains(t, string(raw), "PENDING_FOLLOWUP")
					assert.Contains(t, string(raw), "CURRENT_ATTACHMENTS")
					assert.Contains(t, string(raw), "CURRENT_PROJECT_CONTEXT")
					compactionServerError(w, 400, "invalid_request_error", "prompt is too long")
				case 3:
					assert.Contains(t, string(raw), "CONTEXT CHECKPOINT COMPACTION")
					assert.Contains(t, string(raw), "COMPACTED_FACT")
					assert.NotContains(t, string(raw), `"type":"compaction"`)
					if summaryFails {
						compactionServerError(w, 400, "invalid_request_error", "summary rejected")
					} else {
						compactionServerReply(w, models.ProviderAnthropic, "FALLBACK_SUMMARY", "")
					}
				case 4:
					assert.Contains(t, string(raw), "FALLBACK_SUMMARY")
					assert.Equal(t, 1, strings.Count(string(raw), "PENDING_FOLLOWUP"))
					assert.Contains(t, string(raw), "CURRENT_ATTACHMENTS")
					assert.Contains(t, string(raw), "CURRENT_PROJECT_CONTEXT")
					compactionServerReply(w, models.ProviderAnthropic, "Done", "")
				default:
					t.Errorf("unexpected call %d", calls)
					compactionServerError(w, 400, "invalid_request_error", "unexpected call")
				}
			}))
			defer server.Close()
			old := anthropicclient.AnthropicAPIHost
			anthropicclient.AnthropicAPIHost = server.URL
			defer func() { anthropicclient.AnthropicAPIHost = old }()
			svc := NewLLMService(nil, nil, nil, nil, nil, nil)
			req := llmcontracts.AgentRequest{Ctx: managedcontext.With(attachmentcontext.With(compactionTestContext(), "CURRENT_ATTACHMENTS"), "CURRENT_PROJECT_CONTEXT"), Operation: llmcontracts.OperationStreaming, Followup: true, Message: "PENDING_FOLLOWUP", WorkDir: t.TempDir(), ContextTokenEstimate: 190000,
				ChatHistory: []models.Execution{{Status: models.ExecCompleted, PromptSent: "Earlier question", Output: "Earlier answer"}},
				Agent:       models.LLMConfig{ID: "preturn-continuation-failure", Provider: models.ProviderAnthropic, Model: "claude-sonnet-4-6", AuthMethod: models.AuthMethodAPIKey, APIKey: "test-continuation-failure", ContextWindow: 200000}}
			result, err := svc.callProviderWithCompaction(svc.providerAdapters[models.ProviderAnthropic], req)
			if summaryFails {
				require.Error(t, err)
				require.Equal(t, 3, calls)
				require.Equal(t, 110, result.Usage.TotalTokens)
			} else {
				require.NoError(t, err)
				require.Equal(t, 4, calls)
				require.Contains(t, result.Output, "Done")
				require.Equal(t, 120, result.Usage.InputTokens)
				require.Equal(t, 15, result.Usage.OutputTokens)
				require.Equal(t, 135, result.Usage.TotalTokens)
				require.Equal(t, 25, result.Usage.LastContextTokens)
			}
			require.Equal(t, 16, result.Usage.CachedInputTokens)
			require.Equal(t, 7, result.Usage.ProviderRaw["cache_creation_input_tokens"])
			require.Equal(t, 9, result.Usage.ProviderRaw["cache_read_input_tokens"])
			require.Empty(t, result.NativeCompactionStateJSON)
		})
	}
}

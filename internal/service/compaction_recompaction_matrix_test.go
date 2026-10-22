package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Recompacting a restored summary must retain its execution boundary, or advance
// it when newer runs are included. Exercise real adapters, SQLite, and reloads.
func TestConnectedSummaryRecompactionBoundaryMatrix(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	providers := []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible}
	for _, from := range providers {
		for _, to := range providers {
			for _, newer := range []bool{false, true} {
				for _, fail := range []bool{false, true} {
					name := string(from) + "/" + string(to)
					if newer {
						name += "/newer_runs"
					} else {
						name += "/summary_only"
					}
					if fail {
						name += "/continuation_failure"
					} else {
						name += "/success"
					}
					t.Run(name, func(t *testing.T) {
						ctx := compactionTestContext()
						stores := teststores.New(t).Stores
						task := &models.Task{ProjectID: "default", Title: "recompaction", Category: models.CategoryBacklog, Status: models.StatusCompleted}
						require.NoError(t, stores.Tasks.Create(ctx, task))
						create := func(prompt, output string) *models.Execution {
							e := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: prompt}
							require.NoError(t, stores.Executions.Create(ctx, e))
							if output != "" {
								require.NoError(t, stores.Executions.Complete(ctx, e.ID, models.ExecCompleted, output, "", 0, 0))
								e.Status, e.Output = models.ExecCompleted, output
							}
							return e
						}
						old := create("OLD_PROMPT", "OLD_ALREADY_SUMMARIZED")
						history := []models.Execution{*old}
						expectedBoundary := old.ID
						if newer {
							recent := create("RECENT_PROMPT", "NEWLY_SUMMARIZED_OUTPUT")
							history = append(history, *recent)
							expectedBoundary = recent.ID
						}
						current := create("CURRENT_REQUEST", "")
						stage, summaries, generations := 0, 0, 0
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							if r.Header.Get("Upgrade") == "websocket" {
								http.Error(w, "use HTTP", http.StatusUpgradeRequired)
								return
							}
							raw, err := io.ReadAll(r.Body)
							assert.NoError(t, err)
							text := string(raw)
							assert.NotContains(t, text, "OLD_ALREADY_SUMMARIZED")
							if strings.Contains(text, "CONTEXT CHECKPOINT COMPACTION") {
								summaries++
								assert.Equal(t, 0, stage, "reload must not need another compaction")
								assert.Contains(t, text, "PRIOR_SUMMARY_FACT")
								if newer {
									assert.Contains(t, text, "NEWLY_SUMMARIZED_OUTPUT")
								}
								compactionServerReply(w, to, "SMALL_SUMMARY", "")
								return
							}
							generations++
							assert.Contains(t, text, "SMALL_SUMMARY")
							assert.NotContains(t, text, "NEWLY_SUMMARIZED_OUTPUT")
							if stage == 0 && fail {
								http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
								return
							}
							compactionServerReply(w, to, "NEW_ANSWER", "")
						}))
						defer server.Close()
						oldAI, oldAnth := openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost
						openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost = server.URL+"/v1/", server.URL
						defer func() { openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost = oldAI, oldAnth }()
						config := func(p models.LLMProvider) models.LLMConfig {
							model := "fixture"
							if p == models.ProviderOpenAI {
								model = "gpt-5.3-codex"
							}
							if p == models.ProviderAnthropic {
								model = "claude-sonnet-4-6"
							}
							return models.LLMConfig{ID: string(p), Provider: p, Model: model, APIKey: "test", AuthMethod: models.AuthMethodAPIKey, BaseURL: server.URL + "/v1", PresetSlug: "vllm", ContextWindow: 80000, DisableNativeCompaction: true}
						}
						source := config(from)
						source.ContextWindow = 200000 // The selected destination has a smaller input window.
						require.NoError(t, stores.Executions.UpsertChatCompactionCheckpoint(ctx, models.ChatCompactionCheckpoint{
							ScopeType: "task", ScopeID: task.ID, SourceExecutionID: old.ID, CompatibilityKey: providerCompatibilityKey(source), ModelConfigID: source.ID, Strategy: "local_summary",
							History: buildCompactedReplacementHistory(nil, "PRIOR_SUMMARY_FACT "+strings.Repeat("summary detail ", 17000)),
						}))
						req := llmcontracts.AgentRequest{Ctx: ctx, ExecID: current.ID, Agent: config(to), Message: current.PromptSent, Operation: llmcontracts.OperationStreaming, Followup: true, TransportScope: "task:" + task.ID, WorkDir: t.TempDir(), ChatHistory: history, DisableTools: true, DisableNativeCompaction: true}
						send := func() error {
							svc := NewLLMService(nil, stores.Executions, nil, nil, nil, stores.Attachments)
							_, err := svc.callProviderWithCompaction(svc.providerAdapters[to], req)
							return err
						}
						err := send()
						if fail {
							require.Error(t, err)
						} else {
							require.NoError(t, err)
						}
						require.Equal(t, 1, summaries)
						saved, err := stores.Executions.GetChatCompactionCheckpoint(ctx, "task", task.ID)
						require.NoError(t, err)
						require.Equal(t, expectedBoundary, saved.SourceExecutionID)
						status, output := models.ExecCompleted, "NEW_ANSWER"
						if fail {
							status, output = models.ExecFailed, ""
						}
						require.NoError(t, stores.Executions.Complete(ctx, current.ID, status, output, "", 0, 0))
						current, err = stores.Executions.GetByID(ctx, current.ID)
						require.NoError(t, err)
						history = append(history, *current)
						next := create("RELOADED_FOLLOWUP", "")
						stage = 1
						req.ExecID, req.Message, req.ChatHistory = next.ID, next.PromptSent, history
						require.NoError(t, send())
						require.Equal(t, 1, summaries)
						require.Equal(t, 2, generations)
					})
				}
			}
		}
	}
}

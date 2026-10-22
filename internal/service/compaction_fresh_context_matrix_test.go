package service

import (
	"context"
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

// Exercise persisted clear-context boundaries through the real HTTP adapters.
// Old checkpoints must stay excluded even after the boundary leaves loaded history.
func TestConnectedFreshContextCheckpointMatrix(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	providers := []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible}
	for _, source := range providers {
		for _, target := range providers {
			kinds := []string{"summary"}
			if source != models.ProviderOpenAICompatible {
				kinds = append(kinds, "native")
			}
			for _, kind := range kinds {
				t.Run(string(source)+"/"+string(target)+"/"+kind, func(t *testing.T) {
					ctx := compactionTestContext()
					stores := teststores.New(t).Stores
					task := &models.Task{ProjectID: "default", Title: "fresh context", Status: models.StatusCompleted, Category: models.CategoryBacklog}
					require.NoError(t, stores.Tasks.Create(ctx, task))
					stage, calls := 0, 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Header.Get("Upgrade") == "websocket" {
							http.Error(w, "use HTTP", http.StatusUpgradeRequired)
							return
						}
						raw, err := io.ReadAll(r.Body)
						assert.NoError(t, err)
						calls++
						text := string(raw)
						assert.NotContains(t, text, "OLD_CONTEXT")
						assert.NotContains(t, text, localContextCompactionInstruction, "fresh context must not summarize the old run")
						assert.Contains(t, text, "NEW_REQUEST")
						if stage == 2 {
							assert.Contains(t, text, "NEW_CONTEXT_SUMMARY")
						}
						compactionServerReply(w, target, "NEW_ANSWER", "")
					}))
					defer server.Close()
					oldAI, oldAnth := openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost
					openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost = server.URL+"/v1/", server.URL
					defer func() { openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost = oldAI, oldAnth }()
					config := func(provider models.LLMProvider) models.LLMConfig {
						model := "fixture"
						if provider == models.ProviderOpenAI {
							model = "gpt-5.3-codex"
						}
						if provider == models.ProviderAnthropic {
							model = "claude-opus-5-5"
						}
						return models.LLMConfig{ID: string(provider), Provider: provider, Model: model, AuthMethod: models.AuthMethodAPIKey, APIKey: "test", BaseURL: server.URL + "/v1", PresetSlug: "vllm", ContextWindow: 200000, DisableNativeCompaction: true}
					}
					create := func(prompt string, reset bool) *models.Execution {
						e := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: prompt, StartsNewContext: reset}
						require.NoError(t, stores.Executions.Create(ctx, e))
						return e
					}
					finish := func(e *models.Execution) {
						require.NoError(t, stores.Executions.Complete(ctx, e.ID, models.ExecCompleted, "NEW_ANSWER", "", 0, 0))
					}
					old := create("OLD_CONTEXT", false)
					finish(old)
					checkpoint := models.ChatCompactionCheckpoint{ScopeType: "task", ScopeID: task.ID, SourceExecutionID: old.ID, ModelConfigID: string(source), CompatibilityKey: providerCompatibilityKey(config(source)), Strategy: "local_summary", History: buildCompactedReplacementHistory(nil, "OLD_CONTEXT_SUMMARY")}
					if kind == "native" {
						checkpoint.History = nil
						if source == models.ProviderOpenAI {
							checkpoint.Strategy = "native"
							checkpoint.ProviderStateJSON = `[{"type":"compaction","encrypted_content":"OLD_CONTEXT_OPAQUE"}]`
						} else {
							checkpoint.Strategy = "anthropic_context_management"
							checkpoint.ProviderStateJSON = `{"version":1,"messages":[{"role":"assistant","content":[{"type":"compaction","content":"OLD_CONTEXT_SUMMARY"}]}]}`
						}
					}
					require.NoError(t, stores.Executions.UpsertChatCompactionCheckpoint(ctx, checkpoint))
					fresh := create("NEW_REQUEST fresh", true)
					request := llmcontracts.AgentRequest{Ctx: ctx, ProjectID: "default", ExecID: fresh.ID, Agent: config(target), Operation: llmcontracts.OperationStreaming, Followup: true, TransportScope: "task:" + task.ID, WorkDir: t.TempDir(), Message: fresh.PromptSent, ChatHistory: []models.Execution{}, DisableNativeCompaction: true}
					send := func() {
						svc := NewLLMService(nil, stores.Executions, nil, nil, nil, stores.Attachments)
						result, err := svc.callProviderWithCompaction(svc.providerAdapters[target], request)
						require.NoError(t, err)
						require.Contains(t, result.Output, "NEW_ANSWER")
					}
					send()
					finish(fresh)
					// Add a later run so bounded history no longer includes StartsNewContext.
					if source == models.ProviderOpenAICompatible && target == source {
						// Exercise paging past a full page of newer executions.
						for i := 0; i < 100; i++ {
							finish(create("NEW_REQUEST intermediate", false))
						}
					}
					recent := create("NEW_REQUEST recent", false)
					finish(recent)
					recent, err := stores.Executions.GetByID(ctx, recent.ID)
					require.NoError(t, err)
					next := create("NEW_REQUEST followup", false)
					stage = 1
					request.ExecID = next.ID
					request.Message = next.PromptSent
					request.ChatHistory = []models.Execution{*recent}
					send()
					finish(next)
					// A checkpoint from the fresh context is valid, including one at its boundary.
					checkpoint = models.ChatCompactionCheckpoint{ScopeType: "task", ScopeID: task.ID, SourceExecutionID: fresh.ID, ModelConfigID: string(target), CompatibilityKey: providerCompatibilityKey(config(target)), Strategy: "local_summary", History: buildCompactedReplacementHistory(nil, "NEW_CONTEXT_SUMMARY")}
					require.NoError(t, stores.Executions.UpsertChatCompactionCheckpoint(ctx, checkpoint))
					last := create("NEW_REQUEST reload", false)
					stage = 2
					request.ExecID = last.ID
					request.Message = last.PromptSent
					send()
					require.Equal(t, 3, calls)
					saved, err := stores.Executions.GetChatCompactionCheckpoint(context.Background(), "task", task.ID)
					require.NoError(t, err)
					require.NotNil(t, saved)
					require.True(t, strings.Contains(saved.History[len(saved.History)-1].PromptSent, "NEW_CONTEXT_SUMMARY"))
				})
			}
		}
	}
}

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestConnectedSummaryPreflightFailureSwitchMatrix(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	providers := []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible}
	for _, p := range providers {
		for _, target := range providers {
			t.Run(string(p)+"/"+string(target), func(t *testing.T) {
				ctx := compactionTestContext()
				stores := teststores.New(t).Stores
				task := &models.Task{ProjectID: "default", Title: "summary preflight failure", Category: models.CategoryBacklog, Status: models.StatusCompleted}
				require.NoError(t, stores.Tasks.Create(ctx, task))
				current := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "CURRENT_REQUEST"}
				require.NoError(t, stores.Executions.Create(ctx, current))
				dir := t.TempDir()
				file := filepath.Join(dir, "evidence.txt")
				require.NoError(t, os.WriteFile(file, []byte("PRE_SUMMARY_TOOL_EVIDENCE"), 0600))
				calls, summaries := 0, 0
				reload := false
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Upgrade") == "websocket" {
						http.Error(w, "HTTP", 426)
						return
					}
					raw, _ := io.ReadAll(r.Body)
					text := string(raw)
					if reload {
						assert.True(t, strings.Contains(text, "SAVED_SUMMARY_MARKER"), "saved summary missing after reload")
						assert.False(t, strings.Contains(text, "PRE_SUMMARY_ANSWER"), "already summarized answer returned after reload")
						assert.False(t, strings.Contains(text, "read739"), "already summarized tool exchange returned after reload")
						if strings.Contains(text, "CONTEXT CHECKPOINT COMPACTION") {
							compactionServerReply(w, target, "SAVED_SUMMARY_MARKER", "")
						} else {
							compactionServerReply(w, target, "done", "")
						}
						return
					}
					if strings.Contains(text, "CONTEXT CHECKPOINT COMPACTION") {
						summaries++
						compactionServerReply(w, p, "SAVED_SUMMARY_MARKER "+strings.Repeat("summary ", 100000), "")
						return
					}
					calls++
					if calls == 1 {
						compactionServerReply(w, p, "PRE_SUMMARY_ANSWER", file)
						return
					}
					compactionServerError(w, 400, "context_length_exceeded", "context window exceeded")
				}))
				defer server.Close()
				oldAI, oldAnth := openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost
				openaiclient.OpenAIAPIBaseURL = server.URL + "/v1/"
				anthropicclient.AnthropicAPIHost = server.URL
				defer func() { openaiclient.OpenAIAPIBaseURL = oldAI; anthropicclient.AnthropicAPIHost = oldAnth }()
				model := "fixture"
				if p == models.ProviderOpenAI {
					model = "gpt-5.3-codex"
				}
				if p == models.ProviderAnthropic {
					model = "claude-opus-5-5"
				}
				req := llmcontracts.AgentRequest{Ctx: ctx, ExecID: current.ID, Agent: models.LLMConfig{ID: "config", Provider: p, Model: model, APIKey: "test", AuthMethod: models.AuthMethodAPIKey, BaseURL: server.URL + "/v1", PresetSlug: "vllm", ContextWindow: 200000, DisableNativeCompaction: true}, Operation: llmcontracts.OperationStreaming, Followup: true, TransportScope: "task:" + task.ID, WorkDir: dir, Message: current.PromptSent, ChatHistory: []models.Execution{{PromptSent: "old request", Output: "old answer", Status: models.ExecCompleted}}, DisableNativeCompaction: true}
				svc := NewLLMService(nil, stores.Executions, nil, nil, nil, stores.Attachments)
				_, err := svc.callProviderWithCompaction(svc.providerAdapters[p], req)
				require.Error(t, err)
				require.True(t, recognizedContextLengthError(err))
				require.Equal(t, 1, summaries)
				require.Equal(t, 2, calls)
				cp, err := stores.Executions.GetChatCompactionCheckpoint(ctx, "task", task.ID)
				require.NoError(t, err)
				require.Equal(t, "local_summary_inflight", cp.Strategy)
				require.Equal(t, current.ID, cp.SourceExecutionID)
				replay, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{current.ID})
				require.NoError(t, err)
				require.Len(t, replay[current.ID], 1)
				require.Equal(t, "[]", replay[current.ID][0].TranscriptJSON)
				require.NoError(t, stores.Executions.Complete(ctx, current.ID, models.ExecFailed, "PRE_SUMMARY_ANSWER", "", 0, 0))
				current, err = stores.Executions.GetByID(ctx, current.ID)
				require.NoError(t, err)
				require.Equal(t, "PRE_SUMMARY_ANSWER", current.Output)
				next := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "followup"}
				require.NoError(t, stores.Executions.Create(ctx, next))
				req.Agent.Provider = target
				req.Agent.ID = string(target)
				req.Agent.Model = "fixture"
				if target == models.ProviderOpenAI {
					req.Agent.Model = "gpt-5.3-codex"
				}
				if target == models.ProviderAnthropic {
					req.Agent.Model = "claude-opus-5-5"
				}
				req.ExecID = next.ID
				req.ChatHistory = []models.Execution{*current}
				req.Message = "followup"
				req.Agent.ContextWindow = 1000000
				reload = true
				svc = NewLLMService(nil, stores.Executions, nil, nil, nil, stores.Attachments)
				_, err = svc.callProviderWithCompaction(svc.providerAdapters[target], req)
				require.NoError(t, err)
			})
		}
	}
}

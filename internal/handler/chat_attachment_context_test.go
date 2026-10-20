package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmanthropic "github.com/openvibely/openvibely/internal/llm/anthropic"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmollama "github.com/openvibely/openvibely/internal/llm/ollama"
	llmopenai "github.com/openvibely/openvibely/internal/llm/openai"
	llmcompatible "github.com/openvibely/openvibely/internal/llm/openai_compatible"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/retrytest"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"github.com/openvibely/openvibely/pkg/managedcontext"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
)

func TestChatAttachmentContextPreservesTurnOwnership(t *testing.T) {
	h, _, configs, stores := setupTestHandlerWithStores(t)
	h.chatAttachmentRepo = stores.ChatAttachments
	ctx := context.Background()
	agent := createAgent(t, configs)
	project := createProject(t, h, "Attachments")
	task := createTask(t, h, project.ID, "Edit document", func(task *models.Task) { task.Category = models.CategoryChat })
	old := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.PromptSent = strings.Repeat("Original long request ", 20000)
		ex.Status = models.ExecCompleted
	})
	otherProject := createProject(t, h, "Other project")
	otherTask := createTask(t, h, otherProject.ID, "Other chat", func(task *models.Task) { task.Category = models.CategoryChat })
	other := createExec(t, h, otherTask.ID, agent.ID)
	for _, file := range []models.ChatAttachment{
		{ExecutionID: old.ID, FileName: "old.docx", FilePath: "/missing/old.docx", MediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{ExecutionID: old.ID, FileName: "old.png", FilePath: "/missing/old.png", MediaType: "image/png"},
		{ExecutionID: other.ID, FileName: "excluded.pdf", FilePath: "/missing/excluded.pdf", MediaType: "application/pdf"},
	} {
		require.NoError(t, stores.ChatAttachments.Create(ctx, &file))
	}
	history := []models.Execution{*old}
	params := streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, Message: "Replace Paver with Fin", ChatHistory: history, SystemContext: "Personality",
		ImageAttachments: []models.Attachment{{FileName: "current.pdf", FilePath: "/missing/current.pdf", MediaType: "application/pdf"}}}
	files, err := h.prepareChatAttachmentContext(ctx, &params)
	require.NoError(t, err)
	require.Len(t, files, 3)
	require.Equal(t, old.PromptSent, history[0].PromptSent)
	saved, err := h.execRepo.GetByID(ctx, old.ID)
	require.NoError(t, err)
	require.Equal(t, old.PromptSent, saved.PromptSent)
	require.NotContains(t, params.historicalAttachmentContext, "Original long request")
	require.Less(t, len(params.historicalAttachmentContext), 4096)
	require.Contains(t, params.historicalAttachmentContext, old.ID)
	require.Contains(t, params.historicalAttachmentContext, "/missing/old.docx")
	require.Contains(t, params.historicalAttachmentContext, "/missing/old.png")
	require.NotContains(t, params.historicalAttachmentContext, "already included directly")
	require.NotContains(t, params.historicalAttachmentContext, "excluded.pdf")
	require.NotContains(t, params.Message, "old.docx")
	require.Contains(t, params.Message, "/missing/current.pdf")
	require.Equal(t, "Personality", params.SystemContext)
	require.Len(t, params.ImageAttachments, 1, "historical attachments must not be re-uploaded")
}

func TestCurrentAttachmentContextDoesNotDuplicateReferences(t *testing.T) {
	h := &Handler{}
	description, files, err := buildChatAttachmentModelContext([]chatAttachmentModelContextFile{
		{FileName: "current.docx", FilePath: "/missing/current.docx"},
		{FileName: "current.png", FilePath: "/missing/current.png"},
	}, chatAttachmentModelContextOptions{})
	require.NoError(t, err)
	p := streamingResponseParams{Message: "Review", AttachmentContext: description, ImageAttachments: files}
	_, err = h.prepareChatAttachmentContext(context.Background(), &p)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(p.Message, "/missing/current.docx"))
	require.Equal(t, 1, strings.Count(p.Message, "/missing/current.png"))
	require.Contains(t, p.Message, "already included directly")
	require.Empty(t, p.SystemContext)
}

// Exercise real provider serializers against local servers, including a follow-up
// reconstructed from persisted attachment metadata. No files or live APIs needed.
func TestAttachmentReferencesInUserMessagesAcrossProviders(t *testing.T) {
	for _, state := range []string{"completed", "cancelled", "compacted"} {
		for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderOpenAICompatible, models.ProviderAnthropic, models.ProviderOllama} {
			t.Run(string(provider)+"/"+state, func(t *testing.T) {
				h, _, configs, stores := setupTestHandlerWithStores(t)
				h.chatAttachmentRepo = stores.ChatAttachments
				ctx := retrytest.Context()
				agent := createAgent(t, configs)
				project := createProject(t, h, "Wire attachments")
				task := createTask(t, h, project.ID, "Edit document", func(task *models.Task) { task.Category = models.CategoryChat })
				old := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
					ex.PromptSent = "Summarize"
					ex.Output = "Summary"
					ex.Status = models.ExecCompleted
					if state == "cancelled" {
						ex.Status = models.ExecCancelled
						ex.Output = ""
					}
				})
				require.NoError(t, stores.ChatAttachments.Create(ctx, &models.ChatAttachment{ExecutionID: old.ID, FileName: "reference.docx", FilePath: "/missing/reference.docx", MediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"}))
				p := streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, Message: "Replace Paver with Fin", ChatHistory: []models.Execution{*old}, SystemContext: "Be helpful"}
				_, err := h.prepareChatAttachmentContext(ctx, &p)
				require.NoError(t, err)
				ctx = attachmentcontext.With(ctx, p.historicalAttachmentContext)
				ctx = managedcontext.With(ctx, "documents: edit docx files; preferences.md: durable preferences")
				if state == "compacted" {
					p.ChatHistory = []models.Execution{{PromptSent: "Earlier conversation summary", Output: "Document summarized", Status: models.ExecCompleted}}
				}
				var wire map[string]any
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
						http.Error(w, "no websocket", http.StatusUpgradeRequired)
						return
					}
					if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					switch provider {
					case models.ProviderOpenAI:
						fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"done\",\"status\":\"completed\",\"output\":[]}}\n\n")
					case models.ProviderOpenAICompatible, models.ProviderOllama:
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Done\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					case models.ProviderAnthropic:
						for _, event := range []string{
							`{"type":"message_start","message":{"id":"done","model":"claude-opus-4-6","usage":{"input_tokens":8}}}`,
							`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
							`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done"}}`,
							`{"type":"content_block_stop","index":0}`,
							`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
							`{"type":"message_stop"}`,
						} {
							fmt.Fprintf(w, "data: %s\n\n", event)
						}
					}
				}))
				defer server.Close()
				agent.Provider = provider
				agent.AuthMethod = models.AuthMethodAPIKey
				agent.APIKey = "test"
				agent.BaseURL = server.URL + "/v1/"
				workDir := t.TempDir()
				switch provider {
				case models.ProviderOllama:
					agent.Model = "local-test"
					agent.OllamaBaseURL = server.URL
					agent.ContextWindow = 32768
					_, err = llmollama.New(nil, nil).Call(ctx, llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, Message: p.Message, ChatHistory: p.ChatHistory, ChatSystemContext: p.SystemContext, Followup: true, Agent: *agent, DisableTools: true}, workDir, nil)
				case models.ProviderOpenAI:
					original := openaiclient.OpenAIAPIBaseURL
					openaiclient.OpenAIAPIBaseURL = server.URL + "/v1/"
					defer func() { openaiclient.OpenAIAPIBaseURL = original }()
					agent.Model = "gpt-6-sol"
					_, _, err = llmopenai.New(nil, nil, nil, nil).CallChatStreaming(ctx, p.Message, nil, *agent, "", "", "", p.ChatHistory, p.SystemContext, true, models.ChatModeOrchestrate, workDir, nil)
				case models.ProviderOpenAICompatible:
					t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
					agent.Model = "custom-model"
					agent.Transport = "chat_completions"
					agent.PresetSlug = "vllm"
					_, err = llmcompatible.New(nil, nil).Call(ctx, llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, Message: p.Message, ChatHistory: p.ChatHistory, ChatSystemContext: p.SystemContext, Followup: true, Agent: *agent, DisableTools: true}, workDir)
				case models.ProviderAnthropic:
					original := anthropicclient.AnthropicAPIHost
					anthropicclient.AnthropicAPIHost = server.URL
					defer func() { anthropicclient.AnthropicAPIHost = original }()
					agent.Model = "claude-opus-4-6"
					_, err = llmanthropic.New(nil, nil, nil).Call(ctx, llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, Message: p.Message, ChatHistory: p.ChatHistory, ChatSystemContext: p.SystemContext, Followup: true, Agent: *agent, DisableTools: true}, workDir, nil)
				}
				require.NoError(t, err)
				require.NotNil(t, wire)
				for _, key := range []string{"system", "instructions"} {
					encoded, _ := json.Marshal(wire[key])
					require.NotContains(t, string(encoded), "/missing/reference.docx")
					require.NotContains(t, string(encoded), "preferences.md")
				}
				messages, ok := wire["messages"].([]any)
				if provider == models.ProviderOpenAI {
					messages, ok = wire["input"].([]any)
				}
				require.True(t, ok, "missing wire messages: %#v", wire)
				references := 0
				indexes := 0
				for _, raw := range messages {
					msg := raw.(map[string]any)
					content, _ := json.Marshal(msg["content"])
					if strings.Contains(string(content), "preferences.md") {
						indexes++
						require.Equal(t, "user", msg["role"])
					}
					if strings.Contains(string(content), "/missing/reference.docx") {
						references++
						require.Equal(t, "user", msg["role"])
						require.NotContains(t, string(content), "Summarize")
						require.Contains(t, string(content), old.ID)
						require.NotContains(t, string(content), "Replace Paver")
					}
					require.NotContains(t, string(content), "base64")
				}
				require.Equal(t, 1, references)
				require.Equal(t, 1, indexes)
			})
		}
	}

}

func TestChatAttachmentContextIncludesFilesOutsideHistory(t *testing.T) {
	h, _, configs := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, configs)
	project := createProject(t, h, "Old chat files")
	task := createTask(t, h, project.ID, "Chat", func(task *models.Task) { task.Category = models.CategoryChat })
	old := createExec(t, h, task.ID, agent.ID)
	require.NoError(t, h.chatAttachmentRepo.Create(ctx, &models.ChatAttachment{ExecutionID: old.ID, FileName: "report.pdf", FilePath: "/missing/report.pdf", MediaType: "application/pdf"}))
	var history []models.Execution
	for i := 0; i < 21; i++ {
		turn := createTask(t, h, project.ID, fmt.Sprintf("Chat turn %d", i), func(task *models.Task) { task.Category = models.CategoryChat })
		history = append(history, *createExec(t, h, turn.ID, agent.ID))
	}
	for _, recent := range [][]models.Execution{history[1:], nil} {
		params := streamingResponseParams{ProjectID: project.ID, TaskID: history[len(history)-1].TaskID, Message: "Read the report", ChatHistory: recent}
		files, err := h.prepareChatAttachmentContext(ctx, &params)
		require.NoError(t, err)
		require.Len(t, files, 1)
		require.Contains(t, attachmentcontext.Text(params.withAttachmentContext(ctx)), "/missing/report.pdf")
		require.Empty(t, params.ImageAttachments, "old files are references only")
	}
}

func TestChatAttachmentContextRefreshesDuringRun(t *testing.T) {
	h, _, configs := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, configs)
	project := createProject(t, h, "Live chat files")
	task := createTask(t, h, project.ID, "Chat", func(task *models.Task) { task.Category = models.CategoryChat })
	exec := createExec(t, h, task.ID, agent.ID)
	params := streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, ExecID: exec.ID, Message: "go"}
	_, err := h.prepareChatAttachmentContext(ctx, &params)
	require.NoError(t, err)
	reqCtx := params.withAttachmentContext(ctx)
	require.Empty(t, attachmentcontext.Text(reqCtx))
	type msg struct{ role, text string }
	read := func(m msg) (string, string) { return m.role, m.text }
	makeMessage := func(s string) msg { return msg{"user", s} }
	history := []msg{{"user", "go"}}
	file := &models.ChatAttachment{ExecutionID: exec.ID, FileName: "added.pdf", FilePath: "/added.pdf", MediaType: "application/pdf"}
	require.NoError(t, h.chatAttachmentRepo.Create(ctx, file))
	history = attachmentcontext.Ensure(reqCtx, history, read, makeMessage)
	require.Len(t, history, 2)
	require.Contains(t, history[0].text, "added.pdf")
	require.Equal(t, history, attachmentcontext.Ensure(reqCtx, history, read, makeMessage))
	other := createProject(t, h, "Other chat files")
	otherTask := createTask(t, h, other.ID, "Other chat", func(task *models.Task) { task.Category = models.CategoryChat })
	otherExec := createExec(t, h, otherTask.ID, agent.ID)
	require.NoError(t, h.chatAttachmentRepo.Create(ctx, &models.ChatAttachment{ExecutionID: otherExec.ID, FileName: "private.pdf", FilePath: "/private.pdf", MediaType: "application/pdf"}))
	require.Equal(t, history, attachmentcontext.Ensure(reqCtx, history, read, makeMessage))
	require.Error(t, h.chatAttachmentRepo.DeleteByIDForProject(ctx, file.ID, other.ID))
	require.Contains(t, attachmentcontext.Text(reqCtx), "added.pdf")
	require.NoError(t, h.chatAttachmentRepo.DeleteByIDForProject(ctx, file.ID, project.ID))
	history = attachmentcontext.Ensure(reqCtx, append(history, msg{"assistant", "done"}), read, makeMessage)
	require.Len(t, history, 4)
	require.Contains(t, history[3].text, "No files are currently attached")
	require.Equal(t, history, attachmentcontext.Ensure(reqCtx, history, read, makeMessage))
	// Rollback uses the unscoped delete and must invalidate the same live source.
	require.NoError(t, h.chatAttachmentRepo.Create(ctx, file))
	require.Contains(t, attachmentcontext.Text(reqCtx), "added.pdf")
	require.NoError(t, h.chatAttachmentRepo.Delete(ctx, file.ID))
	require.Empty(t, attachmentcontext.Text(reqCtx))
}

package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	contracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	gemini "github.com/openvibely/openvibely/pkg/gemini_client"
	"github.com/stretchr/testify/require"
)

func TestGeminiCompactionAndCheckpointReload(t *testing.T) {
	ctx := compactionTestContext()
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "Gemini compaction", Status: models.StatusRunning, Category: models.CategoryActive}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	create := func(prompt string) *models.Execution {
		e := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: prompt}
		require.NoError(t, stores.Executions.Create(ctx, e))
		return e
	}
	source := create("USER_RULE: preserve the public API")
	contents := []gemini.Content{
		gemini.TextContent("user", source.PromptSent),
		gemini.TextContent("model", "OLD_DETAIL "+strings.Repeat("historical detail ", 7000)),
		{Role: "model", Parts: []gemini.Part{{FunctionCall: &gemini.FunctionCall{ID: "image-call", Name: "view_image", Args: json.RawMessage(`{"path":"image.png"}`)}}}},
		{Role: "user", Parts: []gemini.Part{{FunctionResponse: &gemini.FunctionResponse{ID: "image-call", Name: "view_image", Response: map[string]any{"output": "TOOL_IMAGE_RESULT"}, Parts: []gemini.FunctionResponsePart{{InlineData: &gemini.InlineData{MimeType: "image/png", Data: "YWJj"}}, {FileData: &gemini.FileData{MimeType: "image/png", FileURI: "https://example.test/image.png"}}}}}}},
		gemini.TextContent("model", "inspection complete"),
	}
	var rows []json.RawMessage
	for _, c := range contents {
		raw, err := json.Marshal(c)
		require.NoError(t, err)
		rows = append(rows, raw)
	}
	require.NoError(t, stores.Executions.ReplaceReasoningReplay(ctx, source.ID, "", []models.ExecutionReplayMessage{models.ReplayFromRows("contents", rows)}))
	require.NoError(t, stores.Executions.Complete(ctx, source.ID, models.ExecCompleted, "inspection complete", "", 0, 0))
	source, err := stores.Executions.GetByID(ctx, source.ID)
	require.NoError(t, err)
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		mu.Lock()
		requests = append(requests, string(raw))
		mu.Unlock()
		answer := "CONTINUED_OK"
		if strings.Contains(string(raw), "CONTEXT CHECKPOINT COMPACTION") {
			answer = "SUMMARY: inspected images; preserve the public API."
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":%q}]},\"finishReason\":\"STOP\"}]}\n\n", answer)
	}))
	defer server.Close()
	config := models.LLMConfig{ID: "gemini-test", Provider: models.ProviderGemini, Model: "gemini-3.8-flash", AuthMethod: models.AuthMethodAPIKey, APIKey: "test", ContextWindow: 65536, DefaultMaxTokens: 1024, CompactionThreshold: 12000}
	current := create("CURRENT_REQUEST continue")
	req := contracts.AgentRequest{Ctx: ctx, ProjectID: "default", ExecID: current.ID, Agent: config, Operation: contracts.OperationStreaming, Followup: true, TransportScope: "task:" + task.ID, WorkDir: t.TempDir(), Message: current.PromptSent, ChatHistory: []models.Execution{*source}, DisableTools: true}
	send := func(req contracts.AgentRequest) {
		svc := NewLLMService(nil, stores.Executions, nil, nil, nil, stores.Attachments)
		adapter := svc.providerAdapters[models.ProviderGemini].(*geminiProviderAdapter)
		adapter.adapter.SetClientConfigurer(func(c *gemini.Client) { c.SetBaseURL(server.URL) })
		result, err := svc.callProviderWithCompaction(adapter, req)
		require.NoError(t, err)
		require.Contains(t, result.Output, "CONTINUED_OK")
	}
	send(req)
	checkpoint, err := stores.Executions.GetChatCompactionCheckpoint(ctx, "task", task.ID)
	require.NoError(t, err)
	require.NotNil(t, checkpoint)
	require.Equal(t, "local_summary", checkpoint.Strategy)
	require.Contains(t, historyCompactionSummary(checkpoint.History), "SUMMARY: inspected images")
	require.NoError(t, stores.Executions.Complete(ctx, current.ID, models.ExecCompleted, "CONTINUED_OK", "", 0, 0))
	current, err = stores.Executions.GetByID(ctx, current.ID)
	require.NoError(t, err)
	next := create("RELOAD_REQUEST continue again")
	req.ExecID = next.ID
	req.Message = next.PromptSent
	req.ChatHistory = []models.Execution{*source, *current}
	send(req)
	mu.Lock()
	captured := append([]string(nil), requests...)
	mu.Unlock()
	require.Len(t, captured, 3, "one summary, one continuation, one checkpoint reload")
	for _, marker := range []string{"CONTEXT CHECKPOINT COMPACTION", "USER_RULE", "OLD_DETAIL", "view_image", "image-call", "TOOL_IMAGE_RESULT", "YWJj", "https://example.test/image.png"} {
		require.Contains(t, captured[0], marker)
	}
	var summaryBody map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(captured[0]), &summaryBody))
	require.Empty(t, summaryBody["tools"])
	for _, body := range captured[1:] {
		require.Contains(t, body, "SUMMARY: inspected images")
		require.Contains(t, body, "USER_RULE")
		require.NotContains(t, body, "OLD_DETAIL")
		require.NotContains(t, body, "CONTEXT CHECKPOINT COMPACTION")
	}
	require.Contains(t, captured[1], "CURRENT_REQUEST")
	require.Contains(t, captured[2], "RELOAD_REQUEST")
}

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	compatible "github.com/openvibely/openvibely/internal/llm/openai_compatible"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTruncatedImagesStayOutOfCompatibleProviderRequest(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	ctx := context.Background()
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "audit", Category: models.CategoryBacklog, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	execution := models.Execution{ID: "image-history", TaskID: task.ID, Status: models.ExecCompleted, PromptSent: "inspect the images"}
	require.NoError(t, stores.Executions.Create(ctx, &execution))
	blocks := []map[string]any{}
	for i := 0; i < 30; i++ {
		blocks = append(blocks, map[string]any{"type": "image_url", "image_url": map[string]any{"url": fmt.Sprintf("data:image/png;base64,IMAGE%d", i)}})
	}
	raw, err := json.Marshal([]map[string]any{{"role": "user", "content": blocks}})
	require.NoError(t, err)
	execution.ReplayMessages = []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}
	require.NoError(t, stores.Executions.ReplaceReasoningReplay(ctx, execution.ID, "", execution.ReplayMessages))
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	req := llmcontracts.AgentRequest{Ctx: ctx, Operation: llmcontracts.OperationStreaming, Message: "continue", Followup: true, DisableTools: true, Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey, APIKey: "test", PresetSlug: "vllm", BaseURL: server.URL + "/v1", Model: "test", ContextWindow: 50000}, ChatHistory: []models.Execution{execution}}
	a := compatible.New(stores.Executions, nil)
	_, err = (&LLMService{}).callProviderWithLastResortTruncation(providerAdapterFunc(func(r llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		require.Empty(t, r.ChatHistory[0].ReplayMessages, "budget layer should have discarded oversized images")
		return a.Call(r.Ctx, r, t.TempDir())
	}), req, errors.New("summary failed"))
	require.NoError(t, err)
	require.Contains(t, body, "inspect the images")
	require.Contains(t, body, "continue")
	if strings.Contains(body, "data:image/png") {
		t.Fatalf("provider request reloaded %d trimmed image payloads", strings.Count(body, "data:image/png"))
	}
}

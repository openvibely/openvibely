package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestFileRuntimeViewImageThroughProviderAdapter(t *testing.T) {
	// The provider fixture uses a loopback HTTP server, never a real model endpoint.
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	work := t.TempDir()
	var data bytes.Buffer
	require.NoError(t, png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 8, 8))))
	path := filepath.Join(work, "page.png")
	require.NoError(t, os.WriteFile(path, data.Bytes(), 0600))
	encoded := base64.StdEncoding.EncodeToString(data.Bytes())
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		calls++
		if calls == 1 && !strings.Contains(string(raw), `"name":"view_image"`) {
			t.Error("runtime tool is absent from provider definitions")
		}
		if calls == 2 && !strings.Contains(string(raw), "data:image/png;base64,"+encoded) {
			t.Error("image bytes absent from the next request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			arguments, _ := json.Marshal(map[string]string{"file_path": "page.png"})
			body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call1", "type": "function", "function": map[string]any{"name": "view_image", "arguments": string(arguments)}}}}, "finish_reason": "tool_calls"}}})
			io.WriteString(w, "data: "+string(body)+"\n\ndata: [DONE]\n\n")
		} else {
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"inspected\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer server.Close()
	svc := &LLMService{attachmentRepo: &fileRuntimeAttachmentStore{}}
	adapter, ok := svc.adapterFor(models.ProviderOpenAICompatible)
	require.True(t, ok)
	result, err := svc.callProviderWithCompaction(adapter, llmcontracts.AgentRequest{
		Ctx: context.Background(), Operation: llmcontracts.OperationDirect, Message: "inspect the rendered page", ExecID: "exec", ProjectID: "project", WorkDir: work,
		Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey, APIKey: "test-key", Model: "test", BaseURL: server.URL + "/v1", PresetSlug: "vllm", ContextWindow: 32768, MaxTokens: 1000},
	})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, "inspected", result.Output)
}

package openai_compatible

import (
	"context"
	"encoding/json"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRecoverySeedsTaskAndDirectCompletionsHistory(t *testing.T) {
	for _, operation := range []llmcontracts.Operation{llmcontracts.OperationDirect, llmcontracts.OperationStreaming} {
		t.Run(string(operation), func(t *testing.T) {
			var body string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				body = string(raw)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
			}))
			defer server.Close()
			ctx := llmcontracts.WithHistoryContinuation(context.Background())
			req := llmcontracts.AgentRequest{Operation: operation, Message: "DO_NOT_REPLAY", Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey, APIKey: "test", PresetSlug: "vllm", BaseURL: server.URL + "/v1", Model: "test"}, ChatHistory: []models.Execution{{PromptSent: "original request", Output: "ALREADY_EDITED", Status: models.ExecCompleted}}}
			_, err := New(nil, nil).Call(ctx, req, t.TempDir())
			require.NoError(t, err)
			require.Contains(t, body, "ALREADY_EDITED")
			require.NotContains(t, body, "DO_NOT_REPLAY")
			var request struct {
				Messages []struct {
					Role    string
					Content any
				}
			}
			require.NoError(t, json.Unmarshal([]byte(body), &request))
			for _, message := range request.Messages {
				if message.Role == "user" {
					require.NotEqual(t, "", message.Content)
				}
			}
		})
	}
}

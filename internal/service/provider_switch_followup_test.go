package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestAnthropicFollowupSwitchSendsToolsWithoutCompaction(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	const transcript = `{"messages":[
		{"role":"assistant","content_blocks":[{"type":"thinking","thinking":"PRIVATE_THINKING","signature":"SIGNATURE"},{"type":"tool_use","id":"a","name":"read_file","input":{"path":"a"}},{"type":"tool_use","id":"b","name":"read_file","input":{"path":"b"}}]},
		{"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"b","content":"file B"},{"type":"tool_result","tool_use_id":"a","content":"file A"}]},
		{"role":"assistant","content":"Both files checked."}
	]}`
	for _, provider := range []models.LLMProvider{models.ProviderOpenAICompatible, models.ProviderOllama} {
		t.Run(string(provider), func(t *testing.T) {
			var requests [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				requests = append(requests, raw)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			svc := &LLMService{}
			adapter, ok := svc.adapterFor(provider)
			require.True(t, ok)
			_, err := svc.callProviderWithCompaction(adapter, llmcontracts.AgentRequest{
				Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Followup: true,
				Message: "Compare those files", WorkDir: t.TempDir(),
				ChatHistory: []models.Execution{{Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: transcript}}}},
				Agent:       models.LLMConfig{Provider: provider, AuthMethod: models.AuthMethodAPIKey, APIKey: "test", Model: "test", BaseURL: server.URL + "/v1", OllamaBaseURL: server.URL, PresetSlug: "vllm", ContextWindow: 200000, MaxTokens: 1000},
			})
			require.NoError(t, err)
			require.Len(t, requests, 1, "no summary request should be sent")
			require.NotContains(t, string(requests[0]), "PRIVATE_THINKING")
			require.NotContains(t, string(requests[0]), "SIGNATURE")
			var body struct {
				Messages []map[string]any `json:"messages"`
			}
			require.NoError(t, json.Unmarshal(requests[0], &body))
			callAt := -1
			for i, message := range body.Messages {
				if calls, ok := message["tool_calls"].([]any); ok && len(calls) > 0 {
					require.Equal(t, -1, callAt)
					callAt = i
					require.Len(t, calls, 2)
					require.Equal(t, "a", calls[0].(map[string]any)["id"])
					require.Equal(t, "b", calls[1].(map[string]any)["id"])
				}
			}
			require.GreaterOrEqual(t, callAt, 0)
			require.Greater(t, len(body.Messages), callAt+2)
			for i, id := range []string{"b", "a"} {
				message := body.Messages[callAt+i+1]
				require.Equal(t, "tool", message["role"])
				require.Equal(t, id, message["tool_call_id"])
				require.Equal(t, "file "+strings.ToUpper(id), message["content"])
			}
			require.Contains(t, string(requests[0]), "Compare those files")
		})
	}
}

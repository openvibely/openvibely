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
	gemini "github.com/openvibely/openvibely/pkg/gemini_client"
	"github.com/stretchr/testify/require"
)

func TestAnthropicFollowupSwitchSendsToolsWithoutCompaction(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	const transcript = `{"messages":[
		{"role":"assistant","content_blocks":[{"type":"thinking","thinking":"PRIVATE_THINKING","signature":"SIGNATURE"},{"type":"tool_use","id":"a","name":"read_file","input":{"path":"a"}},{"type":"tool_use","id":"b","name":"read_file","input":{"path":"b"}}]},
		{"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"b","content":"file B"},{"type":"tool_result","tool_use_id":"a","content":"file A"}]},
		{"role":"assistant","content":"Both files checked."}
	]}`
	for _, provider := range []models.LLMProvider{models.ProviderOpenAICompatible, models.ProviderOllama, models.ProviderGemini} {
		t.Run(string(provider), func(t *testing.T) {
			var requests [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				requests = append(requests, raw)
				w.Header().Set("Content-Type", "text/event-stream")
				if provider == models.ProviderGemini {
					io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}]}`+"\n\n")
					return
				}
				io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			svc := &LLMService{}
			adapter, ok := svc.adapterFor(provider)
			require.True(t, ok)
			model := "test"
			if provider == models.ProviderGemini {
				model = "gemini-3.8-flash"
				adapter.(*geminiProviderAdapter).adapter.SetClientConfigurer(func(c *gemini.Client) { c.SetBaseURL(server.URL) })
			}
			result, err := svc.callProviderWithCompaction(adapter, llmcontracts.AgentRequest{
				Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Followup: true,
				Message: "Compare those files", WorkDir: t.TempDir(),
				ChatHistory: []models.Execution{{Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: transcript}}}},
				Agent:       models.LLMConfig{Provider: provider, AuthMethod: models.AuthMethodAPIKey, APIKey: "test", Model: model, BaseURL: server.URL + "/v1", OllamaBaseURL: server.URL, PresetSlug: "vllm", ContextWindow: 200000, MaxTokens: 1000},
			})
			require.NoError(t, err)
			require.Contains(t, result.Output, "done")
			require.Len(t, requests, 1, "no summary request should be sent")
			require.NotContains(t, string(requests[0]), "PRIVATE_THINKING")
			require.NotContains(t, string(requests[0]), "SIGNATURE")
			require.Contains(t, string(requests[0]), "Compare those files")
			if provider == models.ProviderGemini {
				var body struct {
					Contents []gemini.Content `json:"contents"`
				}
				require.NoError(t, json.Unmarshal(requests[0], &body))
				var events []string
				for _, content := range body.Contents {
					for _, part := range content.Parts {
						if call := part.FunctionCall; call != nil {
							require.Equal(t, "model", content.Role)
							require.Equal(t, "read_file", call.Name)
							events = append(events, "call:"+call.ID)
						}
						if response := part.FunctionResponse; response != nil {
							require.Equal(t, "user", content.Role)
							require.Equal(t, "read_file", response.Name)
							require.Equal(t, "file "+strings.ToUpper(response.ID), response.Response["output"])
							events = append(events, "result:"+response.ID)
						}
					}
				}
				require.Equal(t, []string{"call:a", "call:b", "result:b", "result:a"}, events)
				return
			}
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

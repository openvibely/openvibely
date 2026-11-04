package service

import (
	"encoding/json"
	"fmt"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"net/http"
	"net/http/httptest"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/retrytest"
)

func TestSummaryRetriesLiteLLMStreamOverflowWithLessHistory(t *testing.T) {
	const captured = ": ping\n\ndata: {\"error\":{\"message\":\"litellm.ContextWindowExceededError: litellm.BadRequestError: litellm._pre_call_checks: Context Window exceeded for given call. No models have context window large enough for this call.\\nModel=bedrock/us.anthropic.claude-sonnet-4-5-20250929-v1:0, Max Input Tokens=200000, Got=200805\\n\\nLiteLLM: model group 'premium' failed with the error above. No fallback was attempted.\",\"type\":\"invalid_request_error\"}}\n\ndata: [DONE]\n\n"
	for _, tt := range []struct {
		name, message string
		recover       bool
	}{
		{name: "captured LiteLLM overflow error", recover: true},
		{name: "class with output parameter in diagnostics", message: "litellm.ContextWindowExceededError: opaque\nModel=bedrock/anthropic-test, max_tokens=64000", recover: true},
		{name: "combined input and output overflow", message: "litellm.BadRequestError: AnthropicException - input length and `max_tokens` exceed context limit: 192926 + 64000 > 200000, decrease input length or `max_tokens` and try again", recover: true},
		{name: "output allowance", message: "litellm.BadRequestError: max_tokens must be <= 8192; got 16000"},
		{name: "output allowance mentioning context", message: "litellm.BadRequestError: max_tokens must be <= 8192; got 16000 (context length is 200000)"},
		{name: "rate limit with fallback overflow", message: "litellm.RateLimitError: opaque\n\nLiteLLM: fallback failed with litellm.ContextWindowExceededError: context window exceeded"},
		{name: "generic error with fallback overflow", message: "litellm.BadRequestError: opaque\n\nLiteLLM: fallback failed with litellm.ContextWindowExceededError: context window exceeded"},
		{name: "unknown", message: "litellm.FutureError: opaque context window failure"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var sizes []int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Messages []json.RawMessage `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					http.Error(w, "bad request", 400)
					return
				}
				sizes = append(sizes, len(payload.Messages))
				w.Header().Set("Content-Type", "text/event-stream")
				if len(sizes) == 1 {
					frame, _ := json.Marshal(map[string]any{"error": map[string]any{"message": tt.message, "type": "invalid_request_error"}})
					if tt.message == "" {
						fmt.Fprint(w, captured)
					} else {
						fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
					}
					return
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"summary done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			svc := NewLLMService(nil, nil, nil, nil, nil, nil)
			req := llmcontracts.AgentRequest{Ctx: retrytest.Context(), Agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, Model: "custom", AuthMethod: models.AuthMethodAPIKey, APIKey: "test", BaseURL: server.URL + "/v1", PresetSlug: "vllm"}}
			history := []models.Execution{{Status: models.ExecCompleted, PromptSent: "old question", Output: "old answer"}, {Status: models.ExecCompleted, PromptSent: "new question", Output: "new answer"}}
			summary, err := svc.localSummaryCompaction(svc.providerAdapters[models.ProviderOpenAICompatible], req, history)
			if !tt.recover {
				if err == nil || len(sizes) != 1 {
					t.Fatalf("expected failure without dropping history: summary=%q error=%v sizes=%v", summary, err, sizes)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if summary != "summary done" || len(sizes) != 2 || sizes[1] >= sizes[0] {
				t.Fatalf("summary=%q, message counts=%v", summary, sizes)
			}

		})
	}
}

func TestNativeOpenAIErrorClassificationRemainsUnchanged(t *testing.T) {
	// This is the existing service's legacy recognition of an APIError, not
	// a claim that a provider sends this value in a particular wire field.
	err := &openaiclient.APIError{Code: "model_context_window_exceeded", Message: "opaque"}
	if !recognizedContextLengthError(categorizeProviderError(err)) {
		t.Fatal("compatible changes must not replace native OpenAI error classification")
	}
}

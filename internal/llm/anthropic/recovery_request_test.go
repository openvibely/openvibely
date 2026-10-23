package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
)

func TestRecoverySummaryEndsWithUserMessageOnWire(t *testing.T) {
	for _, operation := range []llmcontracts.Operation{llmcontracts.OperationDirect, llmcontracts.OperationStreaming, llmcontracts.OperationTask} {
		t.Run(string(operation), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var request struct {
					Messages []anthropicclient.Message `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if len(request.Messages) == 0 || request.Messages[len(request.Messages)-1].Role != "user" {
					t.Errorf("invalid assistant prefill: %#v", request.Messages)
					http.Error(w, `{"error":{"type":"invalid_request_error","message":"assistant prefill not supported"}}`, 400)
					return
				}
				content := request.Messages[len(request.Messages)-1].Content
				for _, text := range []string{"original task", "actually only review", "file already written"} {
					if strings.Count(content, text) != 1 {
						t.Errorf("missing or duplicated %q in %q", text, content)
					}
				}
				if strings.Contains(content, "do not replay this prompt") {
					t.Error("replayed request prompt")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, event := range []string{
					`{"type":"message_start","message":{"id":"done","model":"claude-opus-4-6","usage":{"input_tokens":8}}}`,
					`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
					`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Reviewed."}}`,
					`{"type":"content_block_stop","index":0}`,
					`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
					`{"type":"message_stop"}`,
				} {
					fmt.Fprintf(w, "data: %s\n\n", event)
				}
			}))
			defer server.Close()
			originalHost := anthropicclient.AnthropicAPIHost
			anthropicclient.AnthropicAPIHost = server.URL
			defer func() { anthropicclient.AnthropicAPIHost = originalHost }()
			ctx := llmcontracts.WithHistoryContinuation(context.Background())
			_, err := New(nil, nil, nil).Call(ctx, llmcontracts.AgentRequest{
				Operation: operation, Message: "do not replay this prompt",
				Agent:       models.LLMConfig{Provider: models.ProviderAnthropic, Model: "claude-opus-4-6", AuthMethod: models.AuthMethodAPIKey, APIKey: "test"},
				ChatHistory: []models.Execution{{PromptSent: "original task", Status: models.ExecCompleted}, {PromptSent: "actually only review", Status: models.ExecCompleted}, {Output: "file already written", Status: models.ExecCompleted}},
			}, t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}
		})
	}
}

func TestFollowupPreservesTrailingUserHistoryOnWire(t *testing.T) {
	const retained = "Keep the database migration backward compatible."
	const followup = "Continue with the implementation."
	for _, tc := range []struct {
		name    string
		history []models.Execution
	}{
		{
			name: "sole truncated user message",
			history: []models.Execution{{Status: models.ExecCompleted, ContextKind: "provider_truncated",
				ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"messages":[{"role":"user","content":"Keep the database migration backward compatible."}]}`}}}},
		},
		{
			name: "truncated conversation ending in user",
			history: []models.Execution{{Status: models.ExecCompleted, ContextKind: "provider_truncated",
				ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"messages":[{"role":"user","content":"Review the migration."},{"role":"assistant","content":"I reviewed it."},{"role":"user","content":"Keep the database migration backward compatible."}]}`}}}},
		},
		{
			name: "previous unanswered followup",
			history: []models.Execution{
				{Status: models.ExecCompleted, PromptSent: "Review the migration.", Output: "I reviewed it."},
				{Status: models.ExecFailed, PromptSent: retained},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests [][]anthropicclient.Message
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []anthropicclient.Message `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				requests = append(requests, body.Messages)
				w.Header().Set("Content-Type", "text/event-stream")
				for _, event := range []string{
					`{"type":"message_start","message":{"id":"done","model":"claude-opus-4-6","usage":{"input_tokens":8}}}`,
					`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
					`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done."}}`,
					`{"type":"content_block_stop","index":0}`,
					`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
					`{"type":"message_stop"}`,
				} {
					fmt.Fprintf(w, "data: %s\n\n", event)
				}
			}))
			defer server.Close()
			oldHost := anthropicclient.AnthropicAPIHost
			anthropicclient.AnthropicAPIHost = server.URL
			defer func() { anthropicclient.AnthropicAPIHost = oldHost }()

			_, err := New(nil, nil, nil).Call(context.Background(), llmcontracts.AgentRequest{
				Operation: llmcontracts.OperationStreaming, Followup: true, Message: followup,
				Agent:       models.LLMConfig{Provider: models.ProviderAnthropic, Model: "claude-opus-4-6", AuthMethod: models.AuthMethodAPIKey, APIKey: "test"},
				ChatHistory: tc.history,
			}, t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(requests) != 1 {
				t.Fatalf("requests = %d, want 1", len(requests))
			}
			messages := requests[0]
			var content strings.Builder
			for _, message := range messages {
				content.WriteString(message.Content)
				content.Write(message.ContentBlocks)
			}
			for _, text := range []string{retained, followup} {
				if strings.Count(content.String(), text) != 1 {
					t.Errorf("want exactly one %q in outgoing messages: %#v", text, messages)
				}
			}
			if len(messages) < 2 || messages[len(messages)-1].Role != "user" || messages[len(messages)-1].Content != followup {
				t.Fatalf("new followup must follow retained history: %#v", messages)
			}
		})
	}
}

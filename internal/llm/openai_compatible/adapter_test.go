package openai_compatible

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestAdapterCallDirectUsesConfiguredChatCompletionsEndpoint(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotTitle string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotTitle = r.Header.Get("X-Title")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":2,\"total_tokens\":11,\"prompt_tokens_details\":{\"cached_tokens\":3}}}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	adapter := New(nil, nil)
	res, err := adapter.Call(context.Background(), llmcontracts.AgentRequest{
		Operation: llmcontracts.OperationDirect,
		Message:   "Say hi",
		Agent: models.LLMConfig{
			Name:             "Compatible",
			Provider:         models.ProviderOpenAICompatible,
			AuthMethod:       models.AuthMethodAPIKey,
			Model:            "provider/model",
			APIKey:           "sk-compatible",
			BaseURL:          srv.URL + "/v1/",
			Transport:        "chat_completions",
			ExtraHeadersJSON: `{"X-Title":"OpenVibely"}`,
			ExtraBodyJSON:    `{"provider":{"order":["nvidia"]},"model":"evil","stream":false}`,
		},
	}, ".")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-compatible" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotTitle != "OpenVibely" {
		t.Fatalf("X-Title = %q", gotTitle)
	}
	if _, ok := gotBody["input"]; ok {
		t.Fatalf("unexpected Responses input field: %#v", gotBody)
	}
	if gotBody["model"] != "provider/model" || gotBody["stream"] != true {
		t.Fatalf("unexpected body: %#v", gotBody)
	}
	if gotBody["temperature"] != float64(0) {
		t.Fatalf("temperature = %#v, want explicit 0", gotBody["temperature"])
	}
	if _, ok := gotBody["provider"].(map[string]any); !ok {
		t.Fatalf("expected allowed provider extra body, got %#v", gotBody["provider"])
	}
	tools, ok := gotBody["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("expected default tools in compatible direct request, got %#v", gotBody["tools"])
	}
	if res.Output != "Hello" || res.Usage.InputTokens != 8 || res.Usage.OutputTokens != 2 || res.Usage.TotalTokens != 11 || res.Usage.CachedInputTokens != 3 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestCompatibleTemperatureOmitsMoonshotKimiOnly(t *testing.T) {
	tests := []struct {
		name      string
		agent     models.LLMConfig
		wantOmit  bool
		wantValue float64
	}{
		{
			name:     "moonshot kimi",
			agent:    models.LLMConfig{PresetSlug: "moonshot", Model: "kimi-k2.5", Temperature: 0},
			wantOmit: true,
		},
		{
			name:  "moonshot non-kimi",
			agent: models.LLMConfig{PresetSlug: "moonshot", Model: "moonshot-v1-128k", Temperature: 0},
		},
		{
			name:  "glm explicit zero",
			agent: models.LLMConfig{PresetSlug: "zai", Model: "glm-5", Temperature: 0},
		},
		{
			name:      "custom kimi-compatible endpoint",
			agent:     models.LLMConfig{Model: "kimi-k2.5", Temperature: 0.2},
			wantValue: 0.2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compatibleTemperature(tt.agent)
			if math.IsNaN(got) != tt.wantOmit {
				t.Fatalf("compatibleTemperature() = %v, want omit %v", got, tt.wantOmit)
			}
			if !tt.wantOmit && got != tt.wantValue {
				t.Fatalf("compatibleTemperature() = %v, want %v", got, tt.wantValue)
			}
		})
	}
}

func TestCompatibleRequestExtrasAddsKimiK3ReasoningEffort(t *testing.T) {
	_, body, err := compatibleRequestExtras(models.LLMConfig{
		Model:            "kimi-k3",
		ReasoningEffort:  "high",
		ExtraBodyJSON:    `{"reasoning_effort":"low","custom":true}`,
		ExtraHeadersJSON: "",
	})
	require.NoError(t, err)
	require.Equal(t, "high", body["reasoning_effort"])
	require.Equal(t, true, body["custom"])
}

func TestAdapterCallDirectRawPromptOmitsOpenVibelySystemPrompt(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"Advice\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	adapter := New(nil, nil)
	res, err := adapter.Call(context.Background(), llmcontracts.AgentRequest{
		Operation:       llmcontracts.OperationDirect,
		Message:         "REFERENCE PROMPT",
		DisableTools:    true,
		RawDirectPrompt: true,
		WorkDir:         "/secret/workdir",
		Agent: models.LLMConfig{
			Name:       "Compatible",
			Provider:   models.ProviderOpenAICompatible,
			AuthMethod: models.AuthMethodAPIKey,
			Model:      "provider/model",
			APIKey:     "sk-compatible",
			BaseURL:    srv.URL + "/v1/",
			Transport:  "chat_completions",
		},
	}, "/secret/workdir")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Output != "Advice" {
		t.Fatalf("output = %q", res.Output)
	}
	messages, ok := gotBody["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %#v", gotBody["messages"])
	}
	msg, _ := messages[0].(map[string]any)
	if msg["role"] != "user" || msg["content"] != "REFERENCE PROMPT" {
		t.Fatalf("message = %#v", msg)
	}
	if _, ok := gotBody["tools"]; ok {
		t.Fatalf("raw direct request should omit tools, got %#v", gotBody["tools"])
	}
	if strings.Contains(fmt.Sprint(gotBody), "OpenVibely") || strings.Contains(fmt.Sprint(gotBody), "/secret/workdir") {
		t.Fatalf("raw direct payload contains OpenVibely/workdir framing: %#v", gotBody)
	}
}

func TestAdapterChatWithRuntimeActionsUsesToolModeSystemPrompt(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"Task created.\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	ctx := llmcontracts.WithRuntimeTools(context.Background(), &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{
			Name: "create_task", Description: "create a task", Parameters: json.RawMessage(`{"type":"object"}`), Access: llmcontracts.RuntimeToolAccessWrite,
		}},
	})
	adapter := New(nil, nil)
	_, err := adapter.Call(ctx, llmcontracts.AgentRequest{
		Ctx: ctx, Operation: llmcontracts.OperationStreaming, Message: "Create a task", ChatMode: models.ChatModeOrchestrate,
		Agent: models.LLMConfig{
			Name: "Compatible", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey,
			Model: "provider/model", APIKey: "sk-compatible", BaseURL: srv.URL + "/v1/", Transport: "chat_completions",
		},
	}, ".")
	require.NoError(t, err)

	messages, ok := gotBody["messages"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, messages)
	system, ok := messages[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "system", system["role"])
	content, _ := system["content"].(string)
	require.Contains(t, content, llmprompt.ChatActionToolModeInstructions)
	require.Contains(t, content, "Available action tools: create_task")
	require.NotContains(t, content, "The ONLY way to create a task is by outputting a [CREATE_TASK] block")
}

func TestAdapterChatWithoutRuntimeActionsReportsCapabilityLimitation(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	adapter := New(nil, nil)
	_, err := adapter.Call(context.Background(), llmcontracts.AgentRequest{
		Operation: llmcontracts.OperationStreaming, Message: "Create a task", ChatMode: models.ChatModeOrchestrate,
		Agent: models.LLMConfig{
			Name: "Compatible", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey,
			Model: "provider/model", APIKey: "sk-compatible", BaseURL: srv.URL + "/v1/", Transport: "chat_completions",
		},
	}, ".")
	require.NoError(t, err)

	messages, ok := gotBody["messages"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, messages)
	system, ok := messages[0].(map[string]any)
	require.True(t, ok)
	content, _ := system["content"].(string)
	require.Contains(t, content, llmprompt.ChatActionUnavailableInstructions)
	require.NotContains(t, content, "[CREATE_TASK]")
	require.NotContains(t, content, llmprompt.ChatActionToolModeInstructions)
}

func TestAdapterTaskFollowupWithoutRuntimeActionsReportsCapabilityLimitation(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	adapter := New(nil, nil)
	_, err := adapter.Call(context.Background(), llmcontracts.AgentRequest{
		Operation: llmcontracts.OperationStreaming, Message: "Create a follow-up task", Followup: true, ChatMode: models.ChatModeOrchestrate,
		Agent: models.LLMConfig{
			Name: "Compatible", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey,
			Model: "provider/model", APIKey: "sk-compatible", BaseURL: srv.URL + "/v1/", Transport: "chat_completions",
		},
	}, ".")
	require.NoError(t, err)

	messages, ok := gotBody["messages"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, messages)
	system, ok := messages[0].(map[string]any)
	require.True(t, ok)
	content, _ := system["content"].(string)
	require.Contains(t, content, llmprompt.ChatActionUnavailableInstructions)
	require.NotContains(t, content, "[CREATE_TASK]")
	require.NotContains(t, content, llmprompt.ChatActionToolModeInstructions)
}

func TestAdapterPlanWithoutRuntimeActionsRemainsReadOnlyWithoutActionMode(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	adapter := New(nil, nil)
	_, err := adapter.Call(context.Background(), llmcontracts.AgentRequest{
		Operation: llmcontracts.OperationStreaming, Message: "Plan a task", ChatMode: models.ChatModePlan,
		Agent: models.LLMConfig{
			Name: "Compatible", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey,
			Model: "provider/model", APIKey: "sk-compatible", BaseURL: srv.URL + "/v1/", Transport: "chat_completions",
		},
	}, ".")
	require.NoError(t, err)

	messages, ok := gotBody["messages"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, messages)
	system, ok := messages[0].(map[string]any)
	require.True(t, ok)
	content, _ := system["content"].(string)
	require.Contains(t, content, "PLAN MODE (read-only)")
	require.NotContains(t, content, llmprompt.ChatActionUnavailableInstructions)
	require.NotContains(t, content, llmprompt.ChatActionToolModeInstructions)
}

func TestAdapterTaskWithRuntimeActionsUsesToolModePrompt(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"Task complete.\\n[STATUS: SUCCESS]\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	ctx := llmcontracts.WithRuntimeTools(context.Background(), &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{
			Name: "create_task", Description: "create a task", Parameters: json.RawMessage(`{"type":"object"}`), Access: llmcontracts.RuntimeToolAccessWrite,
		}},
	})
	adapter := New(nil, nil)
	_, err := adapter.Call(ctx, llmcontracts.AgentRequest{
		Ctx: ctx, Operation: llmcontracts.OperationTask, Message: "Investigate the issue",
		Agent: models.LLMConfig{
			Name: "Compatible", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey,
			Model: "provider/model", APIKey: "sk-compatible", BaseURL: srv.URL + "/v1/", Transport: "chat_completions",
		},
	}, ".")
	require.NoError(t, err)

	messages, ok := gotBody["messages"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, messages)
	user, ok := messages[len(messages)-1].(map[string]any)
	require.True(t, ok)
	content, _ := user["content"].(string)
	require.Contains(t, content, "TASK CREATION TOOL MODE")
	require.Contains(t, content, "Available runtime task tools: create_task")
	require.NotContains(t, content, "This is the ONLY way to create a task")
	require.NotContains(t, content, "To create a task, output this format")
}

func TestAdapterTaskWithoutRuntimeActionsDoesNotAdvertiseLegacyMutationMarkers(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"Task complete.\\n[STATUS: SUCCESS]\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	adapter := New(nil, nil)
	_, err := adapter.Call(context.Background(), llmcontracts.AgentRequest{
		Operation: llmcontracts.OperationTask, Message: "Investigate the issue",
		Agent: models.LLMConfig{
			Name: "Compatible", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey,
			Model: "provider/model", APIKey: "sk-compatible", BaseURL: srv.URL + "/v1/", Transport: "chat_completions",
		},
	}, ".")
	require.NoError(t, err)

	messages, ok := gotBody["messages"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, messages)
	user, ok := messages[len(messages)-1].(map[string]any)
	require.True(t, ok)
	content, _ := user["content"].(string)
	require.Contains(t, content, llmprompt.ChatActionUnavailableInstructions)
	require.NotContains(t, content, "[CREATE_TASK]")
	require.NotContains(t, content, "TASK CREATION TOOL MODE")
}

func TestAdapterToolCallReplaysToolResult(t *testing.T) {
	requests := 0
	var secondMessages []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]any
		_ = json.Unmarshal(body, &reqBody)
		if requests == 2 {
			secondMessages, _ = reqBody["messages"].([]any)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			_, _ = w.Write([]byte(
				"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Need memory.\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"memory_view\",\"arguments\":\"{\\\"handle\\\":\"}}]}}]}\n\n" +
					"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"usage.md\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
					"data: [DONE]\n\n",
			))
			return
		}
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"Read it.\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	ctx := llmcontracts.WithRuntimeTools(context.Background(), &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "memory_view", Description: "read memory", Parameters: json.RawMessage(`{"type":"object"}`), Access: llmcontracts.RuntimeToolAccessRead}},
		Executor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			if name != "memory_view" {
				t.Fatalf("unexpected tool name %q", name)
			}
			return "memory contents", true, false, nil
		},
	})

	adapter := New(nil, nil)
	res, err := adapter.Call(ctx, llmcontracts.AgentRequest{
		Ctx:       ctx,
		Operation: llmcontracts.OperationDirect,
		Message:   "Use memory",
		Agent: models.LLMConfig{
			Name:       "Compatible",
			Provider:   models.ProviderOpenAICompatible,
			AuthMethod: models.AuthMethodAPIKey,
			Model:      "provider/model",
			APIKey:     "sk-compatible",
			BaseURL:    srv.URL + "/v1/",
			Transport:  "chat_completions",
		},
	}, ".")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if res.Output != "Read it." {
		t.Fatalf("output = %q", res.Output)
	}
	foundToolMessage := false
	foundReasoningMessage := false
	for _, raw := range secondMessages {
		msg, _ := raw.(map[string]any)
		if msg["role"] == "tool" && msg["tool_call_id"] == "call_1" && msg["content"] == "memory contents" {
			foundToolMessage = true
		}
		if msg["role"] == "assistant" && msg["reasoning_content"] == "Need memory." {
			foundReasoningMessage = true
		}
	}
	if !foundToolMessage {
		t.Fatalf("tool result message not replayed: %#v", secondMessages)
	}
	if !foundReasoningMessage {
		t.Fatalf("assistant reasoning content not replayed: %#v", secondMessages)
	}
}

func TestBuildClientHistoryPreservesReasoningContent(t *testing.T) {
	history := buildClientHistory([]models.Execution{{
		PromptSent:       "question",
		Output:           "answer",
		ReasoningContent: "private thought",
		Status:           models.ExecCompleted,
	}})
	require.Len(t, history, 2)
	require.Equal(t, "assistant", history[1].Role)
	require.Equal(t, "answer", history[1].Content)
	require.Equal(t, "private thought", history[1].ReasoningContent)
}

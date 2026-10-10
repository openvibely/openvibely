package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
)

func TestMaxTokensErrorIsCategorized(t *testing.T) {
	if !llmcontracts.ErrorIs(errMaxTokens, llmcontracts.ErrorOutputTokenLimitReached) {
		t.Fatalf("errMaxTokens category missing: %v", errMaxTokens)
	}
}

func TestAppendToolModeSystemPromptCoversChatAndTaskFollowupsAndPreservesPlan(t *testing.T) {
	noTools := appendToolModeSystemPrompt("base", nil, models.ChatModeOrchestrate, false)
	if noTools != "base" {
		t.Fatalf("no-tool chat prompt must be unchanged: %q", noTools)
	}

	rt := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "create_task"}}}
	capable := appendToolModeSystemPrompt("base", rt, models.ChatModeOrchestrate, false)
	if !strings.Contains(capable, llmprompt.ChatActionToolModeInstructions) || strings.Contains(capable, "Available action tools") {
		t.Fatalf("tool-capable chat prompt missing concrete runtime guidance: %q", capable)
	}

	taskThread := appendToolModeSystemPrompt("base", rt, models.ChatModeOrchestrate, true)
	if taskThread != "base" {
		t.Fatalf("task-thread prompt must not add a tool note: %q", taskThread)
	}
	if got := appendToolModeSystemPrompt("base", nil, models.ChatModeOrchestrate, true); got != "base" {
		t.Fatalf("no-tool task-thread prompt must not claim tools are unavailable: %q", got)
	}

	plan := appendToolModeSystemPrompt("base", nil, models.ChatModePlan, false)
	if plan != "base" {
		t.Fatalf("Plan prompt received action-mode guidance: %q", plan)
	}
}

func TestBuildAnthropicRuntimeAdvertisesAndExecutesMCPToolsOnce(t *testing.T) {
	var initializeCalls atomic.Int64
	var toolsListCalls atomic.Int64
	var toolsCallCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      int64           `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode MCP request: %v", err)
			http.Error(w, "bad MCP request", http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "initialize":
			initializeCalls.Add(1)
			writeAnthropicMCPResult(t, w, req.ID, map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}})
		case "tools/list":
			toolsListCalls.Add(1)
			writeAnthropicMCPResult(t, w, req.ID, map[string]any{"tools": []map[string]any{{"name": "screenshot", "description": "Capture browser", "inputSchema": map[string]any{"type": "object"}}}})
		case "tools/call":
			toolsCallCalls.Add(1)
			writeAnthropicMCPResult(t, w, req.ID, map[string]any{"content": []map[string]string{{"type": "text", "text": "captured"}}, "isError": false})
		default:
			t.Errorf("unexpected MCP method %q", req.Method)
			http.Error(w, "unexpected MCP method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	agentDef := &models.Agent{MCPServers: []models.MCPServerConfig{{Name: "browser", Type: "http", URL: server.URL}}}
	extraTools, executeTool, allowTool, cleanup := buildAnthropicRuntime(context.Background(), t.TempDir(), agentDef)
	defer cleanup()

	if initializeCalls.Load() != 1 || toolsListCalls.Load() != 1 {
		t.Fatalf("MCP setup counts initialize=%d tools/list=%d, want 1 each", initializeCalls.Load(), toolsListCalls.Load())
	}
	if len(extraTools) != 1 || extraTools[0].Name != "browser__screenshot" {
		t.Fatalf("unexpected Anthropic MCP tool definitions: %#v", extraTools)
	}
	if !allowTool("browser__screenshot") {
		t.Fatal("MCP tool was not allowed by runtime filter")
	}
	out, isErr, err := executeTool(context.Background(), "browser__screenshot", json.RawMessage(`{"url":"https://example.test"}`))
	if err != nil || isErr || out != "captured" {
		t.Fatalf("MCP execution output=%q isErr=%v err=%v", out, isErr, err)
	}
	if toolsCallCalls.Load() != 1 {
		t.Fatalf("MCP tools/call count = %d, want 1", toolsCallCalls.Load())
	}
}

func writeAnthropicMCPResult(t *testing.T, w http.ResponseWriter, id int64, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil {
		t.Fatalf("write MCP response: %v", err)
	}
}

func TestCallStreamingZeroHistoryFollowupUsesChatAssembly(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, event := range []string{
			`{"type":"message_start","message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":4}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"follow-up response"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer server.Close()

	origHost := anthropicclient.AnthropicAPIHost
	anthropicclient.AnthropicAPIHost = server.URL
	defer func() { anthropicclient.AnthropicAPIHost = origHost }()

	adapter := New(nil, nil, nil)
	_, err := adapter.Call(context.Background(), llmcontracts.AgentRequest{
		Operation:         llmcontracts.OperationStreaming,
		Message:           "Continue the task",
		Followup:          true,
		ChatMode:          models.ChatModeOrchestrate,
		ChatSystemContext: "FOLLOWUP_CONTEXT_SENTINEL",
		Agent: models.LLMConfig{
			Name:            "Claude API",
			Provider:        models.ProviderAnthropic,
			Model:           "claude-sonnet-5-5",
			ReasoningEffort: "medium",
			AuthMethod:      models.AuthMethodAPIKey,
			APIKey:          "test-key",
		},
	}, ".", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	payload := fmt.Sprint(gotBody)
	if !strings.Contains(payload, "# Task Follow-up Constraints") || !strings.Contains(payload, "FOLLOWUP_CONTEXT_SENTINEL") {
		t.Fatalf("zero-history follow-up did not use Chat assembly: %#v", gotBody)
	}
	if strings.Contains(payload, llmprompt.ChatActionToolModeInstructions) {
		t.Fatalf("zero-history follow-up received Chat action guidance: %#v", gotBody)
	}
	outputConfig, ok := gotBody["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != "medium" {
		t.Fatalf("output_config = %#v, want effort medium", gotBody["output_config"])
	}
	if gotBody["model"] != "claude-sonnet-5-5" {
		t.Fatalf("wire model = %v, want claude-sonnet-5-5", gotBody["model"])
	}
}

func TestCallHaiku55ChatAndTaskStreamingSendSupportedProviderRequest(t *testing.T) {
	const requestCount = 10
	requestBodies := make(chan map[string]any, requestCount)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			http.Error(w, "read request", http.StatusBadRequest)
			return
		}
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(w, "decode request", http.StatusBadRequest)
			return
		}
		requestBodies <- request
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, event := range []string{
			`{"type":"message_start","message":{"id":"msg_haiku","model":"claude-haiku-5-5","usage":{"input_tokens":4}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer server.Close()

	originalHost := anthropicclient.AnthropicAPIHost
	anthropicclient.AnthropicAPIHost = server.URL
	defer func() { anthropicclient.AnthropicAPIHost = originalHost }()

	adapter := New(nil, nil, nil)
	for _, operation := range []llmcontracts.Operation{llmcontracts.OperationStreaming, llmcontracts.OperationTask} {
		for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
			t.Run(string(operation)+"/"+effort, func(t *testing.T) {
				req := llmcontracts.AgentRequest{
					Operation: operation,
					Message:   "Summarize this task",
					Agent: models.LLMConfig{
						Name:            "Haiku 5.5",
						Provider:        models.ProviderAnthropic,
						Model:           "claude-haiku-5-5",
						ReasoningEffort: effort,
						ContextWindow:   200000,
						Temperature:     0.8,
						AuthMethod:      models.AuthMethodAPIKey,
						APIKey:          "test-key",
					},
				}
				if operation == llmcontracts.OperationStreaming {
					req.ChatHistory = []models.Execution{}
				}
				if _, err := adapter.Call(context.Background(), req, ".", nil); err != nil {
					t.Fatalf("Call: %v", err)
				}
				request := <-requestBodies
				if request["model"] != "claude-haiku-5-5" {
					t.Fatalf("request model = %v, want exact claude-haiku-5-5", request["model"])
				}
				outputConfig, ok := request["output_config"].(map[string]any)
				if !ok || outputConfig["effort"] != effort {
					t.Fatalf("output_config = %#v, want effort %q", request["output_config"], effort)
				}
				thinking, ok := request["thinking"].(map[string]any)
				if !ok || thinking["type"] != "adaptive" || len(thinking) != 1 {
					t.Fatalf("thinking = %#v, want only type=adaptive", request["thinking"])
				}
				for _, unsupported := range []string{"temperature", "top_p", "top_k"} {
					if _, ok := request[unsupported]; ok {
						t.Fatalf("request unexpectedly contains unsupported %s: %v", unsupported, request[unsupported])
					}
				}
				if _, ok := thinking["budget_tokens"]; ok {
					t.Fatalf("request unexpectedly contains legacy thinking budget: %v", thinking)
				}
				maxTokens, ok := request["max_tokens"].(float64)
				if !ok || maxTokens > 128000 {
					t.Fatalf("max_tokens = %v, want no more than 128000", request["max_tokens"])
				}
			})
		}
	}
}

func TestCallDirectReturnsErrorOnRefusalStopReason(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		events := []string{
			`{"type":"message_start","message":{"id":"msg_1","model":"claude-fable-5","usage":{"input_tokens":10}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I can’t help with that."}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":6}}`,
			`{"type":"message_stop"}`,
		}
		for _, evt := range events {
			fmt.Fprintf(w, "data: %s\n\n", evt)
		}
	}))
	defer server.Close()

	origHost := anthropicclient.AnthropicAPIHost
	anthropicclient.AnthropicAPIHost = server.URL
	defer func() { anthropicclient.AnthropicAPIHost = origHost }()

	adapter := New(nil, nil, nil)
	output, usage, err := adapter.callDirect(context.Background(), "test", nil, models.LLMConfig{
		Name:            "Sonnet 4.5",
		Provider:        models.ProviderAnthropic,
		Model:           "claude-sonnet-4-5-20250929",
		ReasoningEffort: "low",
		AuthMethod:      models.AuthMethodAPIKey,
		APIKey:          "test-key",
	}, ".", "", nil, nil, nil, true, true, false, false)
	if err == nil {
		t.Fatal("expected refusal stop_reason to return an error")
	}
	if !strings.Contains(err.Error(), "stop_reason=refusal") {
		t.Fatalf("error = %v, want refusal stop reason", err)
	}
	if !strings.Contains(output, "help") {
		t.Fatalf("output = %q, want refusal text preserved", output)
	}
	if usage.OutputTokens != 6 {
		t.Fatalf("output tokens = %d, want 6", usage.OutputTokens)
	}
	if _, ok := gotBody["output_config"]; ok {
		t.Fatalf("unsupported Sonnet 4.5 effort must be omitted, got %#v", gotBody["output_config"])
	}
}

func TestCallDirectOperationUsesRuntimeToolsAndDefaultFraming(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, evt := range []string{
			`{"type":"message_start","message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":8,"cache_creation_input_tokens":1,"cache_read_input_tokens":2}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"direct result"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", evt)
		}
	}))
	defer server.Close()

	origHost := anthropicclient.AnthropicAPIHost
	anthropicclient.AnthropicAPIHost = server.URL
	defer func() { anthropicclient.AnthropicAPIHost = origHost }()

	ctx := llmcontracts.WithRuntimeTools(context.Background(), &llmcontracts.RuntimeTools{
		Definitions:      []llmcontracts.RuntimeToolDefinition{{Name: "create_task", Description: "Create a task", Parameters: json.RawMessage(`{"type":"object"}`)}},
		SkipDefaultTools: true,
	})
	adapter := New(nil, nil, nil)
	result, err := adapter.Call(ctx, llmcontracts.AgentRequest{
		Operation:           llmcontracts.OperationDirect,
		Message:             "solve it",
		ProjectInstructions: "project rules",
		Agent: models.LLMConfig{
			Name:            "Claude API",
			Provider:        models.ProviderAnthropic,
			Model:           " CLAUDE-OPUS-5 ",
			ReasoningEffort: "low",
			AuthMethod:      models.AuthMethodAPIKey,
			APIKey:          "test-key",
		},
	}, "/repo/worktree", nil)
	if err != nil {
		t.Fatalf("Call direct: %v", err)
	}
	if result.Output != "direct result" || result.Usage.InputTokens != 8 || result.Usage.OutputTokens != 3 {
		t.Fatalf("result = %#v", result)
	}
	if gotBody["model"] != "claude-opus-5" {
		t.Fatalf("wire model = %v, want canonical ID", gotBody["model"])
	}
	if system := fmt.Sprint(gotBody["system"]); !strings.Contains(system, "project rules") || !strings.Contains(system, llmprompt.AnthropicAgentSystemPrompt) {
		t.Fatalf("direct call omitted default system framing: %#v", gotBody["system"])
	}
	if !strings.Contains(fmt.Sprint(gotBody["messages"]), "solve it") {
		t.Fatalf("direct call omitted task prompt: %#v", gotBody["messages"])
	}
	if tools := fmt.Sprint(gotBody["tools"]); !strings.Contains(tools, "create_task") || strings.Contains(tools, "Bash") {
		t.Fatalf("runtime/default tool set = %s", tools)
	}
}

func TestCallDirectRawPromptOmitsOpenVibelySystemTaskPromptAndTools(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		events := []string{
			`{"type":"message_start","message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":4}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"advice"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		}
		for _, evt := range events {
			fmt.Fprintf(w, "data: %s\n\n", evt)
		}
	}))
	defer server.Close()

	origHost := anthropicclient.AnthropicAPIHost
	anthropicclient.AnthropicAPIHost = server.URL
	defer func() { anthropicclient.AnthropicAPIHost = origHost }()

	adapter := New(nil, nil, nil)
	output, _, err := adapter.callDirect(context.Background(), "REFERENCE PROMPT", nil, models.LLMConfig{
		Name:            "Claude API",
		Provider:        models.ProviderAnthropic,
		Model:           "claude-opus-5",
		ReasoningEffort: "max",
		AuthMethod:      models.AuthMethodAPIKey,
		APIKey:          "test-key",
	}, "/secret/workdir", "project instructions", nil, nil, nil, true, true, true, false)
	if err != nil {
		t.Fatalf("callDirect: %v", err)
	}
	if output != "advice" {
		t.Fatalf("output = %q", output)
	}
	if _, ok := gotBody["system"]; ok {
		t.Fatalf("raw direct request should omit system prompt, got %#v", gotBody["system"])
	}
	if _, ok := gotBody["tools"]; ok {
		t.Fatalf("raw direct request should omit tools, got %#v", gotBody["tools"])
	}
	outputConfig, ok := gotBody["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != "max" {
		t.Fatalf("output_config = %#v, want effort max", gotBody["output_config"])
	}
	messages, ok := gotBody["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %#v", gotBody["messages"])
	}
	msg, _ := messages[0].(map[string]any)
	if msg["role"] != "user" || msg["content"] != "REFERENCE PROMPT" {
		t.Fatalf("message = %#v", msg)
	}
	if strings.Contains(fmt.Sprint(gotBody), "OpenVibely") || strings.Contains(fmt.Sprint(gotBody), "Task:") || strings.Contains(fmt.Sprint(gotBody), "/secret/workdir") {
		t.Fatalf("raw direct payload contains OpenVibely/task/workdir framing: %#v", gotBody)
	}
}

func TestToolSecondaryInfo_LongBashPreservesLaterContext(t *testing.T) {
	input := map[string]any{
		"command": "cd /Users/dubee/go/src/github.com/openvibely/openvibely/.worktrees/task_6a40e9f8fefa53ac8d203aa3fd3a70be && rg -n \"toolSecondaryInfo|truncateToolSecondary|task thread\" internal pkg web/templates/components/chat_shared.templ",
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	got := toolSecondaryInfo("Bash", raw)
	if !strings.HasPrefix(got, "$ cd ") {
		t.Fatalf("expected bash detail prefix, got %q", got)
	}
	if !strings.Contains(got, "chat_shared.templ") {
		t.Fatalf("expected later command context to survive truncation, got %q", got)
	}
}

func TestToolSecondaryInfo_LongGrepPreservesLaterPatternContext(t *testing.T) {
	input := map[string]any{
		"pattern": "len\\(cmd\\) >|len\\(p\\) >|toolSecondaryInfo|truncateToolSecondary|task thread|chat_shared\\.templ|stream/events\\.go",
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	got := toolSecondaryInfo("Grep", raw)
	if !strings.Contains(got, "chat_shared") {
		t.Fatalf("expected later grep context to survive truncation, got %q", got)
	}
}

func TestComposeTaskRuntimeToolFilter_AllowsDefaultToolsWithoutRuntimeTools(t *testing.T) {
	base := func(name string) bool {
		switch name {
		case "read_file", "list_files", "grep_search", "bash":
			return true
		default:
			return false
		}
	}

	filter := composeTaskRuntimeToolFilter(base, nil)
	for _, name := range []string{"read_file", "list_files", "grep_search", "bash"} {
		if !filter(name) {
			t.Fatalf("expected task tool %q to remain allowed without runtime action tools", name)
		}
	}
	if filter("unknown_tool") {
		t.Fatalf("expected base filter denial to be preserved")
	}
}

func TestAgentSkipDefaultToolsBlocksDefaultsButKeepsRuntimeMemoryTool(t *testing.T) {
	agent := &models.Agent{ToolConfig: models.AgentToolConfig{SkipDefaultTools: true}}
	if agentAllowsBuiltInTool(agent, "list_files") || agentAllowsBuiltInTool(agent, "bash") || agentAllowsBuiltInTool(agent, "read_file") {
		t.Fatalf("expected agent SkipDefaultTools to block default built-in tools")
	}

	rt := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "memory_view", Access: llmcontracts.RuntimeToolAccessRead}},
		Filter: func(name string) (bool, bool) {
			if name == "memory_view" {
				return true, true
			}
			return false, true
		},
	}
	filter := composeTaskRuntimeToolFilter(func(name string) bool { return agentAllowsBuiltInTool(agent, name) }, rt)
	if !filter("memory_view") {
		t.Fatalf("expected selected memory runtime tool to remain available")
	}
	if filter("list_files") || filter("bash") || filter("read_file") {
		t.Fatalf("expected default tools to stay blocked")
	}
}

func TestTaskStreamingRuntimeToolComposition_AllowsScopedFilesRuntimeTools(t *testing.T) {
	rt := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "list_files"}},
		Executor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			if name == "list_files" {
				return "[]", true, false, nil
			}
			return "", false, false, nil
		},
		Filter: func(name string) (bool, bool) {
			if name == "list_files" {
				return true, true
			}
			return false, false
		},
		SkipDefaultTools: true,
	}

	extraTools := runtimeAnthropicTools(rt)
	if len(extraTools) != 1 || extraTools[0].Name != "list_files" {
		t.Fatalf("runtimeAnthropicTools() = %#v, want list_files", extraTools)
	}

	exec := composeRuntimeToolExecutor(nil, rt)
	out, isError, err := exec(context.Background(), "list_files", json.RawMessage(`{}`))
	if err != nil || isError || out != "[]" {
		t.Fatalf("runtime executor = (%q, %v, %v), want non-error [] nil", out, isError, err)
	}

	filter := composeTaskRuntimeToolFilter(nil, rt)
	if !filter("list_files") {
		t.Fatalf("expected runtime scoped file tool to be allowed")
	}
	if filter("Read") || filter("Bash") {
		t.Fatalf("expected default tools to be hidden when SkipDefaultTools is true")
	}
}

func TestShouldSkipDefaultToolsForChatMode(t *testing.T) {
	rt := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{
			{Name: "create_task"},
		},
	}

	if !shouldSkipDefaultToolsForChatMode(false, models.ChatModeOrchestrate, rt) {
		t.Fatalf("expected default tools to be skipped for orchestrate chat with runtime action tools")
	}
	if shouldSkipDefaultToolsForChatMode(true, models.ChatModeOrchestrate, rt) {
		t.Fatalf("did not expect skip for task follow-up mode")
	}
	if shouldSkipDefaultToolsForChatMode(false, models.ChatModePlan, rt) {
		t.Fatalf("did not expect skip for plan mode")
	}
	if shouldSkipDefaultToolsForChatMode(false, models.ChatModeOrchestrate, nil) {
		t.Fatalf("did not expect skip without runtime tools")
	}
}

func TestDirectRuntimeToolsDoNotRequestSkippingDefaultsByDefault(t *testing.T) {
	rt := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "write_file"}},
	}
	if rt.SkipDefaultTools {
		t.Fatalf("runtime tools should not skip defaults unless explicitly requested")
	}
	rt.SkipDefaultTools = true
	if !rt.SkipDefaultTools {
		t.Fatalf("expected explicit skip-default flag to be settable for scoped tool sessions")
	}
}

func TestResolveChatToolPolicy(t *testing.T) {
	rt := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{
			{Name: "create_task"},
		},
	}

	tests := []struct {
		name   string
		follow bool
		mode   models.ChatMode
		rt     *llmcontracts.RuntimeTools
		wantD  bool
		wantS  bool
	}{
		{
			name:   "orchestrate without runtime tools disables function tools",
			follow: false,
			mode:   models.ChatModeOrchestrate,
			rt:     nil,
			wantD:  true,
			wantS:  false,
		},
		{
			name:   "orchestrate with runtime tools skips defaults without disabling tools",
			follow: false,
			mode:   models.ChatModeOrchestrate,
			rt:     rt,
			wantD:  false,
			wantS:  true,
		},
		{
			name:   "plan mode keeps tools enabled and defaults visible",
			follow: false,
			mode:   models.ChatModePlan,
			rt:     rt,
			wantD:  false,
			wantS:  false,
		},
		{
			name:   "task follow-up keeps tools enabled and defaults visible",
			follow: true,
			mode:   models.ChatModeOrchestrate,
			rt:     rt,
			wantD:  false,
			wantS:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotDisable, gotSkip := resolveChatToolPolicy(tc.follow, tc.mode, tc.rt)
			if gotDisable != tc.wantD || gotSkip != tc.wantS {
				t.Fatalf("resolveChatToolPolicy(follow=%v, mode=%s, rt_nil=%v) = (disable=%v, skip=%v), want (disable=%v, skip=%v)",
					tc.follow, tc.mode, tc.rt == nil, gotDisable, gotSkip, tc.wantD, tc.wantS)
			}
		})
	}
}

// Lifecycle hooks return structured JSON. They must keep their own agent
// prompt but drop the shared coding-agent system prompt, the take-direct-action
// header, and provider web tools, all of which are wasted context for them.
func TestCallDirectLifecycleHookDropsCodingAgentFraming(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		events := []string{
			`{"type":"message_start","message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":4}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"{}"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		}
		for _, evt := range events {
			fmt.Fprintf(w, "data: %s\n\n", evt)
		}
	}))
	defer server.Close()

	origHost := anthropicclient.AnthropicAPIHost
	anthropicclient.AnthropicAPIHost = server.URL
	defer func() { anthropicclient.AnthropicAPIHost = origHost }()

	adapter := New(nil, nil, nil)
	_, _, err := adapter.callDirect(context.Background(), "HOOK PROMPT", nil, models.LLMConfig{
		Name:       "Claude API",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-opus-5",
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "test-key",
	}, "/repo", "AGENT OWN PROMPT", nil, nil, nil, true, true, false, true)
	if err != nil {
		t.Fatalf("callDirect: %v", err)
	}

	systemBlocks, _ := json.Marshal(gotBody["system"])
	system := string(systemBlocks)
	if !strings.Contains(system, "AGENT OWN PROMPT") {
		t.Fatalf("lifecycle hook must keep its own agent prompt, got %s", system)
	}
	if strings.Contains(system, "expert software engineer") || strings.Contains(system, "You are an interactive agent") {
		t.Fatalf("lifecycle hook must not receive the coding-agent system prompt, got %s", system)
	}

	messages, _ := json.Marshal(gotBody["messages"])
	if strings.Contains(string(messages), "Do not use plan mode") {
		t.Fatalf("lifecycle hook must not receive the task prompt header, got %s", messages)
	}

	tools, _ := json.Marshal(gotBody["tools"])
	if strings.Contains(string(tools), "web_search") || strings.Contains(string(tools), "web_fetch") {
		t.Fatalf("lifecycle hook must not receive provider web tools, got %s", tools)
	}
}

func TestCallStreamingSonnet55PreservesEffortAndThinkingToolHistory(t *testing.T) {
	var gotBodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var gotBody map[string]any
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		gotBodies = append(gotBodies, gotBody)
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Fatalf("x-api-key = %q, want test-key", got)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if len(gotBodies) == 1 {
			for _, evt := range []string{
				`{"type":"message_start","message":{"id":"msg_sonnet55_tool","model":"claude-sonnet-5-5","usage":{"input_tokens":11}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"consider the task"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sonnet55-thinking-signature"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_sonnet55","name":"create_task","input":{"title":"ship it"}}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
				`{"type":"message_stop"}`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", evt)
			}
			return
		}
		for _, evt := range []string{
			`{"type":"message_start","message":{"id":"msg_sonnet55_final","model":"claude-sonnet-5-5","usage":{"input_tokens":7}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"streamed Sonnet 5.5 answer"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", evt)
		}
	}))
	defer server.Close()

	origHost := anthropicclient.AnthropicAPIHost
	anthropicclient.AnthropicAPIHost = server.URL
	defer func() { anthropicclient.AnthropicAPIHost = origHost }()

	ctx := llmcontracts.WithRuntimeTools(context.Background(), &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "create_task", Description: "Create task", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Executor: func(_ context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			if name != "create_task" || !strings.Contains(string(input), "ship it") {
				t.Fatalf("runtime tool call = %s %s", name, string(input))
			}
			return `{"created":true}`, true, false, nil
		},
		SkipDefaultTools: true,
	})

	adapter := New(nil, nil, nil)
	result, err := adapter.Call(ctx, llmcontracts.AgentRequest{
		Operation:           llmcontracts.OperationTask,
		Message:             "Finish task",
		ExecID:              "exec-sonnet55-task",
		ProjectInstructions: "project rules",
		Agent: models.LLMConfig{
			Name:            "Claude Sonnet 5.5",
			Provider:        models.ProviderAnthropic,
			Model:           "claude-sonnet-5-5",
			ReasoningEffort: "xhigh",
			AuthMethod:      models.AuthMethodAPIKey,
			APIKey:          "test-key",
		},
	}, "/repo/worktree", nil)
	if err != nil {
		t.Fatalf("Call task: %v", err)
	}
	if !strings.Contains(result.Output, "streamed Sonnet 5.5 answer") || !strings.Contains(result.Output, "consider the task") {
		t.Fatalf("stream output missing answer or thinking: %q", result.Output)
	}
	if len(gotBodies) != 2 {
		t.Fatalf("Anthropic request count = %d, want tool turn and final turn", len(gotBodies))
	}
	firstPayload := fmt.Sprint(gotBodies[0])
	if !strings.Contains(firstPayload, "Finish task") || !strings.Contains(firstPayload, "project rules") || !strings.Contains(firstPayload, "create_task") {
		t.Fatalf("task request body missing prompt/system/runtime tools: %#v", gotBodies[0])
	}
	if !strings.Contains(fmt.Sprint(gotBodies[0]["system"]), llmprompt.AnthropicAgentSystemPrompt) {
		t.Fatal("task request omitted Anthropic base prompt")
	}
	if !strings.Contains(firstPayload, "If you recovered and completed the requested outcome, report success") ||
		!strings.Contains(firstPayload, "[STATUS: FAILED | <describe what prevented completion>]") {
		t.Fatalf("task request missing provider-neutral outcome status contract: %#v", gotBodies[0]["messages"])
	}
	if strings.Contains(firstPayload, "Bash") {
		t.Fatalf("SkipDefaultTools should omit default tools, got %#v", gotBodies[0]["tools"])
	}
	for i, body := range gotBodies {
		if body["model"] != "claude-sonnet-5-5" {
			t.Errorf("request %d model = %v, want claude-sonnet-5-5", i+1, body["model"])
		}
		if body["max_tokens"] != float64(64000) {
			t.Errorf("request %d max_tokens = %v, want 64000", i+1, body["max_tokens"])
		}
		if outputConfig, ok := body["output_config"].(map[string]any); !ok || outputConfig["effort"] != "xhigh" {
			t.Errorf("request %d output_config = %#v, want effort xhigh", i+1, body["output_config"])
		}
		if thinking, ok := body["thinking"].(map[string]any); !ok || thinking["type"] != "adaptive" {
			t.Errorf("request %d thinking = %#v, want adaptive", i+1, body["thinking"])
		}
		for _, unsupported := range []string{"temperature", "top_p", "top_k", "tool_choice"} {
			if _, ok := body[unsupported]; ok {
				t.Errorf("request %d unexpectedly included %q: %#v", i+1, unsupported, body[unsupported])
			}
		}
	}

	messages, ok := gotBodies[1]["messages"].([]any)
	if !ok {
		t.Fatalf("second turn messages = %#v", gotBodies[1]["messages"])
	}
	var echoedThinking, echoedToolUse, returnedToolResult bool
	for _, rawMessage := range messages {
		message, _ := rawMessage.(map[string]any)
		blocks, _ := message["content"].([]any)
		for _, rawBlock := range blocks {
			block, _ := rawBlock.(map[string]any)
			switch block["type"] {
			case "thinking":
				echoedThinking = block["thinking"] == "consider the task" && block["signature"] == "sonnet55-thinking-signature"
			case "tool_use":
				echoedToolUse = block["id"] == "toolu_sonnet55" && block["name"] == "create_task"
			case "tool_result":
				returnedToolResult = block["tool_use_id"] == "toolu_sonnet55"
			}
		}
	}
	if !echoedThinking || !echoedToolUse || !returnedToolResult {
		t.Fatalf("second turn did not preserve thinking signature/tool history: thinking=%v tool_use=%v tool_result=%v messages=%#v", echoedThinking, echoedToolUse, returnedToolResult, messages)
	}
}

func TestCallChatStreamingUsesRuntimePolicyHistoryAndSystemContext(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, evt := range []string{
			`{"type":"message_start","message":{"id":"msg_chat","model":"claude-sonnet-5-5","usage":{"input_tokens":7}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"chat answer"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", evt)
		}
	}))
	defer server.Close()

	origHost := anthropicclient.AnthropicAPIHost
	anthropicclient.AnthropicAPIHost = server.URL
	defer func() { anthropicclient.AnthropicAPIHost = origHost }()

	ctx := llmcontracts.WithRuntimeTools(context.Background(), &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "list_tasks", Description: "List tasks", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	adapter := New(nil, nil, nil)
	result, err := adapter.Call(ctx, llmcontracts.AgentRequest{
		Operation:         llmcontracts.OperationStreaming,
		Message:           "What next?",
		ExecID:            "exec-chat",
		ChatHistory:       []models.Execution{{PromptSent: "Earlier question", Output: "Earlier answer", Status: models.ExecCompleted}},
		ChatSystemContext: "CHAT_SYSTEM_SENTINEL",
		Followup:          true,
		ChatMode:          models.ChatModeOrchestrate,
		Agent: models.LLMConfig{
			Name:            "Claude API",
			Provider:        models.ProviderAnthropic,
			Model:           "claude-sonnet-5-5",
			ReasoningEffort: "medium",
			AuthMethod:      models.AuthMethodAPIKey,
			APIKey:          "test-key",
		},
	}, "/repo/worktree", nil)
	if err != nil {
		t.Fatalf("Call chat: %v", err)
	}
	if !strings.Contains(result.Output, "chat answer") {
		t.Fatalf("output = %q", result.Output)
	}
	if result.Usage.InputTokens != 7 || result.Usage.OutputTokens != 4 {
		t.Fatalf("usage = %#v", result.Usage)
	}
	payload := fmt.Sprint(gotBody)
	for _, want := range []string{"What next?", "Earlier question", "Earlier answer", "CHAT_SYSTEM_SENTINEL", "list_tasks", llmprompt.AnthropicAgentSystemPrompt} {
		if !strings.Contains(payload, want) {
			t.Fatalf("request body missing %q: %#v", want, gotBody)
		}
	}
	if strings.Contains(payload, llmprompt.ChatActionToolModeInstructions) || strings.Contains(payload, "Available action tools") {
		t.Fatalf("task follow-up runtime tools should supplement coding tools: %#v", gotBody["system"])
	}
	if gotBody["model"] != "claude-sonnet-5-5" {
		t.Fatalf("chat model = %v, want claude-sonnet-5-5", gotBody["model"])
	}
	if outputConfig, ok := gotBody["output_config"].(map[string]any); !ok || outputConfig["effort"] != "medium" {
		t.Fatalf("chat output_config = %#v, want effort medium", gotBody["output_config"])
	}
	if thinking, ok := gotBody["thinking"].(map[string]any); !ok || thinking["type"] != "adaptive" {
		t.Fatalf("chat thinking = %#v, want adaptive", gotBody["thinking"])
	}
	for _, unsupported := range []string{"temperature", "top_p", "top_k", "tool_choice"} {
		if _, ok := gotBody[unsupported]; ok {
			t.Errorf("chat request unexpectedly included %q: %#v", unsupported, gotBody[unsupported])
		}
	}
}

func TestCompactionActivityInTaskAndChatResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"message_start","message":{"id":"msg_fixture","model":"claude-sonnet-4-6"}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"compaction"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"compaction_delta","content":"private summary"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Continued"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer server.Close()
	old := anthropicclient.AnthropicAPIHost
	anthropicclient.AnthropicAPIHost = server.URL
	defer func() { anthropicclient.AnthropicAPIHost = old }()
	for _, operation := range []llmcontracts.Operation{llmcontracts.OperationTask, llmcontracts.OperationStreaming} {
		t.Run(string(operation), func(t *testing.T) {
			adapter := New(nil, nil, nil)
			ctx := context.Background()
			result, err := adapter.Call(ctx, llmcontracts.AgentRequest{Ctx: ctx, Operation: operation, ExecID: "exec", Message: "continue", Agent: models.LLMConfig{Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey, Model: "claude-sonnet-4-6", APIKey: "fixture"}}, t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Output, "[Compaction started]") || !strings.Contains(result.Output, "[Compaction done | ") || !strings.Contains(result.Output, "Continued") || strings.Contains(result.Output, "private summary") {
				t.Fatalf("output=%q", result.Output)
			}
		})
	}
}

package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
)

func TestAppendToolModeSystemPromptCoversTaskFollowupsAndPreservesPlan(t *testing.T) {
	followup := appendToolModeSystemPrompt("base", nil, models.ChatModeOrchestrate)
	if !strings.Contains(followup, llmprompt.ChatActionUnavailableInstructions) {
		t.Fatalf("no-tool follow-up prompt missing capability limitation: %q", followup)
	}

	rt := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "create_task"}}}
	capable := appendToolModeSystemPrompt("base", rt, models.ChatModeOrchestrate)
	if !strings.Contains(capable, llmprompt.ChatActionToolModeInstructions) || !strings.Contains(capable, "Available action tools: create_task") {
		t.Fatalf("tool-capable follow-up prompt missing concrete runtime guidance: %q", capable)
	}

	plan := appendToolModeSystemPrompt("base", nil, models.ChatModePlan)
	if plan != "base" {
		t.Fatalf("Plan prompt received action-mode guidance: %q", plan)
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
			Model:           "claude-opus-5",
			ReasoningEffort: "low",
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
	if !strings.Contains(payload, llmprompt.ChatActionUnavailableInstructions) {
		t.Fatalf("zero-history follow-up missing capability limitation: %#v", gotBody)
	}
	if strings.Contains(payload, "TASK CREATION TOOL MODE") {
		t.Fatalf("zero-history follow-up received initial-task guidance: %#v", gotBody)
	}
	outputConfig, ok := gotBody["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != "low" {
		t.Fatalf("output_config = %#v, want effort low", gotBody["output_config"])
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
			Model:           "claude-opus-5",
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
	if system := fmt.Sprint(gotBody["system"]); !strings.Contains(system, "project rules") || !strings.Contains(system, "expert software engineer") {
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
	if strings.Contains(system, "expert software engineer") {
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

func TestCallStreamingPreservesListAlertsPresenceAcrossProviderBoundary(t *testing.T) {
	var turn int
	var handlerInputs []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn++
		body, _ := io.ReadAll(r.Body)
		if turn == 1 {
			for _, want := range []string{`"processing_state"`, `"default":"all"`} {
				if !strings.Contains(string(body), want) {
					t.Fatalf("provider request schema missing %s: %s", want, body)
				}
			}
			if strings.Contains(string(body), "x-openvibely-omit-value") {
				t.Fatalf("provider request leaked internal schema metadata: %s", body)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_%d\",\"model\":\"claude-opus-5\",\"usage\":{\"input_tokens\":1}}}\n\n", turn)
		if turn <= 3 {
			input := `{"decision_state":"approved","implementation_task_linked":"unlinked","limit":50,"offset":0}`
			if turn == 2 {
				input = `{"project_id":"","decision_state":"approved","processing_state":"all","type":"","source":"","read":"all","implementation_task_linked":"unlinked","limit":50,"offset":0}`
			}
			if turn == 3 {
				input = `{"decision_state":"approved","processing_state":"not_applicable","read":"unread","implementation_task_linked":"linked"}`
			}
			fmt.Fprintf(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_%d\",\"name\":\"list_alerts\",\"input\":%s}}\n\n", turn, input)
			_, _ = w.Write([]byte("data: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n"))
			return
		}
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"done\"}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	origHost := anthropicclient.AnthropicAPIHost
	anthropicclient.AnthropicAPIHost = server.URL
	defer func() { anthropicclient.AnthropicAPIHost = origHost }()
	definition := chatcontrol.Get("list_alerts")
	runtime := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: definition.Name, Description: definition.Description, Parameters: definition.Parameters, Access: llmcontracts.RuntimeToolAccessRead}},
		Executor: func(_ context.Context, _ string, input json.RawMessage) (string, bool, bool, error) {
			var decoded map[string]any
			if err := chatcontrol.DecodeRuntimeToolInput(input, &decoded); err != nil {
				return "", true, true, err
			}
			handlerInputs = append(handlerInputs, decoded)
			return `{}`, true, false, nil
		},
		SkipDefaultTools: true,
	}
	ctx := llmcontracts.WithRuntimeTools(context.Background(), runtime)
	adapter := New(nil, nil, nil)
	_, _, _, err := adapter.callStreaming(ctx, "scan", nil, models.LLMConfig{Name: "Claude", Provider: models.ProviderAnthropic, Model: "claude-opus-5", AuthMethod: models.AuthMethodAPIKey, APIKey: "test"}, "exec", ".", "", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("callStreaming: %v", err)
	}
	if len(handlerInputs) != 3 {
		t.Fatalf("handler inputs = %d, want 3", len(handlerInputs))
	}
	for _, index := range []int{0, 1} {
		for _, omitted := range []string{"project_id", "processing_state", "type", "source", "read"} {
			if _, ok := handlerInputs[index][omitted]; ok {
				t.Fatalf("omitted field %q reached handler in case %d: %#v", omitted, index, handlerInputs[index])
			}
		}
	}
	if handlerInputs[2]["processing_state"] != "not_applicable" || handlerInputs[2]["read"] != "unread" || handlerInputs[2]["implementation_task_linked"] != "linked" {
		t.Fatalf("explicit filters changed: %#v", handlerInputs[2])
	}
}

func TestCallStreamingUsesAgenticStreamCallbacksAndRuntimeTools(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Fatalf("x-api-key = %q, want test-key", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, evt := range []string{
			`{"type":"message_start","message":{"id":"msg_stream","model":"claude-opus-5","usage":{"input_tokens":11,"cache_creation_input_tokens":2,"cache_read_input_tokens":3}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"consider options"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"create_task","input":{"title":"ship it"}}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"streamed answer"}}`,
			`{"type":"content_block_stop","index":2}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
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
		Executor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			if name != "create_task" || !strings.Contains(string(input), "ship it") {
				t.Fatalf("runtime tool call = %s %s", name, string(input))
			}
			return `{"created":true}`, true, false, nil
		},
		SkipDefaultTools: true,
	})

	adapter := New(nil, nil, nil)
	output, textOnly, usage, err := adapter.callStreaming(ctx, "Finish task", nil, models.LLMConfig{
		Name:            "Claude API",
		Provider:        models.ProviderAnthropic,
		Model:           "claude-opus-5",
		ReasoningEffort: "low",
		AuthMethod:      models.AuthMethodAPIKey,
		APIKey:          "test-key",
	}, "exec-stream", "/repo/worktree", "project rules", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("callStreaming: %v", err)
	}
	if !strings.Contains(output, "streamed answer") || !strings.Contains(output, "consider options") {
		t.Fatalf("stream output missing thinking/text events: %q", output)
	}
	if textOnly != "streamed answer" {
		t.Fatalf("textOnly = %q, want streamed answer", textOnly)
	}
	if usage.InputTokens != 11 || usage.OutputTokens != 5 || usage.TotalTokens != 16 {
		t.Fatalf("usage = %#v", usage)
	}
	payload := fmt.Sprint(gotBody)
	if !strings.Contains(payload, "Finish task") || !strings.Contains(payload, "project rules") || !strings.Contains(payload, "create_task") {
		t.Fatalf("request body missing prompt/system/runtime tools: %#v", gotBody)
	}
	if strings.Contains(payload, "Bash") {
		t.Fatalf("SkipDefaultTools should omit default tools, got %#v", gotBody["tools"])
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
			`{"type":"message_start","message":{"id":"msg_chat","model":"claude-opus-5","usage":{"input_tokens":7}}}`,
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
	output, usage, err := adapter.callChatStreaming(ctx, "What next?", nil, models.LLMConfig{
		Name:            "Claude API",
		Provider:        models.ProviderAnthropic,
		Model:           "claude-opus-5",
		ReasoningEffort: "low",
		AuthMethod:      models.AuthMethodAPIKey,
		APIKey:          "test-key",
	}, "exec-chat", []models.Execution{{PromptSent: "Earlier question", Output: "Earlier answer", Status: models.ExecCompleted}}, "CHAT_SYSTEM_SENTINEL", true, models.ChatModeOrchestrate, "/repo/worktree", nil, nil, nil, false)
	if err != nil {
		t.Fatalf("callChatStreaming: %v", err)
	}
	if !strings.Contains(output, "chat answer") {
		t.Fatalf("output = %q", output)
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 4 {
		t.Fatalf("usage = %#v", usage)
	}
	payload := fmt.Sprint(gotBody)
	for _, want := range []string{"What next?", "Earlier question", "Earlier answer", "CHAT_SYSTEM_SENTINEL", "list_tasks"} {
		if !strings.Contains(payload, want) {
			t.Fatalf("request body missing %q: %#v", want, gotBody)
		}
	}
	if !strings.Contains(payload, llmprompt.ChatActionToolModeInstructions) {
		t.Fatalf("chat runtime tools should enable action guidance: %#v", gotBody["system"])
	}
}

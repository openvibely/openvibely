package service

import (
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/agentplugins"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestProviderAdapter_TestProvider_UsesCanonicalRequest(t *testing.T) {
	svc := &LLMService{}
	mock := testutil.NewMockLLMCaller()
	mock.Response = "adapter-output"
	mock.TextOnly = "adapter-text"
	mock.Tokens = 33
	svc.SetLLMCaller(mock)
	svc.initProviderAdapters()

	adapter, ok := svc.adapterFor(models.ProviderTest)
	if !ok {
		t.Fatal("expected test provider adapter")
	}

	res, err := adapter.Call(llmcontracts.AgentRequest{
		Ctx:       context.Background(),
		Operation: llmcontracts.OperationTask,
		Message:   " hello from adapter ",
		Agent:     models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"},
		ExecID:    "exec-1",
		WorkDir:   "/tmp/work",
	})
	if err != nil {
		t.Fatalf("adapter.Call error: %v", err)
	}
	if res.Output != "adapter-output" {
		t.Fatalf("expected output adapter-output, got %q", res.Output)
	}
	if res.TextOnlyOutput != "adapter-text" {
		t.Fatalf("expected textOnly adapter-text, got %q", res.TextOnlyOutput)
	}
	if res.Usage.TotalTokens != 33 {
		t.Fatalf("expected usage total 33, got %d", res.Usage.TotalTokens)
	}
	if mock.CallCount() != 1 {
		t.Fatalf("expected one mock call, got %d", mock.CallCount())
	}
	if mock.LastCall().ExecID != "exec-1" {
		t.Fatalf("expected exec id propagated, got %q", mock.LastCall().ExecID)
	}
}

func TestLLMService_CallAgentDirect_NormalizesMessageWhitespace(t *testing.T) {
	svc := &LLMService{}
	mock := testutil.NewMockLLMCaller()
	mock.Response = "ok"
	mock.TextOnly = "ok"
	mock.Tokens = 1
	svc.SetLLMCaller(mock)
	svc.initProviderAdapters()
	svc.routing = newAgentRoutingStrategy(svc)

	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}
	_, _, err := svc.CallAgentDirect(context.Background(), "  hello world  ", nil, agent, "")
	if err != nil {
		t.Fatalf("CallAgentDirect error: %v", err)
	}

	if got := mock.LastCall().Prompt; got != "hello world" {
		t.Fatalf("expected normalized prompt 'hello world', got %q", got)
	}
}

func TestLLMService_CallAgentDirectNoTools_PropagatesDisableToolsFlag(t *testing.T) {
	svc := &LLMService{}
	capture := &captureProviderAdapter{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		models.ProviderTest: capture,
	}
	svc.routing = newAgentRoutingStrategy(svc)

	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}
	if _, _, err := svc.CallAgentDirectNoTools(context.Background(), "  json only  ", nil, agent, ""); err != nil {
		t.Fatalf("CallAgentDirectNoTools error: %v", err)
	}

	if !capture.lastReq.DisableTools {
		t.Fatal("expected DisableTools=true in provider request")
	}
	if capture.lastReq.Message != "json only" {
		t.Fatalf("expected normalized message to propagate, got %q", capture.lastReq.Message)
	}
}

func TestLLMService_CallAgentRawDirectNoToolsIsolatesUtilityRequest(t *testing.T) {
	svc := &LLMService{}
	capture := &captureProviderAdapter{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		models.ProviderTest: capture,
	}
	svc.routing = newAgentRoutingStrategy(svc)

	runtimeTools := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "write_file"}},
	}
	ctx := llmcontracts.WithRuntimeTools(context.Background(), runtimeTools)
	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}
	if _, _, err := svc.CallAgentRawDirectNoTools(ctx, "commit summary", nil, agent, ""); err != nil {
		t.Fatalf("CallAgentRawDirectNoTools error: %v", err)
	}

	if !capture.lastReq.DisableTools || !capture.lastReq.RawDirectPrompt {
		t.Fatalf("expected raw no-tools request, got disable=%v raw=%v", capture.lastReq.DisableTools, capture.lastReq.RawDirectPrompt)
	}
	if tools := llmcontracts.RuntimeToolsFromContext(capture.lastReq.Ctx); tools != nil {
		t.Fatalf("expected inherited runtime tools to be removed, got %#v", tools)
	}
	if capture.lastReq.AgentDefinition != nil || capture.lastReq.ProjectInstructions != "" || capture.lastReq.ChatSystemContext != "" {
		t.Fatalf("expected no interactive agent framing, got %#v", capture.lastReq)
	}
}

func TestAnthropicProviderAdapter_RejectsRetiredCLITransport(t *testing.T) {
	adapter := &anthropicProviderAdapter{svc: &LLMService{}}
	_, err := adapter.Call(llmcontracts.AgentRequest{
		Ctx:          context.Background(),
		Operation:    llmcontracts.OperationDirect,
		Message:      "generate JSON",
		DisableTools: true,
		Agent: models.LLMConfig{
			Provider:   models.ProviderAnthropic,
			AuthMethod: models.AuthMethodCLI,
			Model:      "claude-sonnet-4",
		},
	})
	if err == nil {
		t.Fatal("expected error for retired Anthropic CLI transport")
	}
	if !strings.Contains(err.Error(), "no longer supported") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpenAIProviderAdapter_RejectsRetiredCLITransport(t *testing.T) {
	adapter := &openAIProviderAdapter{svc: &LLMService{}}
	_, err := adapter.Call(llmcontracts.AgentRequest{
		Ctx:          context.Background(),
		Operation:    llmcontracts.OperationDirect,
		Message:      "generate JSON",
		DisableTools: true,
		Agent: models.LLMConfig{
			Provider:   models.ProviderOpenAI,
			AuthMethod: models.AuthMethodCLI,
			Model:      "gpt-5.3-codex",
		},
	})
	if err == nil {
		t.Fatal("expected error for retired OpenAI CLI transport")
	}
	if !strings.Contains(err.Error(), "no longer supported") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRequestUsesChatStreamingTreatsFirstTurnChatAsChat(t *testing.T) {
	if !requestUsesChatStreaming(llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, ChatMode: models.ChatModeOrchestrate}) {
		t.Fatal("expected first-turn orchestrate chat with nil history to use chat streaming")
	}
	if !requestUsesChatStreaming(llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, ChatMode: models.ChatModePlan}) {
		t.Fatal("expected first-turn plan chat with nil history to use chat streaming")
	}
	if !requestUsesChatStreaming(llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, Followup: true}) {
		t.Fatal("expected task followup without prior history or selected context to use chat streaming")
	}
	if !requestUsesChatStreaming(llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, ChatMode: models.ChatModeOrchestrate, Followup: true, ChatHistory: []models.Execution{{PromptSent: "previous", Output: "done"}}}) {
		t.Fatal("expected task followup with chat history to use chat streaming")
	}
	if !requestUsesChatStreaming(llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, Followup: true, ChatSystemContext: "## Selected Memories For This Task"}) {
		t.Fatal("expected task followup with selected-memory system context to use chat streaming")
	}
	if requestUsesChatStreaming(llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming}) {
		t.Fatal("streaming task without explicit chat mode/history/context must not use chat streaming")
	}
}

func TestResolveAgentRuntime_PerAgentPluginIsolation(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		if len(pluginIDs) == 1 && pluginIDs[0] == "plugin-a@alpha-market" {
			return &agentplugins.RuntimeBundle{
				PluginIDs: []string{"plugin-a@alpha-market"},
				Skills:    []models.SkillConfig{{Name: "skill-a", Content: "a"}},
			}, nil
		}
		if len(pluginIDs) == 1 && pluginIDs[0] == "plugin-b@beta-market" {
			return &agentplugins.RuntimeBundle{
				PluginIDs: []string{"plugin-b@beta-market"},
				Skills:    []models.SkillConfig{{Name: "skill-b", Content: "b"}},
			}, nil
		}
		return &agentplugins.RuntimeBundle{}, nil
	}

	agentA := &models.Agent{Name: "agent-a", Plugins: []string{"plugin-a@alpha-market"}}
	agentB := &models.Agent{Name: "agent-b", Plugins: []string{"plugin-b@beta-market"}}

	_, mergedA := resolveAgentRuntime(context.Background(), agentA)
	_, mergedB := resolveAgentRuntime(context.Background(), agentB)

	if len(mergedA.Skills) == 0 || !strings.EqualFold(strings.TrimSpace(mergedA.Skills[0].Name), "skill-a") {
		t.Fatalf("expected agent A to include only plugin A skill, got %+v", mergedA.Skills)
	}
	if len(mergedB.Skills) == 0 || !strings.EqualFold(strings.TrimSpace(mergedB.Skills[0].Name), "skill-b") {
		t.Fatalf("expected agent B to include only plugin B skill, got %+v", mergedB.Skills)
	}
}

// TestResolveAgentRuntime_NilAgentDefinition verifies that a nil agent
// definition produces zero plugin context (no skills, no dirs, no MCP).
func TestResolveAgentRuntime_NilAgentDefinition(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	called := false
	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		called = true
		return &agentplugins.RuntimeBundle{
			Skills: []models.SkillConfig{{Name: "leaked-skill", Content: "leaked"}},
		}, nil
	}

	raw, merged := resolveAgentRuntime(context.Background(), nil)
	if raw != nil {
		t.Fatalf("expected nil raw agent, got %+v", raw)
	}
	if merged != nil {
		t.Fatalf("expected nil merged agent, got %+v", merged)
	}
	if called {
		t.Fatal("plugin resolver should not be called for nil agent definition")
	}
}

// TestResolveAgentRuntime_AgentWithNoPlugins verifies that an agent with an
// empty Plugins list does not trigger plugin resolution.
func TestResolveAgentRuntime_AgentWithNoPlugins(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	called := false
	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		called = true
		return &agentplugins.RuntimeBundle{
			Skills: []models.SkillConfig{{Name: "leaked-skill", Content: "leaked"}},
		}, nil
	}

	agentDef := &models.Agent{
		Name:         "no-plugins-agent",
		SystemPrompt: "I have no plugins",
		Plugins:      nil,
	}

	raw, merged := resolveAgentRuntime(context.Background(), agentDef)
	if raw == nil || merged == nil {
		t.Fatal("expected non-nil raw/merged agent")
	}
	if len(merged.Skills) != 0 {
		t.Fatalf("expected zero skills, got %+v", merged.Skills)
	}
	if len(merged.MCPServers) != 0 {
		t.Fatalf("expected zero MCP servers, got %+v", merged.MCPServers)
	}
	if called {
		t.Fatal("plugin resolver should not be called when agent has no plugins")
	}

	// Also test with explicit empty slice (not nil)
	agentDef.Plugins = []string{}
	called = false
	_, merged2 := resolveAgentRuntime(context.Background(), agentDef)
	if len(merged2.Skills) != 0 {
		t.Fatalf("expected zero skills for empty slice, got %+v", merged2.Skills)
	}
	if called {
		t.Fatal("plugin resolver should not be called for empty plugin list")
	}
}

// TestResolveAgentRuntime_NoCrossAgentLeakage verifies that resolving plugins
// for one agent does not leak skills, MCP servers, or dirs into another agent.
func TestResolveAgentRuntime_NoCrossAgentLeakage(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		// Return unique resources per plugin set to verify isolation
		if len(pluginIDs) == 1 && pluginIDs[0] == "plugin-x@market" {
			return &agentplugins.RuntimeBundle{
				PluginIDs:  []string{"plugin-x@market"},
				Skills:     []models.SkillConfig{{Name: "skill-x", Content: "x-content"}},
				MCPServers: []models.MCPServerConfig{{Name: "mcp-x", Type: "stdio", Command: []string{"echo"}}},
			}, nil
		}
		return &agentplugins.RuntimeBundle{}, nil
	}

	agentWithPlugins := &models.Agent{Name: "agent-with-plugins", Plugins: []string{"plugin-x@market"}}
	agentNoPlugins := &models.Agent{Name: "agent-no-plugins", Plugins: nil}

	// Resolve agent with plugins first
	_, mergedWith := resolveAgentRuntime(context.Background(), agentWithPlugins)
	// Then resolve agent without plugins
	_, mergedWithout := resolveAgentRuntime(context.Background(), agentNoPlugins)

	// Verify agent WITH plugins has them
	if len(mergedWith.Skills) != 1 || mergedWith.Skills[0].Name != "skill-x" {
		t.Fatalf("agent with plugins should have skill-x, got %+v", mergedWith.Skills)
	}
	if len(mergedWith.MCPServers) != 1 || mergedWith.MCPServers[0].Name != "mcp-x" {
		t.Fatalf("agent with plugins should have mcp-x, got %+v", mergedWith.MCPServers)
	}
	// Verify agent WITHOUT plugins has none
	if len(mergedWithout.Skills) != 0 {
		t.Fatalf("agent without plugins should have zero skills, got %+v", mergedWithout.Skills)
	}
	if len(mergedWithout.MCPServers) != 0 {
		t.Fatalf("agent without plugins should have zero MCP servers, got %+v", mergedWithout.MCPServers)
	}
}

// TestAdapterCall_NoAgentDefinition_NoPluginContext verifies that adapter calls
// without an agent definition produce zero plugin context in the request.
func TestAdapterCall_NoAgentDefinition_NoPluginContext(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	called := false
	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		called = true
		return &agentplugins.RuntimeBundle{
			Skills: []models.SkillConfig{{Name: "leaked", Content: "should not appear"}},
		}, nil
	}

	svc := &LLMService{}
	mock := testutil.NewMockLLMCaller()
	mock.Response = "ok"
	mock.TextOnly = "ok"
	mock.Tokens = 1
	svc.SetLLMCaller(mock)
	svc.initProviderAdapters()

	// Call with nil AgentDefinition (task has no assigned agent definition)
	_, err := svc.providerAdapters[models.ProviderTest].Call(llmcontracts.AgentRequest{
		Ctx:             context.Background(),
		Operation:       llmcontracts.OperationTask,
		Message:         "do something",
		Agent:           models.LLMConfig{Provider: models.ProviderTest, Model: "test"},
		AgentDefinition: nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("plugin resolver should not be called when no agent definition is set")
	}
}

// pluginResolvingAdapter wraps a captureProviderAdapter and calls
// resolveAgentRuntime before delegating, mimicking what the real provider
// adapters (openAI/anthropic) do.
type pluginResolvingAdapter struct {
	inner *captureProviderAdapter
}

func (a *pluginResolvingAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	_, runtimeAgentDef := resolveAgentRuntime(req.Ctx, req.AgentDefinition)
	if runtimeAgentDef != nil {
		req.AgentDefinition = runtimeAgentDef
	}
	return a.inner.Call(req)
}

// TestAdapterCall_AgentWithPlugins_OnlyThosePlugins verifies that adapter calls
// with an agent definition containing plugins receive exactly those plugins'
// skills and MCP servers, and no others.
func TestAdapterCall_AgentWithPlugins_OnlyThosePlugins(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	var resolvedIDs []string
	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		resolvedIDs = pluginIDs
		return &agentplugins.RuntimeBundle{
			PluginIDs:  pluginIDs,
			Skills:     []models.SkillConfig{{Name: "target-skill", Content: "target content"}},
			MCPServers: []models.MCPServerConfig{{Name: "target-mcp", Type: "stdio", Command: []string{"echo"}}},
		}, nil
	}

	capture := &captureProviderAdapter{}
	adapter := &pluginResolvingAdapter{inner: capture}

	agentDef := &models.Agent{
		ID:           "agent-def-target",
		Name:         "target-agent",
		SystemPrompt: "Use target tools",
		Plugins:      []string{"target-plugin@market"},
	}

	_, err := adapter.Call(llmcontracts.AgentRequest{
		Ctx:             context.Background(),
		Operation:       llmcontracts.OperationTask,
		Message:         "run task",
		Agent:           models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-test"},
		AgentDefinition: agentDef,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the plugin resolver was called with exactly the agent's plugins
	if len(resolvedIDs) != 1 || resolvedIDs[0] != "target-plugin@market" {
		t.Fatalf("expected resolver to be called with [target-plugin@market], got %v", resolvedIDs)
	}

	// Verify the agent definition passed through has the merged plugins
	if capture.lastReq.AgentDefinition == nil {
		t.Fatal("expected agent definition to be propagated")
	}
	if len(capture.lastReq.AgentDefinition.Skills) != 1 || capture.lastReq.AgentDefinition.Skills[0].Name != "target-skill" {
		t.Fatalf("expected merged skills to contain target-skill, got %+v", capture.lastReq.AgentDefinition.Skills)
	}
	if len(capture.lastReq.AgentDefinition.MCPServers) != 1 || capture.lastReq.AgentDefinition.MCPServers[0].Name != "target-mcp" {
		t.Fatalf("expected merged MCP servers to contain target-mcp, got %+v", capture.lastReq.AgentDefinition.MCPServers)
	}
}

// TestAdapterCall_MultipleAgents_NoPluginCrossLeak exercises the full adapter
// call path with two different agents sequentially, verifying that plugin
// context from one agent does not leak into calls for the other.
func TestAdapterCall_MultipleAgents_NoPluginCrossLeak(t *testing.T) {
	origResolve := resolvePluginRuntimeBundleFn
	defer func() { resolvePluginRuntimeBundleFn = origResolve }()

	resolvePluginRuntimeBundleFn = func(ctx context.Context, pluginIDs []string) (*agentplugins.RuntimeBundle, error) {
		if len(pluginIDs) == 1 && pluginIDs[0] == "alpha-plugin@market-a" {
			return &agentplugins.RuntimeBundle{
				PluginIDs:  []string{"alpha-plugin@market-a"},
				Skills:     []models.SkillConfig{{Name: "alpha-skill", Content: "alpha"}},
				MCPServers: []models.MCPServerConfig{{Name: "alpha-mcp", Type: "stdio", Command: []string{"alpha-cmd"}}},
			}, nil
		}
		if len(pluginIDs) == 1 && pluginIDs[0] == "beta-plugin@market-b" {
			return &agentplugins.RuntimeBundle{
				PluginIDs:  []string{"beta-plugin@market-b"},
				Skills:     []models.SkillConfig{{Name: "beta-skill", Content: "beta"}},
				MCPServers: []models.MCPServerConfig{{Name: "beta-mcp", Type: "stdio", Command: []string{"beta-cmd"}}},
			}, nil
		}
		return &agentplugins.RuntimeBundle{}, nil
	}

	capture := &captureProviderAdapter{}
	adapter := &pluginResolvingAdapter{inner: capture}

	agentA := &models.Agent{
		ID:      "agent-alpha",
		Name:    "alpha-agent",
		Plugins: []string{"alpha-plugin@market-a"},
	}
	agentB := &models.Agent{
		ID:      "agent-beta",
		Name:    "beta-agent",
		Plugins: []string{"beta-plugin@market-b"},
	}

	// Call for agent A
	adapter.Call(llmcontracts.AgentRequest{
		Ctx:             context.Background(),
		Operation:       llmcontracts.OperationTask,
		Message:         "task for alpha",
		Agent:           models.LLMConfig{Provider: models.ProviderOpenAI, Model: "test"},
		AgentDefinition: agentA,
	})
	reqA := capture.lastReq
	if reqA.AgentDefinition == nil {
		t.Fatal("expected agent definition for agent A")
	}
	if len(reqA.AgentDefinition.Skills) != 1 || reqA.AgentDefinition.Skills[0].Name != "alpha-skill" {
		t.Fatalf("agent A should have alpha-skill only, got %+v", reqA.AgentDefinition.Skills)
	}
	if len(reqA.AgentDefinition.MCPServers) != 1 || reqA.AgentDefinition.MCPServers[0].Name != "alpha-mcp" {
		t.Fatalf("agent A should have alpha-mcp only, got %+v", reqA.AgentDefinition.MCPServers)
	}
	// Call for agent B
	adapter.Call(llmcontracts.AgentRequest{
		Ctx:             context.Background(),
		Operation:       llmcontracts.OperationTask,
		Message:         "task for beta",
		Agent:           models.LLMConfig{Provider: models.ProviderOpenAI, Model: "test"},
		AgentDefinition: agentB,
	})
	reqB := capture.lastReq
	if reqB.AgentDefinition == nil {
		t.Fatal("expected agent definition for agent B")
	}
	if len(reqB.AgentDefinition.Skills) != 1 || reqB.AgentDefinition.Skills[0].Name != "beta-skill" {
		t.Fatalf("agent B should have beta-skill only, got %+v", reqB.AgentDefinition.Skills)
	}
	if len(reqB.AgentDefinition.MCPServers) != 1 || reqB.AgentDefinition.MCPServers[0].Name != "beta-mcp" {
		t.Fatalf("agent B should have beta-mcp only, got %+v", reqB.AgentDefinition.MCPServers)
	}
	// Verify no cross-contamination: A's resources don't appear in B
	for _, s := range reqB.AgentDefinition.Skills {
		if s.Name == "alpha-skill" {
			t.Fatal("agent B should NOT have alpha-skill (cross-agent leak)")
		}
	}
	for _, s := range reqB.AgentDefinition.MCPServers {
		if s.Name == "alpha-mcp" {
			t.Fatal("agent B should NOT have alpha-mcp (cross-agent leak)")
		}
	}

	// Call with nil agent definition (task with no agent) — verify zero plugin context
	adapter.Call(llmcontracts.AgentRequest{
		Ctx:             context.Background(),
		Operation:       llmcontracts.OperationTask,
		Message:         "task without agent",
		Agent:           models.LLMConfig{Provider: models.ProviderOpenAI, Model: "test"},
		AgentDefinition: nil,
	})
	reqNone := capture.lastReq
	if reqNone.AgentDefinition != nil {
		t.Fatalf("task with no agent definition should have nil AgentDefinition, got %+v", reqNone.AgentDefinition)
	}
}

// TestApplyAgentToSystemPrompt_NilAgent verifies that a nil agent produces
// no plugin content in the system prompt.
func TestApplyAgentToSystemPrompt_NilAgent(t *testing.T) {
	result := ApplyAgentToSystemPrompt("base prompt", nil)
	if result != "base prompt" {
		t.Fatalf("expected unmodified base prompt, got %q", result)
	}
}

// TestApplyAgentToSystemPrompt_AgentNoSkills verifies that an agent with a
// system prompt but no skills only adds the system prompt.
func TestApplyAgentToSystemPrompt_AgentNoSkills(t *testing.T) {
	agent := &models.Agent{
		SystemPrompt: "I am a test agent",
		Skills:       nil,
	}
	result := ApplyAgentToSystemPrompt("base", agent)
	if !strings.Contains(result, "I am a test agent") {
		t.Fatalf("expected system prompt in result, got %q", result)
	}
	if !strings.Contains(result, "base") {
		t.Fatalf("expected base prompt preserved, got %q", result)
	}
	if strings.Contains(result, "Skill:") {
		t.Fatalf("should not contain skill sections, got %q", result)
	}
}

// TestApplyAgentToSystemPrompt_AgentWithSkills verifies that plugin-resolved
// skills are included in the system prompt.
func TestApplyAgentToSystemPrompt_AgentWithSkills(t *testing.T) {
	agent := &models.Agent{
		SystemPrompt: "I use plugins",
		Skills: []models.SkillConfig{
			{Name: "test-skill", Content: "Do the test thing"},
		},
	}
	result := ApplyAgentToSystemPrompt("base", agent)
	if !strings.Contains(result, "I use plugins") {
		t.Fatalf("expected system prompt, got %q", result)
	}
	if !strings.Contains(result, "test-skill") {
		t.Fatalf("expected skill name in prompt, got %q", result)
	}
	if !strings.Contains(result, "Do the test thing") {
		t.Fatalf("expected skill content in prompt, got %q", result)
	}
}

package lifecycle

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

type fakeCaller struct {
	reply                string
	lastContext          context.Context
	lastPrompt           string
	lastConfig           models.LLMConfig
	lastWorkDir          string
	lastAgentDef         *models.Agent
	lastRuntime          *llmcontracts.RuntimeTools
	calledDirect         bool
	calledWithDef        bool
	calledWithDefNoTools bool
	returnErr            error
}

func (f *fakeCaller) CallAgentDirect(ctx context.Context, message string, _ []models.Attachment, agent models.LLMConfig, workDir string) (string, int, error) {
	f.calledDirect = true
	f.lastContext = ctx
	f.lastPrompt = message
	f.lastConfig = agent
	f.lastWorkDir = workDir
	f.lastRuntime = llmcontracts.RuntimeToolsFromContext(ctx)
	return f.reply, 0, f.returnErr
}

func (f *fakeCaller) CallAgentDirectWithDefinition(ctx context.Context, message string, _ []models.Attachment, agent models.LLMConfig, workDir string, agentDef *models.Agent) (string, int, error) {
	f.calledWithDef = true
	f.lastContext = ctx
	f.lastPrompt = message
	f.lastConfig = agent
	f.lastWorkDir = workDir
	f.lastAgentDef = agentDef
	f.lastRuntime = llmcontracts.RuntimeToolsFromContext(ctx)
	return f.reply, 0, f.returnErr
}

func (f *fakeCaller) CallAgentDirectWithDefinitionNoTools(ctx context.Context, message string, _ []models.Attachment, agent models.LLMConfig, workDir string, agentDef *models.Agent) (string, int, error) {
	f.calledWithDefNoTools = true
	f.lastContext = ctx
	f.lastPrompt = message
	f.lastConfig = agent
	f.lastWorkDir = workDir
	f.lastAgentDef = agentDef
	f.lastRuntime = llmcontracts.RuntimeToolsFromContext(ctx)
	return f.reply, 0, f.returnErr
}

type fakeAgentLookup struct {
	byID map[string]*models.Agent
}

func (f *fakeAgentLookup) GetByID(_ context.Context, id string) (*models.Agent, error) {
	return f.byID[id], nil
}

type fakeLLMConfig struct{ def *models.LLMConfig }

func (f *fakeLLMConfig) GetDefault(_ context.Context) (*models.LLMConfig, error) { return f.def, nil }

func TestLLMHookInvoker_RenderAndCall(t *testing.T) {
	caller := &fakeCaller{reply: `{"content":"hello","sources":["a"]}`}
	agentDef := &models.Agent{Name: "X", Model: "sonnet"}
	agents := &fakeAgentLookup{byID: map[string]*models.Agent{
		"agent-1": agentDef,
	}}
	inv := NewLLMHookInvoker(caller, agents, nil)
	hook := models.AgentLifecycleHook{
		ID:             "h1",
		AgentID:        "agent-1",
		OutputContract: models.OutputContractContextBlock,
		PromptOverride: "Use compact phrasing.",
	}
	in := HookInput{
		TaskID:    "t1",
		TaskRunID: "r1",
		WorkDir:   "/repo",
		SkillBody: "Recall memory then return context_block.",
	}
	raw, err := inv.Invoke(context.Background(), hook, in)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if string(raw) == "" {
		t.Fatal("expected non-empty raw payload")
	}
	if !strings.Contains(caller.lastPrompt, "Recall memory then return context_block.") {
		t.Fatalf("expected skill body in prompt, got %q", caller.lastPrompt)
	}
	if !strings.Contains(caller.lastPrompt, "Use compact phrasing.") {
		t.Fatalf("expected prompt override in prompt, got %q", caller.lastPrompt)
	}
	if !strings.Contains(caller.lastPrompt, "context_block") {
		t.Fatalf("expected output_contract mention in prompt")
	}
	if caller.lastConfig.Model != "sonnet" {
		t.Fatalf("expected agent model override, got %q", caller.lastConfig.Model)
	}
	if caller.lastAgentDef != agentDef {
		t.Fatalf("expected context_block hook to receive owning agent definition")
	}
	if !caller.calledWithDef || caller.calledDirect {
		t.Fatalf("expected hook to use agent definition caller, calledWithDef=%v calledDirect=%v", caller.calledWithDef, caller.calledDirect)
	}
	if caller.lastWorkDir != "/repo" {
		t.Fatalf("expected hook workDir to be passed to caller, got %q", caller.lastWorkDir)
	}
	// Validate the raw payload against the contract for sanity.
	if err := ValidateOutput(hook.OutputContract, raw); err != nil {
		t.Fatalf("raw payload should pass contract validation: %v", err)
	}
}

func TestLLMHookInvoker_HookUsesAgentDefinitionCaller(t *testing.T) {
	caller := &fakeCaller{reply: `{"summary":"reviewed","nothing_to_save":true}`}
	agentDef := &models.Agent{Name: "Skill Curator", Model: "sonnet"}
	inv := NewLLMHookInvoker(caller, &fakeAgentLookup{byID: map[string]*models.Agent{"skill-curator": agentDef}}, nil)
	_, err := inv.Invoke(context.Background(), models.AgentLifecycleHook{AgentID: "skill-curator", OutputContract: models.OutputContractLearningSummary}, HookInput{WorkDir: "/repo"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if caller.lastAgentDef != agentDef {
		t.Fatalf("expected hook to receive agent definition")
	}
	if !caller.calledWithDef || caller.calledDirect {
		t.Fatalf("expected hook to use agent definition caller, calledWithDef=%v calledDirect=%v", caller.calledWithDef, caller.calledDirect)
	}
	if caller.lastWorkDir != "/repo" {
		t.Fatalf("expected hook workdir, got %q", caller.lastWorkDir)
	}
}

func TestLLMHookInvoker_FiltersRuntimeToolsByAgentDefinition(t *testing.T) {
	caller := &fakeCaller{reply: `{"summary":"reviewed","nothing_to_save":true}`}
	agentDef := &models.Agent{Name: "Skill Curator", Model: "sonnet", Tools: []string{"skill_view", "skill_manage"}}
	inv := NewLLMHookInvoker(caller, &fakeAgentLookup{byID: map[string]*models.Agent{"skill-curator": agentDef}}, nil)
	baseTools := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "skill_view"}, {Name: "skills_list"}, {Name: "skill_manage"}},
	}
	ctx := llmcontracts.WithRuntimeTools(context.Background(), baseTools)
	_, err := inv.Invoke(ctx, models.AgentLifecycleHook{AgentID: "skill-curator", OutputContract: models.OutputContractLearningSummary}, HookInput{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if caller.lastRuntime == nil {
		t.Fatal("expected runtime tools")
	}
	for _, allowed := range []string{"skill_view", "skill_manage"} {
		if !caller.lastRuntime.HasDefinition(allowed) {
			t.Fatalf("expected %s to be available", allowed)
		}
	}
	for _, denied := range []string{"skills_list"} {
		if caller.lastRuntime.HasDefinition(denied) {
			t.Fatalf("did not expect %s to be available", denied)
		}
	}
}

func TestLLMHookInvoker_ScopedFilesGrantAllowsConcreteFileRuntimeTools(t *testing.T) {
	caller := &fakeCaller{reply: `{"summary":"updated scoped files","changed_paths":["state.md"]}`}
	agentDef := &models.Agent{Name: "Custom After-Complete Agent", Model: "sonnet", Tools: []string{models.AgentToolScopedFiles}}
	inv := NewLLMHookInvoker(caller, &fakeAgentLookup{byID: map[string]*models.Agent{"custom-hook-agent": agentDef}}, nil)
	baseTools := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "read_file"}, {Name: "write_file"}, {Name: "delete_file"}, {Name: "skill_manage"}},
		Executor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			return `{"ok":true}`, true, false, nil
		},
	}
	ctx := llmcontracts.WithRuntimeTools(context.Background(), baseTools)
	_, err := inv.Invoke(ctx, models.AgentLifecycleHook{AgentID: "custom-hook-agent", OutputContract: models.OutputContractActivitySummary}, HookInput{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	for _, allowed := range []string{"read_file", "write_file", "delete_file"} {
		if caller.lastRuntime == nil || !caller.lastRuntime.HasDefinition(allowed) {
			t.Fatalf("expected %s from ScopedFiles grant, got %#v", allowed, caller.lastRuntime)
		}
		if _, handled, isErr, err := caller.lastRuntime.Executor(ctx, allowed, json.RawMessage(`{}`)); !handled || isErr || err != nil {
			t.Fatalf("expected %s execution to pass filter handled=%v isErr=%v err=%v", allowed, handled, isErr, err)
		}
	}
	if caller.lastRuntime.HasDefinition("skill_manage") {
		t.Fatalf("ScopedFiles grant must not expose unrelated runtime tools: %#v", caller.lastRuntime.Definitions)
	}
}

func TestLLMHookInvoker_MemoryRecallRouteDoesNotExposeFileTools(t *testing.T) {
	caller := &fakeCaller{reply: `{"memories":[],"content":"","confidence":0,"reason":"none","needs_clarification":false}`}
	agentDef := &models.Agent{Key: models.AgentSystemKindMemoryCurator, Name: "Memory Curator", Model: "sonnet", SystemKind: models.AgentSystemKindMemoryCurator, Tools: []string{models.AgentToolScopedFiles}}
	inv := NewLLMHookInvoker(caller, &fakeAgentLookup{byID: map[string]*models.Agent{"memory-curator": agentDef}}, nil)
	baseTools := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "list_files"}, {Name: "read_file"}, {Name: "grep_search"}},
		Executor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			return `{"ok":true}`, true, false, nil
		},
	}
	ctx := llmcontracts.WithRuntimeTools(context.Background(), baseTools)
	_, err := inv.Invoke(ctx, models.AgentLifecycleHook{AgentID: "memory-curator", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories}, HookInput{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if caller.lastRuntime == nil {
		t.Fatal("expected runtime tools")
	}
	if !caller.calledWithDefNoTools || caller.calledWithDef {
		t.Fatalf("expected memory recall route hook to use no-tools definition caller, calledWithDefNoTools=%v calledWithDef=%v", caller.calledWithDefNoTools, caller.calledWithDef)
	}
	if !caller.lastRuntime.SkipDefaultTools {
		t.Fatal("expected memory recall route hook to suppress provider default tools")
	}
	if caller.lastAgentDef == nil {
		t.Fatal("expected sanitized agent definition")
	}
	if len(caller.lastAgentDef.Tools) != 0 || len(caller.lastAgentDef.ToolConfig.ScopedFiles) != 0 || len(caller.lastAgentDef.Plugins) != 0 || len(caller.lastAgentDef.MCPServers) != 0 {
		t.Fatalf("expected sanitized memory route agent definition, got tools=%v config=%#v plugins=%v mcp=%v", caller.lastAgentDef.Tools, caller.lastAgentDef.ToolConfig, caller.lastAgentDef.Plugins, caller.lastAgentDef.MCPServers)
	}
	if caller.lastAgentDef.SystemKind != models.AgentSystemKindMemoryCurator || caller.lastAgentDef.Name != "Memory Curator" {
		t.Fatalf("expected sanitized definition to preserve identity, got %#v", caller.lastAgentDef)
	}
	for _, denied := range []string{"list_files", "read_file", "grep_search"} {
		if caller.lastRuntime.HasDefinition(denied) {
			t.Fatalf("memory recall route hook must not expose %s: %#v", denied, caller.lastRuntime.Definitions)
		}
		if caller.lastRuntime.Executor != nil {
			if _, handled, _, err := caller.lastRuntime.Executor(ctx, denied, json.RawMessage(`{}`)); handled || err != nil {
				t.Fatalf("memory recall route hook should not execute %s handled=%v err=%v", denied, handled, err)
			}
		}
	}
}

func TestLLMHookInvoker_DoesNotExposeRuntimeToolsWithoutAgentGrants(t *testing.T) {
	caller := &fakeCaller{reply: `{"summary":"reviewed","nothing_to_save":true}`}
	agentDef := &models.Agent{Name: "Skill Curator", Model: "sonnet"}
	inv := NewLLMHookInvoker(caller, &fakeAgentLookup{byID: map[string]*models.Agent{"skill-curator": agentDef}}, nil)
	baseTools := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "skill_view"}, {Name: "skill_manage"}},
	}
	ctx := llmcontracts.WithRuntimeTools(context.Background(), baseTools)
	_, err := inv.Invoke(ctx, models.AgentLifecycleHook{AgentID: "skill-curator", OutputContract: models.OutputContractLearningSummary}, HookInput{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if caller.lastRuntime == nil {
		t.Fatal("expected filtered runtime tools object")
	}
	if len(caller.lastRuntime.Definitions) != 0 {
		t.Fatalf("expected no runtime tools without agent grants, got %#v", caller.lastRuntime.Definitions)
	}
}

func TestLLMHookInvoker_RecordsRuntimeToolTrace(t *testing.T) {
	caller := &fakeCaller{reply: `{"summary":"reviewed","nothing_to_save":true}`}
	agentDef := &models.Agent{Name: "Skill Curator", Model: "sonnet", Tools: []string{"skill_view"}}
	inv := NewLLMHookInvoker(caller, &fakeAgentLookup{byID: map[string]*models.Agent{"skill-curator": agentDef}}, nil)
	baseTools := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "skill_view"}},
		Executor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			return `{"ok":true}`, true, false, nil
		},
	}
	store := &memStore{}
	recorder := NewTraceRecorder(store, "exec-1", nil)
	ctx := context.Background()
	ctx = WithTraceRecorder(ctx, recorder)
	ctx = llmcontracts.WithRuntimeToolTraceRecorder(ctx, recorder)
	ctx = llmcontracts.WithRuntimeTools(ctx, baseTools)
	_, err := inv.Invoke(ctx, models.AgentLifecycleHook{AgentID: "skill-curator", OutputContract: models.OutputContractLearningSummary}, HookInput{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if caller.lastRuntime == nil || caller.lastRuntime.Executor == nil {
		t.Fatal("expected traced runtime executor")
	}
	if _, handled, _, err := caller.lastRuntime.Executor(ctx, "skill_view", json.RawMessage(`{"handle":"agent/skill"}`)); err != nil || !handled {
		t.Fatalf("execute traced tool handled=%v err=%v", handled, err)
	}
	var eventTypes []string
	for _, event := range store.events {
		eventTypes = append(eventTypes, event.EventType)
	}
	for _, want := range []string{"llm_prompt", "available_tools", "llm_raw_reply", "llm_extracted_json", "tool_call", "tool_result"} {
		if !containsString(eventTypes, want) {
			t.Fatalf("expected trace event %q in %v", want, eventTypes)
		}
	}
}

func TestLLMHookInvoker_RedactsMemoryCuratorFileToolTraceOutputs(t *testing.T) {
	secretMemory := "durable preference that should not be persisted raw in lifecycle traces"
	caller := &fakeCaller{reply: `{"content":"remember compactly","sources":["topic.md"],"confidence":0.8}`}
	agentDef := &models.Agent{Name: "Memory Curator", Model: "sonnet", SystemKind: models.AgentSystemKindMemoryCurator, Tools: []string{models.AgentToolScopedFiles}}
	inv := NewLLMHookInvoker(caller, &fakeAgentLookup{byID: map[string]*models.Agent{"memory-curator": agentDef}}, nil)
	baseTools := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "read_file"}},
		Executor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			return secretMemory, true, false, nil
		},
	}
	store := &memStore{}
	recorder := NewTraceRecorder(store, "exec-1", nil)
	ctx := context.Background()
	ctx = WithTraceRecorder(ctx, recorder)
	ctx = llmcontracts.WithRuntimeToolTraceRecorder(ctx, recorder)
	ctx = llmcontracts.WithRuntimeTools(ctx, baseTools)
	_, err := inv.Invoke(ctx, models.AgentLifecycleHook{AgentID: "memory-curator", SkillKey: "recall_memory", OutputContract: models.OutputContractContextBlock}, HookInput{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if caller.lastRuntime == nil || caller.lastRuntime.Executor == nil {
		t.Fatal("expected traced runtime executor")
	}
	if _, handled, _, err := caller.lastRuntime.Executor(caller.lastContext, "read_file", json.RawMessage(`{"file_path":"topic.md","limit":80}`)); err != nil || !handled {
		t.Fatalf("execute traced tool handled=%v err=%v", handled, err)
	}
	for _, event := range store.events {
		if event.EventType != "tool_result" {
			continue
		}
		if strings.Contains(event.PayloadJSON, secretMemory) {
			t.Fatalf("memory tool trace leaked raw output: %s", event.PayloadJSON)
		}
		if !strings.Contains(event.PayloadJSON, "[redacted memory tool output]") || !strings.Contains(event.PayloadJSON, "output_bytes") {
			t.Fatalf("memory tool trace missing redaction metadata: %s", event.PayloadJSON)
		}
		return
	}
	t.Fatal("expected tool_result trace event")
}

func TestLLMHookInvoker_RendersConversationTranscript(t *testing.T) {
	caller := &fakeCaller{reply: `{"summary":"reviewed","nothing_to_save":true}`}
	inv := NewLLMHookInvoker(caller, nil, nil)
	_, err := inv.Invoke(context.Background(), models.AgentLifecycleHook{OutputContract: models.OutputContractLearningSummary}, HookInput{
		TaskID: "task-1",
		Extras: map[string]any{
			ConversationTranscriptKey: llmcontracts.ChatContext{
				Messages: []llmcontracts.ChatContextMessage{
					{Role: "user", Content: "please fix the templ workflow"},
					{Role: "assistant", Content: "ran make templ and go test"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	for _, want := range []string{"Hook input", "conversation_transcript", "task-1", "please fix the templ workflow", "ran make templ and go test"} {
		if !strings.Contains(caller.lastPrompt, want) {
			t.Fatalf("expected rendered prompt to contain %q, got %q", want, caller.lastPrompt)
		}
	}
}

func TestMaintainAgentSkillLibraryPromptAvoidsUnnecessaryInternalLabels(t *testing.T) {
	for _, forbidden := range []string{"OpenVibely skill library", "Built-in system agent", "built-in system agent", "non-system agent", "generated skill", "generated skills"} {
		if strings.Contains(MaintainAgentSkillLibraryPrompt, forbidden) {
			t.Fatalf("maintenance prompt contains unnecessary internal/product wording %q:\n%s", forbidden, MaintainAgentSkillLibraryPrompt)
		}
	}
	for _, want := range []string{"user-managed agent-owned skill library", "Do not create, edit, archive, route, or reassign agents", "Never modify protected agent skills"} {
		if !strings.Contains(MaintainAgentSkillLibraryPrompt, want) {
			t.Fatalf("maintenance prompt missing %q:\n%s", want, MaintainAgentSkillLibraryPrompt)
		}
	}
}

func TestRenderHookPrompt_UsesReadableTemplateAndPreservesContractInstructions(t *testing.T) {
	prompt, err := renderHookPrompt(models.AgentLifecycleHook{
		PromptOverride: "Use compact phrasing.",
		OutputContract: models.OutputContractSelectedMemories,
	}, HookInput{
		SkillBody:  "# Recall Memory\nSelect relevant memory handles.",
		TaskID:     "task-template",
		TaskPrompt: "Use the provider memory.",
	})
	if err != nil {
		t.Fatalf("renderHookPrompt: %v", err)
	}
	for _, want := range []string{
		"# Recall Memory",
		"Use compact phrasing.",
		"Return one JSON object that matches the `selected_memories` output contract.",
		"Required JSON shape",
		"Choose only memory file handles listed in available_memories for this turn.",
		"Hook input:\n```json",
		`"task_id": "task-template"`,
		`"task_prompt": "Use the provider memory."`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("rendered prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "OpenVibely") {
		t.Fatalf("generic lifecycle hook prompt should not inject product name:\n%s", prompt)
	}
}

func TestLLMHookInvoker_LearningSummaryPromptIncludesExactJSONShape(t *testing.T) {
	caller := &fakeCaller{reply: `{"summary":"reviewed","nothing_to_save":true}`}
	inv := NewLLMHookInvoker(caller, nil, nil)
	_, err := inv.Invoke(context.Background(), models.AgentLifecycleHook{OutputContract: models.OutputContractLearningSummary}, HookInput{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	for _, want := range []string{"nothing_to_save", "created_skills", "agent_skill_manage", "Agent definition fields", `{"summary":"No durable learning to save.","nothing_to_save":true}`} {
		if !strings.Contains(caller.lastPrompt, want) {
			t.Fatalf("expected rendered prompt to contain %q, got %q", want, caller.lastPrompt)
		}
	}
}

func TestLLMHookInvoker_ObserveSkillPromptTreatsMissingCoverageAsCreateSignal(t *testing.T) {
	caller := &fakeCaller{reply: `{"summary":"reviewed","nothing_to_save":true}`}
	inv := NewLLMHookInvoker(caller, nil, nil)
	_, err := inv.Invoke(context.Background(), models.AgentLifecycleHook{OutputContract: models.OutputContractLearningSummary}, HookInput{
		SkillBody: strings.Join([]string{
			"# Observe Task For Learning",
			"Missing coverage is not a no-op reason.",
			"create or patch a selectable primary coding/repository agent",
		}, "\n"),
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	for _, want := range []string{"Missing coverage is not a no-op reason", "selectable primary coding/repository agent"} {
		if !strings.Contains(caller.lastPrompt, want) {
			t.Fatalf("expected rendered observe prompt to contain %q, got %q", want, caller.lastPrompt)
		}
	}
}

func TestLLMHookInvoker_ExtractsInlineJSONAfterProse(t *testing.T) {
	caller := &fakeCaller{reply: "I found no durable learning.\n{\"summary\":\"No durable learning to save.\",\"nothing_to_save\":true}\nThanks."}
	inv := NewLLMHookInvoker(caller, nil, nil)
	raw, err := inv.Invoke(context.Background(), models.AgentLifecycleHook{OutputContract: models.OutputContractLearningSummary}, HookInput{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if err := ValidateOutput(models.OutputContractLearningSummary, raw); err != nil {
		t.Fatalf("expected extracted JSON to validate, got %q: %v", string(raw), err)
	}
}

func TestLLMHookInvoker_FallsBackToDefault(t *testing.T) {
	caller := &fakeCaller{reply: `{"selected_mode":"x","mode":"x","action":"continue","confidence":0.5}`}
	def := &models.LLMConfig{Name: "default", Model: "haiku"}
	inv := NewLLMHookInvoker(caller, &fakeAgentLookup{byID: map[string]*models.Agent{}}, &fakeLLMConfig{def: def})
	if _, err := inv.Invoke(context.Background(), models.AgentLifecycleHook{AgentID: "missing"}, HookInput{}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if caller.lastConfig.Model != "haiku" {
		t.Fatalf("expected default config to be used, got %q", caller.lastConfig.Model)
	}
}

func TestLLMHookInvoker_ExtractsFencedJSON(t *testing.T) {
	caller := &fakeCaller{reply: "Here you go:\n```json\n{\"a\":1}\n```\n"}
	inv := NewLLMHookInvoker(caller, nil, nil)
	raw, err := inv.Invoke(context.Background(), models.AgentLifecycleHook{}, HookInput{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var v map[string]int
	if err := json.Unmarshal(raw, &v); err != nil || v["a"] != 1 {
		t.Fatalf("expected extracted JSON {a:1}, got %q (err=%v)", string(raw), err)
	}
}

func TestLLMHookInvoker_NilCaller(t *testing.T) {
	var inv *LLMHookInvoker
	if _, err := inv.Invoke(context.Background(), models.AgentLifecycleHook{}, HookInput{}); err == nil {
		t.Fatal("expected error for nil invoker")
	}
	inv2 := &LLMHookInvoker{}
	if _, err := inv2.Invoke(context.Background(), models.AgentLifecycleHook{}, HookInput{}); err == nil {
		t.Fatal("expected error for nil caller")
	}
}

func TestExtractJSONPayloadForContract_ReturnsFirstValidObjectFromConcatenatedReply(t *testing.T) {
	reply := `{"task_id":"current"}{}{"task_id":"current"}{"summary":"Marked the task goal achieved because current evidence shows README.md was minimally updated with the verified full test suite command and the note was confirmed present.","changed_paths":["README.md"],"created":[],"updated":[],"skipped":false,"skip_reason":""}`
	got := extractJSONPayloadForContract(reply, models.OutputContractActivitySummary)
	want := `{"summary":"Marked the task goal achieved because current evidence shows README.md was minimally updated with the verified full test suite command and the note was confirmed present.","changed_paths":["README.md"],"created":[],"updated":[],"skipped":false,"skip_reason":""}`
	if got != want {
		t.Fatalf("extractJSONPayloadForContract() = %q, want %q", got, want)
	}
	if err := ValidateOutput(models.OutputContractActivitySummary, json.RawMessage(got)); err != nil {
		t.Fatalf("extracted payload should validate: %v", err)
	}
}

func TestRenderHookPrompt_SanitizesInvalidPreviousOutputPayload(t *testing.T) {
	_, err := renderHookPrompt(models.AgentLifecycleHook{OutputContract: models.OutputContractLearningSummary}, HookInput{
		TaskID: "task",
		PreviousOutputs: []HookOutput{{
			HookID:         "goal-hook",
			SkillKey:       "evaluate_task_goal",
			OutputContract: models.OutputContractActivitySummary,
			Payload:        json.RawMessage(`{"summary":"one"}{"summary":"two"}`),
			Error:          "validate output: activity_summary: invalid JSON: invalid character '{' after top-level value",
		}},
	})
	if err != nil {
		t.Fatalf("renderHookPrompt should not fail on invalid previous payload: %v", err)
	}
}

func TestRenderHookPrompt_StripsPreviousOutputExecutionID(t *testing.T) {
	prompt, err := renderHookPrompt(models.AgentLifecycleHook{OutputContract: models.OutputContractSelectedSkills}, HookInput{
		TaskID: "task",
		PreviousOutputs: []HookOutput{{
			HookID:         "hook-1",
			SkillKey:       "skill_curator/route",
			OutputContract: models.OutputContractSelectedSkills,
			Payload:        json.RawMessage(`{"skills":["skill_a"]}`),
			ExecutionID:    "internal-db-uuid-should-not-appear",
		}},
	})
	if err != nil {
		t.Fatalf("renderHookPrompt: %v", err)
	}
	if strings.Contains(prompt, "internal-db-uuid-should-not-appear") {
		t.Fatalf("prompt must not contain ExecutionID from PreviousOutputs, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "execution_id") {
		t.Fatalf("prompt must not contain the execution_id key, got:\n%s", prompt)
	}
}

func TestLLMHookInvoker_AttachesHookAgentToolsToRuntimeContext(t *testing.T) {
	caller := &fakeCaller{reply: `{"summary":"ok","changed_paths":[],"skipped":false}`}
	agentDef := &models.Agent{ID: "custom-agent", Name: "Custom Hook", Tools: []string{"mark_task_goal_achieved", "send_to_task"}}
	inv := NewLLMHookInvoker(caller, &fakeAgentLookup{byID: map[string]*models.Agent{"custom-agent": agentDef}}, nil)
	ctx := llmcontracts.WithRuntimeTools(context.Background(), &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "mark_task_goal_achieved"}, {Name: "send_to_task"}}})
	_, err := inv.Invoke(ctx, models.AgentLifecycleHook{AgentID: "custom-agent", OutputContract: models.OutputContractActivitySummary}, HookInput{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	hookAgent, ok := HookAgentFromContext(caller.lastContext)
	if !ok {
		t.Fatal("expected hook-agent context")
	}
	if hookAgent.AgentID != "custom-agent" {
		t.Fatalf("hook agent id = %q", hookAgent.AgentID)
	}
	if strings.Join(hookAgent.Tools, ",") != "mark_task_goal_achieved,send_to_task" {
		t.Fatalf("hook agent tools = %#v", hookAgent.Tools)
	}
}

func TestLLMHookInvoker_GoalAgentRequiresGoalRuntimeTools(t *testing.T) {
	caller := &fakeCaller{reply: `{"summary":"ok","changed_paths":[],"skipped":false}`}
	agentDef := &models.Agent{Key: models.AgentSystemKindGoal, SystemKind: models.AgentSystemKindGoal, Name: "System: Goal Agent", Tools: []string{"get_task_goal"}}
	inv := NewLLMHookInvoker(caller, &fakeAgentLookup{byID: map[string]*models.Agent{"goal-agent": agentDef}}, nil)
	ctx := llmcontracts.WithRuntimeTools(context.Background(), &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "get_task_goal"}}})
	_, err := inv.Invoke(ctx, models.AgentLifecycleHook{AgentID: "goal-agent", SkillKey: "evaluate_task_goal", OutputContract: models.OutputContractActivitySummary}, HookInput{})
	if err == nil || !strings.Contains(err.Error(), "required runtime tools unavailable") {
		t.Fatalf("expected required-tool preflight error, got %v", err)
	}
	if caller.calledDirect || caller.calledWithDef {
		t.Fatal("missing required tools should fail before calling the model")
	}
}

func TestLLMHookInvoker_GoalAgentRequiredRuntimeToolsAvailable(t *testing.T) {
	caller := &fakeCaller{reply: `{"summary":"ok","changed_paths":[],"skipped":false}`}
	agentDef := &models.Agent{Key: models.AgentSystemKindGoal, SystemKind: models.AgentSystemKindGoal, Name: "System: Goal Agent", Tools: []string{"get_task_goal", "send_to_task", "mark_task_goal_achieved", "report_task_goal_blocked"}}
	inv := NewLLMHookInvoker(caller, &fakeAgentLookup{byID: map[string]*models.Agent{"goal-agent": agentDef}}, nil)
	ctx := llmcontracts.WithRuntimeTools(context.Background(), &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{
		{Name: "get_task_goal"},
		{Name: "send_to_task"},
		{Name: "mark_task_goal_achieved"},
		{Name: "report_task_goal_blocked"},
	}})
	_, err := inv.Invoke(ctx, models.AgentLifecycleHook{AgentID: "goal-agent", SkillKey: "evaluate_task_goal", OutputContract: models.OutputContractActivitySummary}, HookInput{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !caller.calledWithDef {
		t.Fatal("expected model call when required tools are available")
	}
}

func TestLLMHookInvoker_SkillCuratorRouteHookGetsNoTools(t *testing.T) {
	caller := &fakeCaller{reply: `{"skills":[],"confidence":0.5,"reason":"none","needs_clarification":false}`}
	agentDef := &models.Agent{
		Key: models.AgentSystemKindSkillCurator, Name: "Skill Curator", Model: "sonnet",
		SystemKind: models.AgentSystemKindSkillCurator,
		Tools:      []string{"skill_view", "skills_list", "agent_list", "agent_view"},
	}
	inv := NewLLMHookInvoker(caller, &fakeAgentLookup{byID: map[string]*models.Agent{"skill-curator": agentDef}}, nil)
	baseTools := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "skill_view"}, {Name: "skills_list"}, {Name: "agent_list"}, {Name: "agent_view"}},
	}
	ctx := llmcontracts.WithRuntimeTools(context.Background(), baseTools)
	hook := models.AgentLifecycleHook{
		AgentID: "skill-curator", When: models.LifecycleRouteTask,
		SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills,
	}
	if _, err := inv.Invoke(ctx, hook, HookInput{}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !caller.calledWithDefNoTools || caller.calledWithDef {
		t.Fatalf("expected skill route hook to use the no-tools caller, noTools=%v withDef=%v", caller.calledWithDefNoTools, caller.calledWithDef)
	}
	if !caller.lastRuntime.SkipDefaultTools {
		t.Fatal("expected skill route hook to suppress provider default tools")
	}
	for _, denied := range []string{"skill_view", "skills_list", "agent_list", "agent_view"} {
		if caller.lastRuntime.HasDefinition(denied) {
			t.Fatalf("skill route hook must not carry %s: %#v", denied, caller.lastRuntime.Definitions)
		}
	}
	if caller.lastAgentDef == nil || caller.lastAgentDef.SystemKind != models.AgentSystemKindSkillCurator {
		t.Fatalf("expected sanitized definition to preserve identity, got %#v", caller.lastAgentDef)
	}
}

func TestLLMHookInvoker_AfterCompleteLearningHookKeepsTools(t *testing.T) {
	caller := &fakeCaller{reply: `{"summary":"noted","nothing_to_save":true}`}
	agentDef := &models.Agent{
		Key: models.AgentSystemKindSkillCurator, Name: "Skill Curator", Model: "sonnet",
		SystemKind: models.AgentSystemKindSkillCurator,
		Tools:      []string{"skill_view", "skill_manage"},
	}
	inv := NewLLMHookInvoker(caller, &fakeAgentLookup{byID: map[string]*models.Agent{"skill-curator": agentDef}}, nil)
	baseTools := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "skill_view"}, {Name: "skill_manage"}},
	}
	ctx := llmcontracts.WithRuntimeTools(context.Background(), baseTools)
	hook := models.AgentLifecycleHook{
		AgentID: "skill-curator", When: models.LifecycleAfterComplete,
		SkillKey: "observe_task_for_learning", OutputContract: models.OutputContractLearningSummary,
	}
	if _, err := inv.Invoke(ctx, hook, HookInput{}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if caller.calledWithDefNoTools {
		t.Fatal("after-complete learning hook must keep its mutation tools")
	}
	for _, want := range []string{"skill_view", "skill_manage"} {
		if !caller.lastRuntime.HasDefinition(want) {
			t.Fatalf("expected %s to remain available", want)
		}
	}
}

func TestLLMHookInvoker_CustomRouteHookKeepsTools(t *testing.T) {
	caller := &fakeCaller{reply: `{"skills":[],"confidence":0.5,"reason":"none","needs_clarification":false}`}
	agentDef := &models.Agent{Key: "custom_router", Name: "Custom Router", Model: "sonnet", Tools: []string{"skill_view"}}
	inv := NewLLMHookInvoker(caller, &fakeAgentLookup{byID: map[string]*models.Agent{"custom": agentDef}}, nil)
	baseTools := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "skill_view"}}}
	ctx := llmcontracts.WithRuntimeTools(context.Background(), baseTools)
	hook := models.AgentLifecycleHook{
		AgentID: "custom", When: models.LifecycleRouteTask,
		SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills,
	}
	if _, err := inv.Invoke(ctx, hook, HookInput{}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if caller.calledWithDefNoTools {
		t.Fatal("custom route hooks must keep their tool access")
	}
	if !caller.lastRuntime.HasDefinition("skill_view") {
		t.Fatal("expected custom route hook to keep skill_view")
	}
}

func TestLLMHookInvoker_MarksLifecycleHookCall(t *testing.T) {
	caller := &fakeCaller{reply: `{"summary":"ok","nothing_to_save":true}`}
	agentDef := &models.Agent{Name: "Skill Curator", Model: "sonnet"}
	inv := NewLLMHookInvoker(caller, &fakeAgentLookup{byID: map[string]*models.Agent{"a": agentDef}}, nil)
	hook := models.AgentLifecycleHook{AgentID: "a", OutputContract: models.OutputContractLearningSummary}
	if _, err := inv.Invoke(context.Background(), hook, HookInput{}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !llmcontracts.LifecycleHookCallFromContext(caller.lastContext) {
		t.Fatal("expected hook calls to be marked so the direct path drops coding-agent framing")
	}
}

func TestRenderHookPromptDoesNotRepeatPromptOverride(t *testing.T) {
	hook := models.AgentLifecycleHook{PromptOverride: "SENTINEL_OVERRIDE_TEXT"}
	prompt, err := renderHookPrompt(hook, HookInput{TaskID: "t1", PromptOverride: "SENTINEL_OVERRIDE_TEXT"})
	if err != nil {
		t.Fatalf("renderHookPrompt: %v", err)
	}
	if got := strings.Count(prompt, "SENTINEL_OVERRIDE_TEXT"); got != 1 {
		t.Fatalf("prompt override appears %d times, want 1:\n%s", got, prompt)
	}
}

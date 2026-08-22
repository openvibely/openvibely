package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/util"
)

// AgentCaller is the subset of the LLM service the LLM-backed HookInvoker
// uses. Defined as an interface so the invoker can be unit-tested without
// pulling in the full LLM service and so callers can shim provider-specific
// behavior.
type AgentCaller interface {
	// CallAgentDirect dispatches the supplied prompt against the configured LLM
	// provider for the agent and returns the raw text reply. Request-scoped
	// runtime tools may be attached to ctx and are available to learning hooks.
	CallAgentDirect(
		ctx context.Context,
		message string,
		attachments []models.Attachment,
		agent models.LLMConfig,
		workDir string,
	) (string, int, error)
}

type AgentDefinitionCaller interface {
	CallAgentDirectWithDefinition(
		ctx context.Context,
		message string,
		attachments []models.Attachment,
		agent models.LLMConfig,
		workDir string,
		agentDef *models.Agent,
	) (string, int, error)
}

type AgentDefinitionNoToolsCaller interface {
	CallAgentDirectWithDefinitionNoTools(
		ctx context.Context,
		message string,
		attachments []models.Attachment,
		agent models.LLMConfig,
		workDir string,
		agentDef *models.Agent,
	) (string, int, error)
}

// AgentLookup resolves a persisted agent by ID so the invoker can pick the
// correct system prompt and tool grants for the hook. Optional: when nil the
// invoker emits a default-shaped LLMConfig populated only from the model
// preference on the hook.
type AgentLookup interface {
	GetByID(ctx context.Context, id string) (*models.Agent, error)
}

// LLMConfigLookup resolves the model-routing config used by the agent. The
// runbook (§Auto-Routing) talks about per-agent model defaults; this lookup
// applies them. Optional: a nil lookup falls back to "inherit".
type LLMConfigLookup interface {
	GetDefault(ctx context.Context) (*models.LLMConfig, error)
}

// LLMHookInvoker dispatches a lifecycle hook to the LLM service. The hook
// input is rendered as a prompt block (skill body + prompt override + serialized
// previous outputs); the model is expected to return a JSON payload matching
// the configured output_contract.
//
// Wiring example (in server.go):
//
//	invoker := lifecycle.NewLLMHookInvoker(llmSvc, agentRepo, llmConfigRepo)
//	runner := lifecycle.NewRunner(lifecycleRepo, invoker, skillResolver)
type LLMHookInvoker struct {
	caller AgentCaller
	agents AgentLookup
	models LLMConfigLookup
}

// NewLLMHookInvoker constructs the production invoker. caller is required;
// agents/models are optional but recommended.
func NewLLMHookInvoker(caller AgentCaller, agents AgentLookup, modelCfg LLMConfigLookup) *LLMHookInvoker {
	return &LLMHookInvoker{caller: caller, agents: agents, models: modelCfg}
}

// Invoke renders the prompt and asks the LLM for a structured reply. The
// returned bytes are passed to ValidateOutput by the runner. If the model
// returns a fenced ```json block, we extract its body so the runner does not
// have to handle Markdown formatting.
func (i *LLMHookInvoker) Invoke(ctx context.Context, hook models.AgentLifecycleHook, input HookInput) (json.RawMessage, error) {
	if i == nil || i.caller == nil {
		return nil, errors.New("lifecycle: LLMHookInvoker not configured")
	}
	prompt, err := renderHookPrompt(hook, input)
	if err != nil {
		return nil, err
	}
	cfg, agentDef, err := i.resolveLLMConfig(ctx, hook)
	if err != nil {
		return nil, err
	}
	callCtx := contextWithHookRuntimeTools(ctx, hook, agentDef)
	callCtx = llmcontracts.WithLifecycleHookCall(callCtx)
	executionError, _ := input.Extras[ExecutionErrorKey].(string)
	callCtx = WithHookAgent(callCtx, HookAgent{AgentID: hook.AgentID, SystemKind: systemKindForHookAgent(agentDef), Tools: hookAgentTools(agentDef), TaskID: input.TaskID, TaskRunID: input.TaskRunID, ExecutionError: executionError})
	if err := validateRequiredLifecycleRuntimeTools(hook, agentDef, llmcontracts.RuntimeToolsFromContext(callCtx)); err != nil {
		RecordTraceEvent(callCtx, "available_tools_missing", map[string]any{"error": err.Error(), "tools": runtimeToolNamesForTrace(callCtx)})
		return nil, err
	}
	RecordTraceEvent(callCtx, "llm_prompt", map[string]any{
		"agent_id":        hook.AgentID,
		"agent_key":       agentKeyForTrace(agentDef),
		"skill_key":       hook.SkillKey,
		"output_contract": string(hook.OutputContract),
		"work_dir":        input.WorkDir,
		"prompt":          prompt,
	})
	RecordTraceEvent(callCtx, "available_tools", map[string]any{"tools": runtimeToolNamesForTrace(callCtx)})
	var reply string
	callAgentDef := agentDefinitionForHookCall(hook, agentDef)
	if noToolsCaller, ok := i.caller.(AgentDefinitionNoToolsCaller); ok && callAgentDef != nil && isSelectionOnlyRouteHook(hook, agentDef) {
		reply, _, err = noToolsCaller.CallAgentDirectWithDefinitionNoTools(callCtx, prompt, nil, cfg, input.WorkDir, callAgentDef)
	} else if defCaller, ok := i.caller.(AgentDefinitionCaller); ok && callAgentDef != nil {
		reply, _, err = defCaller.CallAgentDirectWithDefinition(callCtx, prompt, nil, cfg, input.WorkDir, callAgentDef)
	} else {
		reply, _, err = i.caller.CallAgentDirect(callCtx, prompt, nil, cfg, input.WorkDir)
	}
	if err != nil {
		RecordTraceEvent(callCtx, "llm_error", map[string]any{"error": err.Error()})
		return nil, fmt.Errorf("lifecycle: LLM call failed: %w", err)
	}
	RecordTraceEvent(callCtx, "llm_raw_reply", map[string]any{"reply": reply})
	extracted := extractJSONPayloadForContract(reply, hook.OutputContract)
	RecordTraceEvent(callCtx, "llm_extracted_json", map[string]any{"json": extracted})
	return json.RawMessage(extracted), nil
}

// resolveLLMConfig picks the model the hook should run under. Preference:
//  1. hook's persisted model override (future column; not implemented yet)
//  2. agent's model field
//  3. the configured default LLMConfig
//  4. an empty config (so the caller's defaults apply)
func (i *LLMHookInvoker) resolveLLMConfig(ctx context.Context, hook models.AgentLifecycleHook) (models.LLMConfig, *models.Agent, error) {
	var agentDef *models.Agent
	if i.agents != nil && hook.AgentID != "" {
		if a, err := i.agents.GetByID(ctx, hook.AgentID); err == nil && a != nil {
			agentDef = a
			cfg := models.LLMConfig{Name: a.Name, Model: a.Model}
			if a.Model != "" && a.Model != "inherit" {
				return cfg, agentDef, nil
			}
		}
	}
	if i.models != nil {
		if def, err := i.models.GetDefault(ctx); err == nil && def != nil {
			return *def, agentDef, nil
		}
	}
	return models.LLMConfig{Model: "inherit"}, agentDef, nil
}

const (
	lifecycleHookInputPromptTemplate = "Hook input:\n```json\n%s\n```"
	outputContractPromptTemplate     = "Return one JSON object that matches the `%s` output contract. Do not wrap the JSON in prose.%s"
)

// renderHookPrompt builds the prompt the hook sends to the LLM. The skill
// body anchors the procedure; the prompt override (if any) is appended; the
// hook input snapshot lands in a fenced JSON block so the model can read the
// relevant context without ambiguity.
func renderHookPrompt(hook models.AgentLifecycleHook, input HookInput) (string, error) {
	encoded, err := json.MarshalIndent(sanitizedHookInputForPrompt(input), "", "  ")
	if err != nil {
		return "", fmt.Errorf("render hook prompt: %w", err)
	}

	sections := compactPromptSections(
		input.SkillBody,
		hook.PromptOverride,
		renderOutputContractPrompt(hook.OutputContract),
		fmt.Sprintf(lifecycleHookInputPromptTemplate, encoded),
	)
	return strings.Join(sections, "\n\n") + "\n", nil
}

func compactPromptSections(parts ...string) []string {
	sections := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			sections = append(sections, part)
		}
	}
	return sections
}

func renderOutputContractPrompt(contract models.LifecycleOutputContract) string {
	if contract == "" {
		return ""
	}
	spec := outputContractPromptSpec(contract)
	if spec != "" {
		spec = "\n" + spec
	}
	return fmt.Sprintf(outputContractPromptTemplate, contract, spec)
}

// sanitizedHookInputForPrompt strips fields that renderHookPrompt already
// emits as their own prompt section. Leaving them in the JSON block would send
// the same text to the model twice.
func sanitizedHookInputForPrompt(input HookInput) HookInput {
	input.SkillBody = ""
	input.PromptOverride = ""
	input.PreviousOutputs = sanitizeHookOutputsForPrompt(input.PreviousOutputs)
	return input
}

type HookAgent struct {
	AgentID        string
	SystemKind     string
	Tools          []string
	TaskID         string
	TaskRunID      string
	ExecutionError string
}

type hookAgentCtxKey struct{}

func WithHookAgent(ctx context.Context, agent HookAgent) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, hookAgentCtxKey{}, agent)
}

func HookAgentFromContext(ctx context.Context) (HookAgent, bool) {
	if ctx == nil {
		return HookAgent{}, false
	}
	agent, ok := ctx.Value(hookAgentCtxKey{}).(HookAgent)
	return agent, ok
}

func systemKindForHookAgent(agentDef *models.Agent) string {
	if agentDef == nil {
		return ""
	}
	return agentDef.SystemKind
}

func hookAgentTools(agentDef *models.Agent) []string {
	if agentDef == nil || len(agentDef.Tools) == 0 {
		return nil
	}
	tools := make([]string, len(agentDef.Tools))
	copy(tools, agentDef.Tools)
	return tools
}

func sanitizeHookOutputsForPrompt(outputs []HookOutput) []HookOutput {
	if len(outputs) == 0 {
		return outputs
	}
	out := make([]HookOutput, 0, len(outputs))
	for _, output := range outputs {
		if len(output.Payload) > 0 && !json.Valid(output.Payload) {
			output.Payload = json.RawMessage(strconv.Quote(string(output.Payload)))
		}
		// Strip internal DB metadata that must never be exposed to the model.
		output.ExecutionID = ""
		out = append(out, output)
	}
	return out
}

func contextWithHookRuntimeTools(ctx context.Context, hook models.AgentLifecycleHook, agentDef *models.Agent) context.Context {
	base := llmcontracts.RuntimeToolsFromContext(ctx)
	filtered := filterRuntimeToolsForAgent(base, agentDef)
	filtered = filterRuntimeToolsForHook(filtered, hook, agentDef)
	filtered = llmcontracts.TraceRuntimeTools(filtered, TraceRecorderFromContext(ctx))
	if filtered == base {
		return ctx
	}
	return llmcontracts.WithRuntimeTools(ctx, filtered)
}

func contextWithAgentRuntimeTools(ctx context.Context, agentDef *models.Agent) context.Context {
	return contextWithHookRuntimeTools(ctx, models.AgentLifecycleHook{}, agentDef)
}

func filterRuntimeToolsForAgent(rt *llmcontracts.RuntimeTools, agentDef *models.Agent) *llmcontracts.RuntimeTools {
	if rt == nil || agentDef == nil {
		return rt
	}
	allowed := allowedRuntimeToolNamesForAgent(agentDef)
	defs := make([]llmcontracts.RuntimeToolDefinition, 0, len(rt.Definitions))
	for _, def := range rt.Definitions {
		if _, ok := allowed[strings.ToLower(strings.TrimSpace(def.Name))]; ok {
			defs = append(defs, def)
		}
	}
	return &llmcontracts.RuntimeTools{
		Definitions:      defs,
		Executor:         filteredRuntimeToolExecutor(rt.Executor, allowed),
		Filter:           filteredRuntimeToolFilter(rt.Filter, allowed),
		Metadata:         rt.Metadata,
		SkipDefaultTools: rt.SkipDefaultTools,
	}
}

func filterRuntimeToolsForHook(rt *llmcontracts.RuntimeTools, hook models.AgentLifecycleHook, agentDef *models.Agent) *llmcontracts.RuntimeTools {
	if rt == nil || agentDef == nil {
		return rt
	}
	if isSelectionOnlyRouteHook(hook, agentDef) {
		return &llmcontracts.RuntimeTools{SkipDefaultTools: true}
	}
	return rt
}

func agentDefinitionForHookCall(hook models.AgentLifecycleHook, agentDef *models.Agent) *models.Agent {
	if agentDef == nil || !isSelectionOnlyRouteHook(hook, agentDef) {
		return agentDef
	}
	copyDef := *agentDef
	copyDef.Tools = nil
	copyDef.ToolConfig = models.AgentToolConfig{SkipDefaultTools: true}
	copyDef.Plugins = nil
	copyDef.MCPServers = nil
	return &copyDef
}

// isSelectionOnlyRouteHook reports whether a route_task hook is a pure
// selection step whose index already arrives in the hook payload. These hooks
// need no tools at all: attaching them costs tool-schema tokens on every task
// turn and invites a classifier into a tool loop that re-sends the whole
// prompt each iteration.
//
// Scoped to built-in system agents so custom route hooks, whose skill bodies
// may legitimately call read tools, keep their existing tool access.
func isSelectionOnlyRouteHook(hook models.AgentLifecycleHook, agentDef *models.Agent) bool {
	if agentDef == nil || hook.When != models.LifecycleRouteTask {
		return false
	}
	switch agentDef.SystemKind {
	case models.AgentSystemKindMemoryCurator:
		return hook.OutputContract == models.OutputContractSelectedMemories
	case models.AgentSystemKindSkillCurator:
		return hook.OutputContract == models.OutputContractSelectedSkills
	default:
		return false
	}
}

func allowedRuntimeToolNamesForAgent(agentDef *models.Agent) map[string]struct{} {
	allowed := make(map[string]struct{}, len(agentDef.Tools))
	for _, tool := range agentDef.Tools {
		name := strings.ToLower(strings.TrimSpace(tool))
		if name == "" {
			continue
		}
		allowed[name] = struct{}{}
		if strings.EqualFold(name, models.AgentToolScopedFiles) {
			for _, scopedTool := range []string{"list_files", "read_file", "write_file", "edit_file", "grep_search", "delete_file"} {
				allowed[scopedTool] = struct{}{}
			}
		}
	}
	return allowed
}

func filteredRuntimeToolExecutor(base llmcontracts.RuntimeToolExecutor, allowed map[string]struct{}) llmcontracts.RuntimeToolExecutor {
	if base == nil {
		return nil
	}
	return func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		if _, ok := allowed[strings.ToLower(strings.TrimSpace(name))]; !ok {
			return "", false, false, nil
		}
		return base(ctx, name, input)
	}
}

func filteredRuntimeToolFilter(base llmcontracts.RuntimeToolFilter, allowed map[string]struct{}) llmcontracts.RuntimeToolFilter {
	return func(name string) (bool, bool) {
		key := strings.ToLower(strings.TrimSpace(name))
		if _, ok := allowed[key]; !ok {
			return false, true
		}
		if base != nil {
			allow, handled := base(name)
			if handled {
				return allow, true
			}
		}
		return true, true
	}
}

func validateRequiredLifecycleRuntimeTools(hook models.AgentLifecycleHook, agentDef *models.Agent, rt *llmcontracts.RuntimeTools) error {
	if agentDef == nil || agentDef.SystemKind != models.AgentSystemKindGoal || hook.SkillKey != "evaluate_task_goal" {
		return nil
	}
	missing := []string{}
	for _, name := range []string{"get_task_goal", "send_to_task", "mark_task_goal_achieved", "report_task_goal_blocked"} {
		if rt == nil || !rt.HasDefinition(name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("goal evaluation required runtime tools unavailable: %s", strings.Join(missing, ", "))
	}
	return nil
}

func runtimeToolNamesForTrace(ctx context.Context) []string {
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	if rt == nil {
		return nil
	}
	names := make([]string, 0, len(rt.Definitions))
	for _, def := range rt.Definitions {
		name := strings.TrimSpace(def.Name)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func agentKeyForTrace(agentDef *models.Agent) string {
	if agentDef == nil {
		return ""
	}
	return agentDef.Key
}

func outputContractPromptSpec(contract models.LifecycleOutputContract) string {
	switch contract {
	case models.OutputContractLearningSummary:
		return "Required JSON shape: {\"summary\":\"string\",\"nothing_to_save\":boolean,\"created_skills\":[\"skill_or_assigned_agent/skill\"],\"updated_skills\":[\"skill_or_assigned_agent/skill\"],\"archived_skills\":[\"skill_or_assigned_agent/skill\"],\"support_files_written\":[\"path\"],\"blocked_changes\":[\"reason\"],\"evidence_refs\":[{\"task_id\":\"id\",\"task_run_id\":\"id\",\"reason\":\"why\"}]}. Agent definition fields must be omitted or empty; agents are user-managed. Agent-owned skill changes are allowed only through server-scoped agent_skill_manage for the task's assigned agent. If there is nothing durable to save, return exactly: {\"summary\":\"No durable learning to save.\",\"nothing_to_save\":true}"
	case models.OutputContractSelectedMode:
		return "Required JSON shape: {\"mode\":\"agent_key\",\"action\":\"continue|switch\",\"confidence\":0.0,\"reason\":\"string\",\"needs_clarification\":false,\"clarifying_question\":\"string\"}."
	case models.OutputContractSelectedSkills:
		return "Required JSON shape: {\"skills\":[\"skill_key\"],\"confidence\":0.0,\"reason\":\"Why these skills fit the task\",\"needs_clarification\":false,\"clarifying_question\":\"string\"}. Choose only handles listed in available_skills for this turn. Return an empty skills array when no listed skill is relevant."
	case models.OutputContractSelectedMemories:
		return "Required JSON shape: {\"memories\":[{\"file\":\"memory_file.md\",\"topic\":\"optional debug label\"}],\"confidence\":0.0,\"reason\":\"Why these memory handles fit the task\",\"needs_clarification\":false,\"clarifying_question\":\"string\"}. Choose only memory file handles listed in available_memories for this turn. Task context will receive selected file handles only; topic is debug metadata and content, summary, or snippet fields must stay empty. Return an empty memories array when no listed memory is relevant."
	case models.OutputContractContextBlock:
		return "Required JSON shape: {\"content\":\"string\",\"sources\":[\"source\"],\"confidence\":0.0}."
	case models.OutputContractActivitySummary:
		return "Required JSON shape: {\"summary\":\"string\",\"changed_paths\":[\"path\"],\"created\":[\"id\"],\"updated\":[\"id\"],\"skipped\":false,\"skip_reason\":\"string\"}."
	case models.OutputContractLibraryUpdateSummary:
		return "Required JSON shape: {\"summary\":\"string\",\"created_skills\":[\"agent/skill\"],\"updated_skills\":[\"agent/skill\"],\"archived_skills\":[\"agent/skill\"],\"skill_consolidations\":[{\"from\":\"agent/skill\",\"into\":\"agent/skill\",\"reason\":\"why\"}],\"skill_prunings\":[{\"handle\":\"agent/skill\",\"reason\":\"why\"}],\"blocked_changes\":[\"reason\"]}. Agent arrays/consolidations/prunings must be omitted or empty; agents are user-managed."
	default:
		return ""
	}
}

// extractJSONPayload pulls the first JSON value out of a model reply. If the
// reply is already a JSON object/array, it is returned verbatim. Otherwise the
// function looks for fenced JSON and then for the first balanced inline JSON
// object/array. Falling all the way through returns the trimmed reply so the
// validator can produce the canonical error.
func extractJSONPayload(reply string) string {
	return extractJSONPayloadForContract(reply, "")
}

func extractJSONPayloadForContract(reply string, contract models.LifecycleOutputContract) string {
	s := strings.TrimSpace(reply)
	if s == "" {
		return s
	}
	candidates := util.JSONPayloadCandidates(s)
	for _, candidate := range candidates {
		if ValidateOutput(contract, json.RawMessage(candidate)) == nil {
			return candidate
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return s
}

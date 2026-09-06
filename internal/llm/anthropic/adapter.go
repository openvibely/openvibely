package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	llmattachment "github.com/openvibely/openvibely/internal/llm/attachment"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmoauth "github.com/openvibely/openvibely/internal/llm/oauth"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	llmstream "github.com/openvibely/openvibely/internal/llm/stream"
	llmusage "github.com/openvibely/openvibely/internal/llm/usage"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	mcpclient "github.com/openvibely/openvibely/pkg/mcp_client"
)

const claudeCodeMaxOutputTokensEnv = "CLAUDE_CODE_MAX_OUTPUT_TOKENS"

type claudeCodeOutputBudget struct {
	Default    int
	UpperLimit int
}

// applyAgentToSystemPrompt prepends the agent definition's system prompt and
// skill contents to the base system context string.
func claudeCodeOutputBudgetForModel(model string) claudeCodeOutputBudget {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(m, "claude-opus-5"), strings.Contains(m, "claude-sonnet-5"):
		return claudeCodeOutputBudget{Default: 64000, UpperLimit: 128000}
	case strings.Contains(m, "claude-fable-5"), strings.Contains(m, "claude-mythos-5"):
		return claudeCodeOutputBudget{Default: 64000, UpperLimit: 128000}
	case strings.Contains(m, "claude-opus-4-8"), strings.Contains(m, "claude-opus-4-7"), strings.Contains(m, "claude-opus-4-6"):
		return claudeCodeOutputBudget{Default: 64000, UpperLimit: 128000}
	case strings.Contains(m, "claude-sonnet-4-6"):
		return claudeCodeOutputBudget{Default: 32000, UpperLimit: 64000}
	case strings.Contains(m, "claude-opus-4-5"), strings.Contains(m, "claude-sonnet-4-5"),
		strings.Contains(m, "claude-sonnet-4-0"), strings.Contains(m, "claude-haiku-4-5"),
		strings.Contains(m, "claude-3-7-sonnet"):
		return claudeCodeOutputBudget{Default: 32000, UpperLimit: 64000}
	case strings.Contains(m, "claude-opus-4-1"), strings.Contains(m, "claude-opus-4-0"):
		return claudeCodeOutputBudget{Default: 32000, UpperLimit: 32000}
	case strings.Contains(m, "claude-3-5-sonnet"), strings.Contains(m, "claude-3-5-haiku"),
		strings.Contains(m, "claude-3-sonnet"):
		return claudeCodeOutputBudget{Default: 8192, UpperLimit: 8192}
	case strings.Contains(m, "claude-3-opus"), strings.Contains(m, "claude-3-haiku"):
		return claudeCodeOutputBudget{Default: 4096, UpperLimit: 4096}
	default:
		return claudeCodeOutputBudget{Default: 32000, UpperLimit: 128000}
	}
}

func claudeCodeMaxOutputTokens(model string) int {
	budget := claudeCodeOutputBudgetForModel(model)
	parsed, ok := parseClaudeCodeMaxOutputTokensEnv(os.Getenv(claudeCodeMaxOutputTokensEnv), budget.UpperLimit)
	if !ok || parsed <= 0 {
		return budget.Default
	}
	if parsed > budget.UpperLimit {
		return budget.UpperLimit
	}
	return parsed
}

func parseClaudeCodeMaxOutputTokensEnv(raw string, upperLimit int) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	end := 0
	if raw[0] == '+' || raw[0] == '-' {
		end = 1
	}
	for end < len(raw) && raw[end] >= '0' && raw[end] <= '9' {
		end++
	}
	if end == 0 || (end == 1 && (raw[0] == '+' || raw[0] == '-')) {
		return 0, false
	}
	parsed, err := strconv.ParseInt(raw[:end], 10, 64)
	if err != nil {
		if strings.HasPrefix(raw[:end], "-") {
			return math.MinInt, true
		}
		return upperLimit, true
	}
	if parsed > int64(math.MaxInt) {
		return upperLimit, true
	}
	if parsed < int64(math.MinInt) {
		return math.MinInt, true
	}
	return int(parsed), true
}

func applyAgentToSystemPrompt(base string, agent *models.Agent) string {
	if agent == nil {
		return base
	}
	var parts []string
	if agent.SystemPrompt != "" {
		parts = append(parts, agent.SystemPrompt)
	}
	for _, skill := range agent.Skills {
		if skill.Content != "" {
			parts = append(parts, fmt.Sprintf("## Skill: %s\n\n%s", skill.Name, skill.Content))
		}
	}
	if base != "" {
		parts = append(parts, base)
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// errMaxTokens is returned when the API response was truncated due to max_tokens.
var errMaxTokens = fmt.Errorf("response truncated: max_tokens limit reached (output budget exhausted before task completed)")

// errRefusal is returned when Anthropic returns HTTP 200 with stop_reason=refusal.
var errRefusal = fmt.Errorf("model refused the request: Anthropic returned stop_reason=refusal")

// Adapter encapsulates Anthropic provider logic.
type Adapter struct {
	llmConfigRepo *repository.LLMConfigRepo
	execRepo      *repository.ExecutionRepo
	streamHub     llmstream.ExecutionStreamPublisher
	oauthRecovery *llmoauth.Manager
}

func mapBuiltInToolName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read_file":
		return "Read"
	case "write_file":
		return "Write"
	case "edit_file":
		return "Edit"
	case "bash":
		return "Bash"
	case "list_files":
		return "Glob"
	case "grep_search":
		return "Grep"
	case "web_search", "web_search_20250305", "web_search_20260209":
		return "WebSearch"
	case "web_fetch", "web_fetch_20250910", "web_fetch_20260209", "web_fetch_20260309":
		return "WebFetch"
	default:
		return ""
	}
}

func agentSkipDefaultTools(agentDef *models.Agent) bool {
	return agentDef != nil && agentDef.ToolConfig.SkipDefaultTools
}

func agentAllowsBuiltInTool(agentDef *models.Agent, toolName string) bool {
	var configuredTools []string
	if agentDef != nil {
		configuredTools = agentDef.Tools
	}
	return llmcontracts.AllowsBuiltInTool(toolName, llmcontracts.BuiltInToolPolicyOptions{
		SkipDefaultTools: agentSkipDefaultTools(agentDef),
		ConfiguredTools:  configuredTools,
		MapToolName:      mapBuiltInToolName,
	})
}

func planModeAllowsReadOnlyTool(name string) bool {
	if llmcontracts.DefaultPlanModeAllowsReadOnlyTool(name) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "web_search_20250305", "web_search_20260209",
		"web_fetch", "web_fetch_20250910", "web_fetch_20260209", "web_fetch_20260309":
		return true
	default:
		return false
	}
}

func runtimeAnthropicTools(rt *llmcontracts.RuntimeTools) []anthropicclient.ToolDefinition {
	if rt == nil || len(rt.Definitions) == 0 {
		return nil
	}
	out := make([]anthropicclient.ToolDefinition, 0, len(rt.Definitions))
	for _, def := range rt.Definitions {
		name := strings.TrimSpace(def.Name)
		if name == "" {
			continue
		}
		out = append(out, anthropicclient.ToolDefinition{
			Name:        anthropicRuntimeToolWireName(name),
			Description: strings.TrimSpace(def.Description),
			InputSchema: rt.ProviderParameters(name),
		})
	}
	return out
}

func anthropicRuntimeToolWireName(name string) string {
	trimmed := strings.TrimSpace(name)
	if strings.EqualFold(trimmed, "skills_list") {
		return "skill_list"
	}
	return trimmed
}

func anthropicRuntimeToolCanonicalName(name string) string {
	if strings.EqualFold(strings.TrimSpace(name), "skill_list") {
		return "skills_list"
	}
	return name
}

func composeRuntimeToolExecutor(base func(context.Context, string, json.RawMessage) (string, bool, error), rt *llmcontracts.RuntimeTools) func(context.Context, string, json.RawMessage) (string, bool, error) {
	if rt == nil || rt.Executor == nil {
		return base
	}
	return func(ctx context.Context, name string, input json.RawMessage) (string, bool, error) {
		canonicalName := anthropicRuntimeToolCanonicalName(name)
		input = rt.NormalizeToolInput(canonicalName, input)
		if output, handled, isError, err := rt.Executor(ctx, canonicalName, input); handled || err != nil {
			return output, isError, err
		}
		if base == nil {
			return "", true, fmt.Errorf("tool %q is not available", canonicalName)
		}
		return base(ctx, canonicalName, input)
	}
}

func runtimeToolPolicyOptions(isTaskFollowup bool, chatMode models.ChatMode) llmcontracts.RuntimeToolPolicyOptions {
	return llmcontracts.RuntimeToolPolicyOptions{
		IsTaskFollowup:     isTaskFollowup,
		ChatMode:           chatMode,
		CanonicalName:      anthropicRuntimeToolCanonicalName,
		AllowsReadOnlyTool: planModeAllowsReadOnlyTool,
	}
}

func composeTaskRuntimeToolFilter(base func(string) bool, rt *llmcontracts.RuntimeTools) func(string) bool {
	return llmcontracts.ComposeRuntimeToolFilter(base, rt, runtimeToolPolicyOptions(true, models.ChatModeOrchestrate))
}

func appendToolModeSystemPrompt(base string, rt *llmcontracts.RuntimeTools, chatMode models.ChatMode) string {
	if chatMode != models.ChatModeOrchestrate {
		return base
	}
	return llmprompt.ApplyChatActionToolMode(base, rt.DefinitionNames())
}

func shouldSkipDefaultToolsForChatMode(isTaskFollowup bool, chatMode models.ChatMode, rt *llmcontracts.RuntimeTools) bool {
	// In orchestrate chat with runtime action tools, advertise only runtime tools.
	// Local coding tools are blocked by filter in this mode, so exposing them
	// causes pointless "tool not allowed" turns.
	return !isTaskFollowup && chatMode == models.ChatModeOrchestrate && rt != nil && len(rt.Definitions) > 0
}

func resolveChatToolPolicy(isTaskFollowup bool, chatMode models.ChatMode, rt *llmcontracts.RuntimeTools) (disableTools bool, skipDefaultTools bool) {
	skipDefaultTools = shouldSkipDefaultToolsForChatMode(isTaskFollowup, chatMode, rt)
	disableTools = !isTaskFollowup && chatMode != models.ChatModePlan && rt == nil
	return disableTools, skipDefaultTools
}

func buildAnthropicRuntime(ctx context.Context, workDir string, agentDef *models.Agent) ([]anthropicclient.ToolDefinition, func(context.Context, string, json.RawMessage) (string, bool, error), func(string) bool, func()) {
	cleanup := func() {}
	if agentDef == nil || len(agentDef.MCPServers) == 0 {
		execFn := func(ctx context.Context, name string, input json.RawMessage) (string, bool, error) {
			out, err := anthropicclient.ExecuteTool(ctx, workDir, name, input)
			return out, err != nil, err
		}
		filterFn := func(name string) bool {
			return agentAllowsBuiltInTool(agentDef, name)
		}
		return nil, execFn, filterFn, cleanup
	}

	manager, err := mcpclient.NewMCPManager(ctx, agentDef.MCPServers, workDir)
	if err != nil {
		applog.Infof("[anthropic] MCP manager init failed: %v", err)
		execFn := func(ctx context.Context, name string, input json.RawMessage) (string, bool, error) {
			out, err := anthropicclient.ExecuteTool(ctx, workDir, name, input)
			return out, err != nil, err
		}
		filterFn := func(name string) bool {
			return agentAllowsBuiltInTool(agentDef, name)
		}
		return nil, execFn, filterFn, cleanup
	}
	cleanup = func() { manager.Close() }

	execFn := func(ctx context.Context, name string, input json.RawMessage) (string, bool, error) {
		if manager.IsMCPTool(name) {
			var args map[string]interface{}
			if len(input) > 0 {
				_ = json.Unmarshal(input, &args)
			}
			out, isErr, err := manager.ExecuteTool(name, args)
			return out, isErr, err
		}
		out, err := anthropicclient.ExecuteTool(ctx, workDir, name, input)
		return out, err != nil, err
	}
	filterFn := func(name string) bool {
		if manager.IsMCPTool(name) {
			return true
		}
		return agentAllowsBuiltInTool(agentDef, name)
	}
	return manager.ToolDefinitions(), execFn, filterFn, cleanup
}

// New creates a new Anthropic adapter.
func New(llmConfigRepo *repository.LLMConfigRepo, execRepo *repository.ExecutionRepo, streamHub llmstream.ExecutionStreamPublisher) *Adapter {
	return &Adapter{
		llmConfigRepo: llmConfigRepo,
		execRepo:      execRepo,
		streamHub:     streamHub,
		oauthRecovery: llmoauth.NewManager(llmConfigRepo),
	}
}

// Call handles Anthropic LLM requests.
func (a *Adapter) Call(ctx context.Context, req llmcontracts.AgentRequest, workDir string, w *llmstream.Writer) (llmcontracts.AgentResult, error) {
	agent := req.Agent

	// API paths only (OAuth or API key).
	if !agent.IsOAuth() && !agent.IsAnthropicAPIKey() {
		return llmcontracts.AgentResult{}, fmt.Errorf("anthropic adapter requires OAuth or API key auth method")
	}

	// Apply agent definition: inject system prompt + skill content into chat context
	if req.AgentDefinition != nil {
		req.ChatSystemContext = applyAgentToSystemPrompt(req.ChatSystemContext, req.AgentDefinition)
		req.ProjectInstructions = applyAgentToSystemPrompt(req.ProjectInstructions, req.AgentDefinition)
		if req.AgentDefinition.Model != "" && req.AgentDefinition.Model != "inherit" {
			agent.Model = req.AgentDefinition.Model
			req.Agent = agent
		}
	}

	extraTools, toolExecutor, toolFilter, cleanupRuntime := buildAnthropicRuntime(ctx, workDir, req.AgentDefinition)
	defer cleanupRuntime()
	agentSkipDefaults := agentSkipDefaultTools(req.AgentDefinition)

	switch req.Operation {
	case llmcontracts.OperationTask:
		output, textOnly, usage, err := a.callStreaming(ctx, req.Message, req.Attachments, agent, req.ExecID, workDir, req.ProjectInstructions, extraTools, toolExecutor, toolFilter, agentSkipDefaults)
		return llmcontracts.AgentResult{
			Output:         output,
			TextOnlyOutput: textOnly,
			Usage:          usage,
			StopReason:     stopReasonIfMaxTokens(err),
		}, err

	case llmcontracts.OperationStreaming:
		if req.Followup || req.ChatHistory != nil || req.ChatMode == models.ChatModeOrchestrate || req.ChatMode == models.ChatModePlan {
			output, usage, err := a.callChatStreaming(ctx, req.Message, req.Attachments, agent, req.ExecID, req.ChatHistory, req.ChatSystemContext, req.Followup, req.ChatMode, workDir, extraTools, toolExecutor, toolFilter, agentSkipDefaults)
			return llmcontracts.AgentResult{
				Output:     output,
				Usage:      usage,
				StopReason: stopReasonIfMaxTokens(err),
			}, err
		}
		output, textOnly, usage, err := a.callStreaming(ctx, req.Message, req.Attachments, agent, req.ExecID, workDir, req.ProjectInstructions, extraTools, toolExecutor, toolFilter, agentSkipDefaults)
		return llmcontracts.AgentResult{
			Output:         output,
			TextOnlyOutput: textOnly,
			Usage:          usage,
			StopReason:     stopReasonIfMaxTokens(err),
		}, err

	case llmcontracts.OperationDirect:
		rt := llmcontracts.RuntimeToolsFromContext(ctx)
		extraTools = append(extraTools, runtimeAnthropicTools(rt)...)
		toolExecutor = composeRuntimeToolExecutor(toolExecutor, rt)
		toolFilter = llmcontracts.ComposeRuntimeToolFilter(toolFilter, rt, runtimeToolPolicyOptions(false, models.ChatModeOrchestrate))
		if rt != nil && len(rt.Definitions) > 0 {
			req.DisableTools = false
		}
		skipDefaultTools := agentSkipDefaultTools(req.AgentDefinition) || llmcontracts.RuntimeSkipDefaultTools(rt)
		output, usage, err := a.callDirect(ctx, req.Message, req.Attachments, agent, workDir, req.ProjectInstructions, extraTools, toolExecutor, toolFilter, req.DisableTools, skipDefaultTools, req.RawDirectPrompt, req.LifecycleHookCall)
		return llmcontracts.AgentResult{
			Output:     output,
			Usage:      usage,
			StopReason: stopReasonIfMaxTokens(err),
		}, err

	default:
		return llmcontracts.AgentResult{}, fmt.Errorf("unsupported operation: %s", req.Operation)
	}
}

// callDirect calls the Anthropic API using OAuth tokens.
func (a *Adapter) callDirect(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, workDir string, projectInstructions string, extraTools []anthropicclient.ToolDefinition, toolExecutor func(context.Context, string, json.RawMessage) (string, bool, error), toolFilter func(string) bool, disableTools bool, skipDefaultTools bool, rawDirectPrompt bool, lifecycleHookCall bool) (string, llmcontracts.Usage, error) {
	maxTokens := claudeCodeMaxOutputTokens(agent.Model)
	applog.Infof("[anthropic] callDirect model=%s max_tokens=%d workDir=%s attachments=%d disable_tools=%v", agent.Model, maxTokens, workDir, len(attachments), disableTools)

	client, err := a.getClient(ctx, agent)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}

	mcAttachments, err := convertAttachments(attachments)
	if err != nil {
		return "", llmusage.FromTotal(0), fmt.Errorf("convert attachments: %w", err)
	}

	fullPrompt := prompt
	systemPrompt := ""
	webSearchEnabled := false
	switch {
	case rawDirectPrompt:
		// Message is already fully composed; send it untouched.
	case lifecycleHookCall:
		// Lifecycle hooks return structured JSON; they do not edit files, run
		// builds, or browse. Keep the hook agent's own prompt so its identity
		// and rules survive, but drop the shared coding-agent system prompt,
		// the take-direct-action header, and provider web tools.
		systemPrompt = projectInstructions
	default:
		fullPrompt = llmprompt.BuildTaskPromptHeader() + prompt
		systemPrompt = llmprompt.BuildAgentSystemPrompt(projectInstructions, workDir)
		webSearchEnabled = true
	}
	opts := &anthropicclient.AgenticOptions{
		Model:                  agent.Model,
		MaxTokens:              maxTokens,
		Effort:                 agent.ReasoningEffort,
		System:                 systemPrompt,
		WorkDir:                workDir,
		Attachments:            mcAttachments,
		DisableTools:           disableTools,
		SkipDefaultTools:       skipDefaultTools,
		AutoCompaction:         true,
		WebSearchEnabled:       webSearchEnabled,
		ExtraTools:             extraTools,
		ToolExecutor:           toolExecutor,
		ToolFilter:             toolFilter,
		OnToolBoundarySteering: llmcontracts.SteeringCallbackFromContext(ctx),
	}

	resp, err := client.SendAgentic(ctx, fullPrompt, opts)
	if err != nil {
		applog.Infof("[anthropic] callDirect error: %v", err)
		return "", llmusage.FromTotal(0), fmt.Errorf("anthropicclient agentic call: %w", err)
	}

	usage := llmusage.FromAnthropic(resp.InputTokens, resp.OutputTokens, resp.CacheCreationInputTokens, resp.CacheReadInputTokens)
	applog.Infof("[anthropic] callDirect success model=%s input=%d output=%d tools=%d stop=%s compacted=%v", resp.Model, resp.InputTokens, resp.OutputTokens, len(resp.ToolCalls), resp.StopReason, resp.Compacted)
	if resp.StopReason == "max_tokens" {
		return resp.Text, usage, errMaxTokens
	}
	if resp.StopReason == "refusal" {
		return resp.Text, usage, errRefusal
	}
	return resp.Text, usage, nil
}

// callChatStreaming calls the Anthropic API with streaming for chat/followup.
func (a *Adapter) callChatStreaming(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, isTaskFollowup bool, chatMode models.ChatMode, workDir string, extraTools []anthropicclient.ToolDefinition, toolExecutor func(context.Context, string, json.RawMessage) (string, bool, error), toolFilter func(string) bool, agentSkipDefaults bool) (string, llmcontracts.Usage, error) {
	maxTokens := claudeCodeMaxOutputTokens(agent.Model)
	applog.Infof("[anthropic] callChatStreaming model=%s max_tokens=%d history=%d exec=%s isTaskFollowup=%v workDir=%s attachments=%d", agent.Model, maxTokens, len(chatHistory), execID, isTaskFollowup, workDir, len(attachments))

	client, err := a.getClient(ctx, agent)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}

	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	systemPromptStr := llmprompt.BuildChatSystemPrompt(isTaskFollowup, chatMode, chatSystemContext, false)
	systemPromptStr = llmprompt.AppendWorktreeContextPrompt(systemPromptStr, workDir)
	systemPromptStr = appendToolModeSystemPrompt(systemPromptStr, rt, chatMode)
	client.History = append(client.History, buildClientHistory(chatHistory)...)

	mcAttachments, err := convertAttachments(attachments)
	if err != nil {
		return "", llmusage.FromTotal(0), fmt.Errorf("convert attachments: %w", err)
	}

	sw := llmstream.NewWriterWithPublisher(execID, "", a.execRepo, ctx, 500*time.Millisecond, a.streamHub)
	defer sw.Stop()

	extraTools = append(extraTools, runtimeAnthropicTools(rt)...)
	toolExecutor = composeRuntimeToolExecutor(toolExecutor, rt)
	toolFilter = llmcontracts.ComposeRuntimeToolFilter(toolFilter, rt, runtimeToolPolicyOptions(isTaskFollowup, chatMode))
	disableTools, skipDefaultTools := resolveChatToolPolicy(isTaskFollowup, chatMode, rt)
	skipDefaultTools = skipDefaultTools || agentSkipDefaults || llmcontracts.RuntimeSkipDefaultTools(rt)
	chatInThinking := false
	opts := &anthropicclient.AgenticOptions{
		Model:                  agent.Model,
		MaxTokens:              maxTokens,
		Effort:                 agent.ReasoningEffort,
		EnableThinking:         true,
		DisableTools:           disableTools,
		SkipDefaultTools:       skipDefaultTools,
		System:                 systemPromptStr,
		WorkDir:                workDir,
		Attachments:            mcAttachments,
		AutoCompaction:         true,
		WebSearchEnabled:       true,
		ExtraTools:             extraTools,
		ToolExecutor:           toolExecutor,
		ToolFilter:             toolFilter,
		OnToolBoundarySteering: llmcontracts.SteeringCallbackFromContext(ctx),
		OnThinking: func(text string) {
			if !chatInThinking {
				chatInThinking = true
				llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingOpen}, false)
			}
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingText, Text: text}, false)
		},
		OnText: func(text string) {
			if chatInThinking {
				chatInThinking = false
				llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingEnd}, false)
			}
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventTextDelta, Text: text}, false)
		},
		OnToolUse: func(name string, input json.RawMessage) {
			if chatInThinking {
				chatInThinking = false
				llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingEnd}, false)
			}
			secondary := toolSecondaryInfo(name, input)
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventToolUse, ToolName: name, Secondary: secondary}, false)
		},
		OnToolResult: func(name string, output string, isError bool) {
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventToolResult, ToolName: name, Output: output, IsError: isError}, false)
		},
		OnCompaction: func(summary string) {
			applog.Infof("[anthropic] callChatStreaming context compacted, summary_len=%d", len(summary))
		},
	}

	resp, err := client.SendAgentic(ctx, message, opts)
	if err != nil {
		sw.Flush()
		applog.Infof("[anthropic] callChatStreaming error: %v", err)
		return "", llmusage.FromTotal(0), fmt.Errorf("anthropicclient agentic chat streaming call: %w", err)
	}

	sw.Flush()

	output := sw.String()
	usage := llmusage.FromAnthropic(resp.InputTokens, resp.OutputTokens, resp.CacheCreationInputTokens, resp.CacheReadInputTokens)
	applog.Infof("[anthropic] callChatStreaming success output_len=%d tokens=%d tools=%d stop=%s compacted=%v", len(output), usage.TotalTokens, len(resp.ToolCalls), resp.StopReason, resp.Compacted)
	if resp.StopReason == "max_tokens" {
		return output, usage, errMaxTokens
	}
	if resp.StopReason == "refusal" {
		return output, usage, errRefusal
	}
	return output, usage, nil
}

// callStreaming calls the Anthropic API with streaming.
func (a *Adapter) callStreaming(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string, projectInstructions string, extraTools []anthropicclient.ToolDefinition, toolExecutor func(context.Context, string, json.RawMessage) (string, bool, error), toolFilter func(string) bool, agentSkipDefaults bool) (string, string, llmcontracts.Usage, error) {
	maxTokens := claudeCodeMaxOutputTokens(agent.Model)
	applog.Infof("[anthropic] callStreaming model=%s max_tokens=%d exec=%s workDir=%s attachments=%d", agent.Model, maxTokens, execID, workDir, len(attachments))

	client, err := a.getClient(ctx, agent)
	if err != nil {
		return "", "", llmusage.FromTotal(0), err
	}

	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	fullPrompt := llmprompt.BuildTaskPromptHeader() + prompt
	fullPrompt = llmprompt.ApplyTaskCreationToolMode(fullPrompt, rt.DefinitionNames())
	mcAttachments, err := convertAttachments(attachments)
	if err != nil {
		return "", "", llmusage.FromTotal(0), fmt.Errorf("convert attachments: %w", err)
	}

	sw := llmstream.NewWriterWithPublisher(execID, "", a.execRepo, ctx, 500*time.Millisecond, a.streamHub)
	defer sw.Stop()

	extraTools = append(extraTools, runtimeAnthropicTools(rt)...)
	toolExecutor = composeRuntimeToolExecutor(toolExecutor, rt)
	toolFilter = composeTaskRuntimeToolFilter(toolFilter, rt)
	skipDefaultTools := agentSkipDefaults || llmcontracts.RuntimeSkipDefaultTools(rt)

	inThinking := false
	opts := &anthropicclient.AgenticOptions{
		Model:                  agent.Model,
		MaxTokens:              maxTokens,
		Effort:                 agent.ReasoningEffort,
		EnableThinking:         true,
		SkipDefaultTools:       skipDefaultTools,
		System:                 llmprompt.BuildAgentSystemPrompt(projectInstructions, workDir),
		WorkDir:                workDir,
		Attachments:            mcAttachments,
		AutoCompaction:         true,
		WebSearchEnabled:       true,
		ExtraTools:             extraTools,
		ToolExecutor:           toolExecutor,
		ToolFilter:             toolFilter,
		OnToolBoundarySteering: llmcontracts.SteeringCallbackFromContext(ctx),
		OnThinking: func(text string) {
			if !inThinking {
				inThinking = true
				llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingOpen}, false)
			}
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingText, Text: text}, false)
		},
		OnText: func(text string) {
			if inThinking {
				inThinking = false
				llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingEnd}, false)
			}
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventTextDelta, Text: text}, false)
		},
		OnToolUse: func(name string, input json.RawMessage) {
			if inThinking {
				inThinking = false
				llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingEnd}, false)
			}
			secondary := toolSecondaryInfo(name, input)
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventToolUse, ToolName: name, Secondary: secondary}, false)
		},
		OnToolResult: func(name string, output string, isError bool) {
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventToolResult, ToolName: name, Output: output, IsError: isError}, false)
		},
		OnCompaction: func(summary string) {
			applog.Infof("[anthropic] callStreaming context compacted, summary_len=%d", len(summary))
		},
	}

	resp, err := client.SendAgentic(ctx, fullPrompt, opts)
	if err != nil {
		sw.Flush()
		applog.Infof("[anthropic] callStreaming error: %v", err)
		return "", "", llmusage.FromTotal(0), fmt.Errorf("anthropicclient agentic streaming call: %w", err)
	}

	sw.Flush()

	output := sw.String()
	textOnly := sw.TextString()
	usage := llmusage.FromAnthropic(resp.InputTokens, resp.OutputTokens, resp.CacheCreationInputTokens, resp.CacheReadInputTokens)
	applog.Infof("[anthropic] callStreaming success output_len=%d tokens=%d tools=%d stop=%s compacted=%v", len(output), usage.TotalTokens, len(resp.ToolCalls), resp.StopReason, resp.Compacted)
	if resp.StopReason == "max_tokens" {
		return output, textOnly, usage, errMaxTokens
	}
	if resp.StopReason == "refusal" {
		return output, textOnly, usage, errRefusal
	}
	return output, textOnly, usage, nil
}

func (a *Adapter) anthropicRefreshFunc() llmoauth.RefreshFunc {
	return func(ctx context.Context, cfg models.LLMConfig) (llmoauth.TokenSet, error) {
		auth, err := anthropicclient.RefreshToken(cfg.OAuthRefreshToken)
		if err != nil {
			return llmoauth.TokenSet{}, err
		}
		return llmoauth.TokenSet{AccessToken: auth.Token, RefreshToken: auth.RefreshToken, ExpiresAt: auth.ExpiresAt}, nil
	}
}

func (a *Adapter) ensureFreshOAuth(ctx context.Context, agent models.LLMConfig) (models.LLMConfig, error) {
	return a.oauthRecovery.EnsureFresh(ctx, agent, time.Hour, a.anthropicRefreshFunc())
}

func (a *Adapter) recoverUnauthorized(ctx context.Context, agent models.LLMConfig, tokenUsed string) (models.LLMConfig, bool, error) {
	return a.oauthRecovery.RecoverUnauthorized(ctx, agent, tokenUsed, a.anthropicRefreshFunc())
}

func (a *Adapter) newOAuthClient(ctx context.Context, agent models.LLMConfig) *anthropicclient.Client {
	client := anthropicclient.NewWithOAuthToken(agent.OAuthAccessToken, agent.OAuthRefreshToken, agent.OAuthExpiresAt)
	client.SetOAuthUnauthorizedHandler(func(ctx context.Context, tokenUsed string) (anthropicclient.OAuthTokens, bool, error) {
		fresh, recovered, err := a.recoverUnauthorized(ctx, agent, tokenUsed)
		if err != nil || !recovered {
			return anthropicclient.OAuthTokens{}, recovered, err
		}
		return anthropicclient.OAuthTokens{AccessToken: fresh.OAuthAccessToken, RefreshToken: fresh.OAuthRefreshToken, ExpiresAt: fresh.OAuthExpiresAt}, true, nil
	})
	return client
}

// getClient creates an anthropicclient.Client from API key or OAuth tokens.
func (a *Adapter) getClient(ctx context.Context, agent models.LLMConfig) (*anthropicclient.Client, error) {
	if agent.IsAnthropicAPIKey() {
		if strings.TrimSpace(agent.APIKey) == "" {
			return nil, fmt.Errorf("Anthropic API key not configured for model %q", agent.Name)
		}
		return anthropicclient.NewWithAPIKey(agent.APIKey), nil
	}

	if agent.OAuthAccessToken == "" {
		return nil, fmt.Errorf("OAuth not configured for model %q - click 'Connect with OAuth' on the Models page", agent.Name)
	}

	agent, err := a.ensureFreshOAuth(ctx, agent)
	if err != nil {
		applog.Infof("[anthropic] getClient token refresh failed for agent=%s: %v", agent.Name, err)
		return nil, err
	}

	return a.newOAuthClient(ctx, agent), nil
}

// buildClientHistory converts chat execution history to anthropicclient.Message slices.
func buildClientHistory(chatHistory []models.Execution) []anthropicclient.Message {
	history := llmprompt.LimitChatHistory(chatHistory)
	var messages []anthropicclient.Message
	for _, exec := range history {
		if exec.PromptSent != "" {
			messages = appendMergedMessage(messages, "user", exec.PromptSent)
		}
		if replay := llmprompt.ReplayAssistantContent(exec); replay != "" {
			messages = appendMergedMessage(messages, "assistant", replay)
		}
	}
	if len(messages) > 0 && messages[len(messages)-1].Role == "user" {
		messages = messages[:len(messages)-1]
	}
	return messages
}

// appendMergedMessage appends a message, merging with the previous if same role.
func appendMergedMessage(messages []anthropicclient.Message, role, content string) []anthropicclient.Message {
	if len(messages) > 0 && messages[len(messages)-1].Role == role {
		messages[len(messages)-1].Content += "\n\n" + content
		return messages
	}
	return append(messages, anthropicclient.Message{Role: role, Content: content})
}

// convertAttachments converts internal models.Attachment to anthropicclient.FileAttachment format.
func convertAttachments(attachments []models.Attachment) ([]*anthropicclient.FileAttachment, error) {
	if len(attachments) == 0 {
		return nil, nil
	}

	prepared, err := llmattachment.Preprocess(attachments)
	if err != nil {
		return nil, fmt.Errorf("preprocess attachments: %w", err)
	}

	result := make([]*anthropicclient.FileAttachment, 0, len(prepared))
	for _, att := range prepared {
		mcAtt, err := anthropicclient.NewFileAttachment(att.FilePath)
		if err != nil {
			applog.Infof("[anthropic] convertAttachments error loading %s: %v", att.FilePath, err)
			return nil, fmt.Errorf("load attachment %s: %w", att.FileName, err)
		}
		result = append(result, mcAtt)
	}
	return result, nil
}

// stopReasonIfMaxTokens returns "max_tokens" if err is errMaxTokens, else empty string.
func stopReasonIfMaxTokens(err error) string {
	if err == errMaxTokens {
		return "max_tokens"
	}
	return ""
}

// toolSecondaryInfo extracts a short secondary label from tool input.
func toolSecondaryInfo(name string, input json.RawMessage) string {
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	switch name {
	case "read_file", "write_file", "edit_file", "Read", "Write", "Edit":
		if fp, ok := m["file_path"].(string); ok {
			parts := splitPath(fp)
			return parts[len(parts)-1]
		}
	case "bash", "Bash":
		if cmd, ok := m["command"].(string); ok {
			cmd = truncateToolSecondary(cmd, 320)
			return "$ " + cmd
		}
	case "grep_search", "Grep":
		if p, ok := m["pattern"].(string); ok {
			return truncateToolSecondary(p, 140)
		}
	case "list_files", "Glob":
		if p, ok := m["path"].(string); ok {
			return p
		}
		if p, ok := m["pattern"].(string); ok {
			return p
		}
	case "web_search", "web_search_20250305", "web_search_20260209", "WebSearch":
		if q, ok := m["query"].(string); ok {
			return truncateToolSecondary(q, 140)
		}
	case "web_fetch", "web_fetch_20250910", "web_fetch_20260209", "web_fetch_20260309", "WebFetch":
		if u, ok := m["url"].(string); ok {
			return truncateToolSecondary(u, 140)
		}
	}
	return ""
}

func truncateToolSecondary(value string, max int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max] + "…"
}

func splitPath(p string) []string {
	var parts []string
	for _, s := range []byte(p) {
		if s == '/' {
			parts = append(parts, "")
		} else if len(parts) == 0 {
			parts = append(parts, string(s))
		} else {
			parts[len(parts)-1] += string(s)
		}
	}
	if len(parts) == 0 {
		return []string{p}
	}
	return parts
}

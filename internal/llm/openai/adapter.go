package openai

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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
	mcpclient "github.com/openvibely/openvibely/pkg/mcp_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

// errMaxTokens is returned when the API response was truncated due to max output tokens.
var errMaxTokens = fmt.Errorf("response truncated: max output tokens limit reached (output budget exhausted before task completed)")

const (
	openAIDirectOutputBudget  = 4096
	openAIAgenticOutputBudget = 16384
	responsesTransportTTL     = 30 * time.Minute
	responsesTransportMax     = 64
)

type responsesTransportEntry struct {
	state    *openaiclient.ResponsesTransportState
	lastUsed time.Time
	leases   int
}

// isMaxTokensStopReason returns true if the stop reason indicates the response
// was truncated due to hitting the output token limit.
func isMaxTokensStopReason(reason string) bool {
	// Responses API: "max_output_tokens" (via incomplete_details.reason)
	// Completions API: "length" (via finish_reason)
	return reason == "max_output_tokens" || reason == "length"
}

// Adapter handles all OpenAI-specific provider logic.
type Adapter struct {
	llmConfigRepo   *repository.LLMConfigRepo
	execRepo        *repository.ExecutionRepo
	streamHub       llmstream.ExecutionStreamPublisher
	oauthRecovery   *llmoauth.Manager
	transportMu     sync.Mutex
	transportStates map[string]*responsesTransportEntry
}

func applyOpenAIOAuthSystemPrompt(base string, agent models.LLMConfig) string {
	if !agent.IsOpenAIOAuth() {
		return base
	}
	return llmprompt.BuildOpenAIOAuthSystemPrompt(base)
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
	case "web_search", "web_search_preview":
		return "WebSearch"
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

func runtimeOpenAITools(rt *llmcontracts.RuntimeTools) []openaiclient.ToolDefinition {
	if rt == nil || len(rt.Definitions) == 0 {
		return nil
	}
	out := make([]openaiclient.ToolDefinition, 0, len(rt.Definitions))
	for _, def := range rt.Definitions {
		name := strings.TrimSpace(def.Name)
		if name == "" {
			continue
		}
		out = append(out, openaiclient.ToolDefinition{
			Type:        "function",
			Name:        name,
			Description: strings.TrimSpace(def.Description),
			Parameters:  def.Parameters,
		})
	}
	return out
}

func composeRuntimeToolExecutor(base func(context.Context, string, json.RawMessage) (string, bool, error), rt *llmcontracts.RuntimeTools) func(context.Context, string, json.RawMessage) (string, bool, error) {
	if rt == nil || rt.Executor == nil {
		return base
	}
	return func(ctx context.Context, name string, input json.RawMessage) (string, bool, error) {
		if output, handled, isError, err := rt.Executor(ctx, name, input); handled || err != nil {
			return output, isError, err
		}
		if base == nil {
			return "", true, fmt.Errorf("tool %q is not available", name)
		}
		return base(ctx, name, input)
	}
}

func runtimeToolPolicyOptions(isTaskFollowup bool, chatMode models.ChatMode) llmcontracts.RuntimeToolPolicyOptions {
	return llmcontracts.RuntimeToolPolicyOptions{
		IsTaskFollowup:     isTaskFollowup,
		ChatMode:           chatMode,
		AllowsReadOnlyTool: llmcontracts.DefaultPlanModeAllowsReadOnlyTool,
	}
}

func appendToolModeSystemPrompt(base string, rt *llmcontracts.RuntimeTools, chatMode models.ChatMode) string {
	if chatMode != models.ChatModeOrchestrate {
		return base
	}
	return llmprompt.ApplyChatActionToolMode(base, rt.DefinitionNames())
}

func buildOpenAIRuntime(ctx context.Context, workDir string, agentDef *models.Agent) ([]openaiclient.ToolDefinition, func(context.Context, string, json.RawMessage) (string, bool, error), func(string) bool, func()) {
	cleanup := func() {}
	if agentDef == nil || len(agentDef.MCPServers) == 0 {
		execFn := func(ctx context.Context, name string, input json.RawMessage) (string, bool, error) {
			out, err := openaiclient.ExecuteTool(ctx, workDir, name, input)
			return out, err != nil, err
		}
		filterFn := func(name string) bool {
			return agentAllowsBuiltInTool(agentDef, name)
		}
		return nil, execFn, filterFn, cleanup
	}

	manager, err := mcpclient.NewMCPManager(ctx, agentDef.MCPServers, workDir)
	if err != nil {
		applog.Infof("[openai-adapter] MCP manager init failed: %v", err)
		execFn := func(ctx context.Context, name string, input json.RawMessage) (string, bool, error) {
			out, err := openaiclient.ExecuteTool(ctx, workDir, name, input)
			return out, err != nil, err
		}
		filterFn := func(name string) bool {
			return agentAllowsBuiltInTool(agentDef, name)
		}
		return nil, execFn, filterFn, cleanup
	}
	cleanup = func() { manager.Close() }

	mcpDefs := manager.ToolDefinitions()
	extra := make([]openaiclient.ToolDefinition, 0, len(mcpDefs))
	for _, d := range mcpDefs {
		extra = append(extra, openaiclient.ToolDefinition{
			Type:        "function",
			Name:        d.Name,
			Description: d.Description,
			Parameters:  d.InputSchema,
		})
	}

	execFn := func(ctx context.Context, name string, input json.RawMessage) (string, bool, error) {
		if manager.IsMCPTool(name) {
			var args map[string]interface{}
			if len(input) > 0 {
				_ = json.Unmarshal(input, &args)
			}
			out, isErr, err := manager.ExecuteTool(name, args)
			return out, isErr, err
		}
		out, err := openaiclient.ExecuteTool(ctx, workDir, name, input)
		return out, err != nil, err
	}
	filterFn := func(name string) bool {
		if manager.IsMCPTool(name) {
			return true
		}
		return agentAllowsBuiltInTool(agentDef, name)
	}
	return extra, execFn, filterFn, cleanup
}

// New creates a new OpenAI adapter.
func New(llmConfigRepo *repository.LLMConfigRepo, execRepo *repository.ExecutionRepo, streamHub llmstream.ExecutionStreamPublisher) *Adapter {
	return &Adapter{
		llmConfigRepo:   llmConfigRepo,
		execRepo:        execRepo,
		streamHub:       streamHub,
		oauthRecovery:   llmoauth.NewManager(llmConfigRepo),
		transportStates: make(map[string]*responsesTransportEntry),
	}
}

// CallDirect makes a non-streaming OpenAI API call.
// CallDirect issues a non-streaming direct request. projectInstructions carries
// the calling agent's own system prompt (the provider wrapper folds the agent
// definition into it); lifecycleHookCall marks a request made on behalf of a
// lifecycle hook, which keeps that agent prompt but drops the shared
// coding-agent framing it has no use for.
func (a *Adapter) CallDirect(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, workDir string, projectInstructions string, disableTools bool, rawDirectPrompt bool, lifecycleHookCall bool) (string, llmcontracts.Usage, error) {
	applog.Infof("[openai-adapter] CallDirect model=%s output_budget=%d attachments=%d auth_method=%s disable_tools=%v lifecycle_hook=%v", agent.Model, openAIDirectOutputBudget, len(attachments), agent.AuthMethod, disableTools, lifecycleHookCall)

	client, releaseTransport, err := a.getClient(ctx, agent, "")
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}
	defer releaseTransport()

	fullPrompt := prompt
	if len(attachments) > 0 {
		fullPrompt += "\n\nAttached files:\n"
		for _, att := range attachments {
			fullPrompt += fmt.Sprintf("- %s (absolute path: %s)\n", att.FileName, llmprompt.AttachmentAbsPath(att))
		}
	}

	oaAttachments, err := convertAttachments(attachments)
	if err != nil {
		return "", llmusage.FromTotal(0), fmt.Errorf("convert attachments: %w", err)
	}

	effectiveWorkDir := workDir
	if effectiveWorkDir == "" {
		effectiveWorkDir = "."
	}
	// A lifecycle hook keeps its agent's own prompt but skips the shared
	// coding-agent system prompt; every other direct call gets both.
	systemPrompt := ""
	if rawDirectPrompt {
		// The caller supplied a complete utility prompt. Do not add coding-agent
		// or interactive commentary instructions to structured generation calls.
	} else if lifecycleHookCall {
		systemPrompt = projectInstructions
	} else {
		systemPrompt = llmprompt.BuildAgentSystemPrompt(projectInstructions, effectiveWorkDir)
	}
	if !rawDirectPrompt {
		systemPrompt = applyOpenAIOAuthSystemPrompt(systemPrompt, agent)
	}

	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	if rt != nil && len(rt.Definitions) > 0 && !disableTools {
		resp, err := client.SendAgentic(ctx, fullPrompt, &openaiclient.AgenticOptions{
			Model:                  agent.Model,
			MaxOutputTokens:        openAIDirectOutputBudget,
			System:                 systemPrompt,
			ReasoningEffort:        reasoningEffort(agent.Model, agent.ReasoningEffort),
			ReasoningSummary:       "auto",
			WorkDir:                effectiveWorkDir,
			Attachments:            oaAttachments,
			ExtraTools:             runtimeOpenAITools(rt),
			ToolExecutor:           composeRuntimeToolExecutor(nil, rt),
			ToolFilter:             llmcontracts.ComposeRuntimeToolFilter(nil, rt, runtimeToolPolicyOptions(true, models.ChatModeOrchestrate)),
			OnToolBoundarySteering: llmcontracts.SteeringCallbackFromContext(ctx),
			SkipDefaultTools:       rt.SkipDefaultTools,
		})
		if err != nil {
			applog.Infof("[openai-adapter] CallDirect agentic error: %v", err)
			return "", llmusage.FromTotal(0), wrapAuthScopeError(agent, err)
		}
		usage := llmusage.FromOpenAI(resp.InputTokens, resp.OutputTokens, resp.CachedInputTokens, resp.ReasoningTokens)
		return resp.Text, usage, nil
	}

	resp, err := client.Send(ctx, fullPrompt, &openaiclient.SendOptions{
		Model:               agent.Model,
		MaxOutputTokens:     openAIDirectOutputBudget,
		System:              systemPrompt,
		ReasoningEffort:     reasoningEffort(agent.Model, agent.ReasoningEffort),
		DisableTools:        disableTools,
		SuppressToolMarkers: disableTools,
		Attachments:         oaAttachments,
	})
	if err != nil {
		applog.Infof("[openai-adapter] CallDirect error: %v", err)
		return "", llmusage.FromTotal(0), wrapAuthScopeError(agent, err)
	}

	usage := llmusage.FromOpenAI(resp.InputTokens, resp.OutputTokens, resp.CachedInputTokens, resp.ReasoningTokens)
	return resp.Text, usage, nil
}

// CallStreaming makes a streaming OpenAI API call with tool use.
func (a *Adapter) CallStreaming(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string, projectInstructions string, agentDef *models.Agent) (string, string, llmcontracts.Usage, error) {
	applog.Infof("[openai-adapter] CallStreaming model=%s output_budget=%d attachments=%d exec=%s auth_method=%s workDir=%s", agent.Model, openAIAgenticOutputBudget, len(attachments), execID, agent.AuthMethod, workDir)

	client, releaseTransport, err := a.getClient(ctx, agent, a.taskTransportScope(ctx, execID))
	if err != nil {
		return "", "", llmusage.FromTotal(0), err
	}
	defer releaseTransport()

	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	fullPrompt := llmprompt.BuildTaskPromptHeader() +
		llmprompt.BuildAttachmentInstructions(attachments) +
		prompt
	fullPrompt = llmprompt.ApplyTaskCreationToolMode(fullPrompt, rt.DefinitionNames())
	fullPrompt += "\n\n---\nRESPONSE FORMAT REQUIREMENT: You MUST end your final response with exactly one of these status lines:\n" +
		"- If the task completed successfully: [STATUS: SUCCESS]\n" +
		"- If a command failed, a script returned non-zero, or the task could not be completed: [STATUS: FAILED | <describe what went wrong>]\n" +
		"- If the task completed but something needs human attention: [STATUS: NEEDS_FOLLOWUP | <describe what needs attention>]\n" +
		"Example: [STATUS: FAILED | fail.sh returned exit code 1]\n" +
		"Example: [STATUS: NEEDS_FOLLOWUP | tests pass but 3 warnings need review]\n" +
		"Replace <describe what went wrong> or <describe what needs attention> with your actual description.\n" +
		"This status line is MANDATORY. Always include it as the very last line of your response."

	oaAttachments, err := convertAttachments(attachments)
	if err != nil {
		return "", "", llmusage.FromTotal(0), fmt.Errorf("convert attachments: %w", err)
	}

	effectiveWorkDir := workDir
	if effectiveWorkDir == "" {
		effectiveWorkDir = "."
	}
	extraTools, toolExecutor, toolFilter, cleanupRuntime := buildOpenAIRuntime(ctx, effectiveWorkDir, agentDef)
	defer cleanupRuntime()
	extraTools = append(extraTools, runtimeOpenAITools(rt)...)
	toolExecutor = composeRuntimeToolExecutor(toolExecutor, rt)
	toolFilter = llmcontracts.ComposeRuntimeToolFilter(toolFilter, rt, runtimeToolPolicyOptions(true, models.ChatModeOrchestrate))

	sw := llmstream.NewWriterWithPublisher(execID, "", a.execRepo, ctx, 500*time.Millisecond, a.streamHub)
	defer sw.Stop()
	inThinking := false

	skipDefaultTools := agentSkipDefaultTools(agentDef) || llmcontracts.RuntimeSkipDefaultTools(rt)
	resp, err := client.SendAgentic(ctx, fullPrompt, &openaiclient.AgenticOptions{
		Model:                  agent.Model,
		MaxOutputTokens:        openAIAgenticOutputBudget,
		System:                 applyOpenAIOAuthSystemPrompt(llmprompt.BuildAgentSystemPrompt(projectInstructions, effectiveWorkDir), agent),
		ReasoningEffort:        reasoningEffort(agent.Model, agent.ReasoningEffort),
		ReasoningSummary:       "auto",
		AutoCompaction:         true,
		WebSearchEnabled:       true,
		WorkDir:                effectiveWorkDir,
		Attachments:            oaAttachments,
		ExtraTools:             extraTools,
		ToolExecutor:           toolExecutor,
		ToolFilter:             toolFilter,
		SkipDefaultTools:       skipDefaultTools,
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
			if isStreamingMarkerChunk(text) {
				llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventRawOutput, Text: text}, false)
				return
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
			applog.Infof("[openai-adapter] CallStreaming context compacted, summary_len=%d", len(summary))
		},
	})
	if err != nil {
		sw.Flush()
		applog.Infof("[openai-adapter] CallStreaming error: %v", err)
		return "", "", llmusage.FromTotal(0), wrapAuthScopeError(agent, err)
	}
	if inThinking {
		inThinking = false
		llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingEnd}, false)
	}

	sw.Flush()

	output := sw.String()
	textOnly := sw.TextString()
	usage := llmusage.FromOpenAI(resp.InputTokens, resp.OutputTokens, resp.CachedInputTokens, resp.ReasoningTokens)
	applog.Infof("[openai-adapter] CallStreaming success output_len=%d tokens=%d tools=%d stop=%s compacted=%v", len(output), usage.TotalTokens, len(resp.ToolCalls), resp.StopReason, resp.Compacted)
	if isMaxTokensStopReason(resp.StopReason) {
		return output, textOnly, usage, errMaxTokens
	}
	return output, textOnly, usage, nil
}

// CallChatStreaming makes a streaming OpenAI chat call with history.
func (a *Adapter) CallChatStreaming(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID, transportScope string, chatHistory []models.Execution, chatSystemContext string, isTaskFollowup bool, chatMode models.ChatMode, workDir string, agentDef *models.Agent) (string, llmcontracts.Usage, error) {
	applog.Infof("[openai-adapter] CallChatStreaming model=%s history=%d message_len=%d context_len=%d attachments=%d exec=%s isTaskFollowup=%v auth_method=%s workDir=%s",
		agent.Model, len(chatHistory), len(message), len(chatSystemContext), len(attachments), execID, isTaskFollowup, agent.AuthMethod, workDir)

	transportScope = strings.TrimSpace(transportScope)
	client, releaseTransport, err := a.getClient(ctx, agent, transportScope)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}
	defer releaseTransport()

	client.History = append(client.History, buildClientHistory(chatHistory)...)
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	systemPromptStr := llmprompt.BuildChatSystemPrompt(isTaskFollowup, chatMode, chatSystemContext, false)
	systemPromptStr = llmprompt.AppendWorktreeContextPrompt(systemPromptStr, workDir)
	systemPromptStr = appendToolModeSystemPrompt(systemPromptStr, rt, chatMode)
	systemPromptStr = applyOpenAIOAuthSystemPrompt(systemPromptStr, agent)

	oaAttachments, err := convertAttachments(attachments)
	if err != nil {
		return "", llmusage.FromTotal(0), fmt.Errorf("convert attachments: %w", err)
	}

	effectiveWorkDir := workDir
	if effectiveWorkDir == "" {
		effectiveWorkDir = "."
	}
	extraTools, toolExecutor, toolFilter, cleanupRuntime := buildOpenAIRuntime(ctx, effectiveWorkDir, agentDef)
	defer cleanupRuntime()
	extraTools = append(extraTools, runtimeOpenAITools(rt)...)
	toolExecutor = composeRuntimeToolExecutor(toolExecutor, rt)
	toolFilter = llmcontracts.ComposeRuntimeToolFilter(toolFilter, rt, runtimeToolPolicyOptions(isTaskFollowup, chatMode))

	sw := llmstream.NewWriterWithPublisher(execID, "", a.execRepo, ctx, 500*time.Millisecond, a.streamHub)
	defer sw.Stop()
	chatInThinking := false

	disableTools := !isTaskFollowup && chatMode != models.ChatModePlan && rt == nil
	skipDefaultTools := agentSkipDefaultTools(agentDef) || llmcontracts.RuntimeSkipDefaultTools(rt)
	resp, err := client.SendAgentic(ctx, message, &openaiclient.AgenticOptions{
		Model:                  agent.Model,
		MaxOutputTokens:        openAIAgenticOutputBudget,
		System:                 systemPromptStr,
		ReasoningEffort:        reasoningEffort(agent.Model, agent.ReasoningEffort),
		ReasoningSummary:       "auto",
		AutoCompaction:         true,
		WebSearchEnabled:       true,
		DisableTools:           disableTools,
		WorkDir:                effectiveWorkDir,
		Attachments:            oaAttachments,
		ExtraTools:             extraTools,
		ToolExecutor:           toolExecutor,
		ToolFilter:             toolFilter,
		SkipDefaultTools:       skipDefaultTools,
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
			if isStreamingMarkerChunk(text) {
				llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventRawOutput, Text: text}, false)
				return
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
			applog.Infof("[openai-adapter] CallChatStreaming context compacted, summary_len=%d", len(summary))
		},
	})
	if err != nil {
		sw.Flush()
		applog.Infof("[openai-adapter] CallChatStreaming error: %v", err)
		return "", llmusage.FromTotal(0), wrapAuthScopeError(agent, err)
	}
	if chatInThinking {
		chatInThinking = false
		llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingEnd}, false)
	}

	sw.Flush()

	output := sw.String()
	usage := llmusage.FromOpenAI(resp.InputTokens, resp.OutputTokens, resp.CachedInputTokens, resp.ReasoningTokens)
	applog.Infof("[openai-adapter] CallChatStreaming success output_len=%d tokens=%d tools=%d stop=%s compacted=%v", len(output), usage.TotalTokens, len(resp.ToolCalls), resp.StopReason, resp.Compacted)
	if isMaxTokensStopReason(resp.StopReason) {
		return output, usage, errMaxTokens
	}
	return output, usage, nil
}

// CallCompletionsStreaming uses /v1/chat/completions as a fallback.
func (a *Adapter) CallCompletionsStreaming(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string, projectInstructions string, agentDef *models.Agent) (string, string, llmcontracts.Usage, error) {
	applog.Infof("[openai-adapter] CallCompletionsStreaming (fallback) model=%s exec=%s", agent.Model, execID)

	client, releaseTransport, err := a.getClient(ctx, agent, "")
	if err != nil {
		return "", "", llmusage.FromTotal(0), err
	}
	defer releaseTransport()

	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	fullPrompt := llmprompt.BuildTaskPromptHeader() +
		llmprompt.BuildAttachmentInstructions(attachments) +
		prompt
	fullPrompt = llmprompt.ApplyTaskCreationToolMode(fullPrompt, rt.DefinitionNames())
	fullPrompt += "\n\n---\nRESPONSE FORMAT REQUIREMENT: You MUST end your final response with exactly one of these status lines:\n" +
		"- If the task completed successfully: [STATUS: SUCCESS]\n" +
		"- If a command failed, a script returned non-zero, or the task could not be completed: [STATUS: FAILED | <describe what went wrong>]\n" +
		"- If the task completed but something needs human attention: [STATUS: NEEDS_FOLLOWUP | <describe what needs attention>]\n" +
		"Example: [STATUS: FAILED | fail.sh returned exit code 1]\n" +
		"Example: [STATUS: NEEDS_FOLLOWUP | tests pass but 3 warnings need review]\n" +
		"Replace <describe what went wrong> or <describe what needs attention> with your actual description.\n" +
		"This status line is MANDATORY. Always include it as the very last line of your response."

	oaAttachments, err := convertAttachments(attachments)
	if err != nil {
		return "", "", llmusage.FromTotal(0), fmt.Errorf("convert attachments: %w", err)
	}

	effectiveWorkDir := workDir
	if effectiveWorkDir == "" {
		effectiveWorkDir = "."
	}
	extraTools, toolExecutor, toolFilter, cleanupRuntime := buildOpenAIRuntime(ctx, effectiveWorkDir, agentDef)
	defer cleanupRuntime()
	extraTools = append(extraTools, runtimeOpenAITools(rt)...)
	toolExecutor = composeRuntimeToolExecutor(toolExecutor, rt)
	toolFilter = llmcontracts.ComposeRuntimeToolFilter(toolFilter, rt, runtimeToolPolicyOptions(true, models.ChatModeOrchestrate))

	sw := llmstream.NewWriterWithPublisher(execID, "", a.execRepo, ctx, 500*time.Millisecond, a.streamHub)
	defer sw.Stop()

	skipDefaultTools := agentSkipDefaultTools(agentDef) || llmcontracts.RuntimeSkipDefaultTools(rt)
	resp, err := client.SendCompletions(ctx, fullPrompt, &openaiclient.CompletionsOptions{
		Model:            agent.Model,
		MaxOutputTokens:  openAIAgenticOutputBudget,
		System:           applyOpenAIOAuthSystemPrompt(llmprompt.BuildAgentSystemPrompt(projectInstructions, effectiveWorkDir), agent),
		WorkDir:          effectiveWorkDir,
		Attachments:      oaAttachments,
		ExtraTools:       extraTools,
		ToolExecutor:     toolExecutor,
		ToolFilter:       toolFilter,
		SkipDefaultTools: skipDefaultTools,
		OnText: func(text string) {
			if isStreamingMarkerChunk(text) {
				llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventRawOutput, Text: text}, false)
				return
			}
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventTextDelta, Text: text}, false)
		},
		OnToolUse: func(name string, input json.RawMessage) {
			secondary := toolSecondaryInfo(name, input)
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventToolUse, ToolName: name, Secondary: secondary}, false)
		},
		OnToolResult: func(name string, output string, isError bool) {
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventToolResult, ToolName: name, Output: output, IsError: isError}, false)
		},
	})
	if err != nil {
		sw.Flush()
		return "", "", llmusage.FromTotal(0), fmt.Errorf("completions API: %w", err)
	}

	sw.Flush()

	output := sw.String()
	textOnly := sw.TextString()
	usage := llmusage.FromOpenAI(resp.InputTokens, resp.OutputTokens, resp.CachedInputTokens, resp.ReasoningTokens)
	applog.Infof("[openai-adapter] CallCompletionsStreaming success output_len=%d tokens=%d tools=%d stop=%s", len(output), usage.TotalTokens, len(resp.ToolCalls), resp.StopReason)
	if isMaxTokensStopReason(resp.StopReason) {
		return output, textOnly, usage, errMaxTokens
	}
	return output, textOnly, usage, nil
}

// CallCompletionsChatStreaming uses /v1/chat/completions for chat with history.
func (a *Adapter) CallCompletionsChatStreaming(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, isTaskFollowup bool, chatMode models.ChatMode, workDir string, agentDef *models.Agent) (string, llmcontracts.Usage, error) {
	applog.Infof("[openai-adapter] CallCompletionsChatStreaming (fallback) model=%s history=%d exec=%s", agent.Model, len(chatHistory), execID)

	client, releaseTransport, err := a.getClient(ctx, agent, "")
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}
	defer releaseTransport()

	client.History = append(client.History, buildClientHistory(chatHistory)...)
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	systemPromptStr := llmprompt.BuildChatSystemPrompt(isTaskFollowup, chatMode, chatSystemContext, false)
	systemPromptStr = llmprompt.AppendWorktreeContextPrompt(systemPromptStr, workDir)
	systemPromptStr = appendToolModeSystemPrompt(systemPromptStr, rt, chatMode)
	systemPromptStr = applyOpenAIOAuthSystemPrompt(systemPromptStr, agent)

	oaAttachments, err := convertAttachments(attachments)
	if err != nil {
		return "", llmusage.FromTotal(0), fmt.Errorf("convert attachments: %w", err)
	}

	effectiveWorkDir := workDir
	if effectiveWorkDir == "" {
		effectiveWorkDir = "."
	}
	extraTools, toolExecutor, toolFilter, cleanupRuntime := buildOpenAIRuntime(ctx, effectiveWorkDir, agentDef)
	defer cleanupRuntime()
	extraTools = append(extraTools, runtimeOpenAITools(rt)...)
	toolExecutor = composeRuntimeToolExecutor(toolExecutor, rt)
	toolFilter = llmcontracts.ComposeRuntimeToolFilter(toolFilter, rt, runtimeToolPolicyOptions(isTaskFollowup, chatMode))

	sw := llmstream.NewWriterWithPublisher(execID, "", a.execRepo, ctx, 500*time.Millisecond, a.streamHub)
	defer sw.Stop()

	disableTools := !isTaskFollowup && chatMode != models.ChatModePlan && rt == nil
	skipDefaultTools := agentSkipDefaultTools(agentDef) || llmcontracts.RuntimeSkipDefaultTools(rt)
	resp, err := client.SendCompletions(ctx, message, &openaiclient.CompletionsOptions{
		Model:            agent.Model,
		MaxOutputTokens:  openAIAgenticOutputBudget,
		System:           systemPromptStr,
		DisableTools:     disableTools,
		WorkDir:          effectiveWorkDir,
		Attachments:      oaAttachments,
		ExtraTools:       extraTools,
		ToolExecutor:     toolExecutor,
		ToolFilter:       toolFilter,
		SkipDefaultTools: skipDefaultTools,
		OnText: func(text string) {
			if isStreamingMarkerChunk(text) {
				llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventRawOutput, Text: text}, false)
				return
			}
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventTextDelta, Text: text}, false)
		},
		OnToolUse: func(name string, input json.RawMessage) {
			secondary := toolSecondaryInfo(name, input)
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventToolUse, ToolName: name, Secondary: secondary}, false)
		},
		OnToolResult: func(name string, output string, isError bool) {
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventToolResult, ToolName: name, Output: output, IsError: isError}, false)
		},
	})
	if err != nil {
		sw.Flush()
		return "", llmusage.FromTotal(0), fmt.Errorf("completions API: %w", err)
	}

	sw.Flush()

	output := sw.String()
	usage := llmusage.FromOpenAI(resp.InputTokens, resp.OutputTokens, resp.CachedInputTokens, resp.ReasoningTokens)
	applog.Infof("[openai-adapter] CallCompletionsChatStreaming success output_len=%d tokens=%d tools=%d stop=%s", len(output), usage.TotalTokens, len(resp.ToolCalls), resp.StopReason)
	if isMaxTokensStopReason(resp.StopReason) {
		return output, usage, errMaxTokens
	}
	return output, usage, nil
}

func (a *Adapter) openAIRefreshFunc() llmoauth.RefreshFunc {
	return func(ctx context.Context, cfg models.LLMConfig) (llmoauth.TokenSet, error) {
		auth, err := openaiclient.RefreshToken(cfg.OAuthRefreshToken)
		if err != nil {
			return llmoauth.TokenSet{}, err
		}
		return llmoauth.TokenSet{AccessToken: auth.Token, RefreshToken: auth.RefreshToken, ExpiresAt: auth.ExpiresAt, AccountID: cfg.OAuthAccountID}, nil
	}
}

func (a *Adapter) ensureFreshOAuth(ctx context.Context, agent models.LLMConfig) (models.LLMConfig, error) {
	return a.oauthRecovery.EnsureFresh(ctx, agent, time.Hour, a.openAIRefreshFunc())
}

func (a *Adapter) recoverUnauthorized(ctx context.Context, agent models.LLMConfig, tokenUsed string) (models.LLMConfig, bool, error) {
	return a.oauthRecovery.RecoverUnauthorized(ctx, agent, tokenUsed, a.openAIRefreshFunc())
}

func (a *Adapter) newOAuthClient(ctx context.Context, agent models.LLMConfig) *openaiclient.Client {
	client := openaiclient.NewWithOAuthToken(agent.OAuthAccessToken, agent.OAuthRefreshToken, agent.OAuthExpiresAt, agent.OAuthAccountID)
	client.SetOAuthUnauthorizedHandler(func(ctx context.Context, tokenUsed string) (openaiclient.OAuthTokens, bool, error) {
		fresh, recovered, err := a.recoverUnauthorized(ctx, agent, tokenUsed)
		if err != nil || !recovered {
			return openaiclient.OAuthTokens{}, recovered, err
		}
		return openaiclient.OAuthTokens{AccessToken: fresh.OAuthAccessToken, RefreshToken: fresh.OAuthRefreshToken, ExpiresAt: fresh.OAuthExpiresAt, AccountID: fresh.OAuthAccountID}, true, nil
	})
	return client
}

func (a *Adapter) acquireResponsesTransportState(agent models.LLMConfig, scope string) (*openaiclient.ResponsesTransportState, func()) {
	key := strings.TrimSpace(agent.ID)
	if key == "" {
		key = strings.Join([]string{string(agent.Provider), string(agent.AuthMethod), agent.Name, agent.Model}, "|")
	}
	key += "|" + responsesTransportAuthIdentity(agent) + "|" + scope
	now := time.Now()
	a.transportMu.Lock()
	if a.transportStates == nil {
		a.transportStates = make(map[string]*responsesTransportEntry)
	}
	var evicted []*openaiclient.ResponsesTransportState
	for existingKey, entry := range a.transportStates {
		if entry.leases == 0 && now.Sub(entry.lastUsed) >= responsesTransportTTL {
			delete(a.transportStates, existingKey)
			evicted = append(evicted, entry.state)
		}
	}
	if entry, ok := a.transportStates[key]; ok {
		entry.lastUsed = now
		entry.leases++
		a.transportMu.Unlock()
		for _, stale := range evicted {
			stale.Close()
		}
		return entry.state, a.responsesTransportRelease(key, entry)
	}
	if len(a.transportStates) >= responsesTransportMax {
		oldestKey := ""
		var oldest *responsesTransportEntry
		for existingKey, entry := range a.transportStates {
			if entry.leases == 0 && (oldestKey == "" || entry.lastUsed.Before(oldest.lastUsed)) {
				oldestKey, oldest = existingKey, entry
			}
		}
		if oldestKey != "" {
			delete(a.transportStates, oldestKey)
			evicted = append(evicted, oldest.state)
		}
	}
	state := openaiclient.NewResponsesTransportState()
	entry := &responsesTransportEntry{state: state, lastUsed: now, leases: 1}
	a.transportStates[key] = entry
	a.transportMu.Unlock()
	for _, stale := range evicted {
		stale.Close()
	}
	return state, a.responsesTransportRelease(key, entry)
}

func (a *Adapter) responsesTransportRelease(key string, entry *responsesTransportEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			now := time.Now()
			var stale *openaiclient.ResponsesTransportState
			a.transportMu.Lock()
			if entry.leases > 0 {
				entry.leases--
			}
			if current, ok := a.transportStates[key]; ok && current == entry && entry.leases == 0 &&
				(now.Sub(entry.lastUsed) >= responsesTransportTTL || len(a.transportStates) > responsesTransportMax) {
				delete(a.transportStates, key)
				stale = entry.state
			}
			a.transportMu.Unlock()
			if stale != nil {
				stale.Close()
			}
		})
	}
}

func responsesTransportAuthIdentity(agent models.LLMConfig) string {
	if agent.IsOpenAIOAuth() {
		if accountID := strings.TrimSpace(agent.OAuthAccountID); accountID != "" {
			return credentialFingerprint("oauth-account", accountID)
		}
		return credentialFingerprint("oauth-token", agent.OAuthAccessToken)
	}
	return credentialFingerprint("api-key", agent.APIKey)
}

func credentialFingerprint(kind, credential string) string {
	sum := sha256.Sum256([]byte(credential))
	return fmt.Sprintf("%s:%x", kind, sum[:8])
}

func (a *Adapter) getClient(ctx context.Context, agent models.LLMConfig, transportScope string) (*openaiclient.Client, func(), error) {
	releaseTransport := func() {}
	if agent.IsOpenAIAPIKey() {
		if strings.TrimSpace(agent.APIKey) == "" {
			return nil, releaseTransport, fmt.Errorf("OpenAI API key not configured for model %q", agent.Name)
		}
		client := openaiclient.NewWithAPIKey(agent.APIKey)
		releaseTransport = client.CloseResponsesTransport
		if transportScope != "" {
			state, release := a.acquireResponsesTransportState(agent, transportScope)
			client.SetResponsesTransportState(state)
			releaseTransport = release
		}
		return client, releaseTransport, nil
	}

	if agent.IsOpenAIOAuth() {
		if strings.TrimSpace(agent.OAuthAccessToken) == "" {
			return nil, releaseTransport, fmt.Errorf("OAuth not configured for model %q - click 'Connect with OAuth' on the Models page", agent.Name)
		}

		agent, err := a.ensureFreshOAuth(ctx, agent)
		if err != nil {
			applog.Infof("[openai-adapter] getClient token refresh failed for agent=%s: %v", agent.Name, err)
			return nil, releaseTransport, err
		}
		client := a.newOAuthClient(ctx, agent)
		releaseTransport = client.CloseResponsesTransport
		if transportScope != "" {
			state, release := a.acquireResponsesTransportState(agent, transportScope)
			client.SetResponsesTransportState(state)
			releaseTransport = release
		}
		return client, releaseTransport, nil
	}

	return nil, releaseTransport, fmt.Errorf("OpenAI model %q is configured with auth_method=%q; expected api_key or oauth", agent.Name, agent.AuthMethod)
}

func (a *Adapter) taskTransportScope(ctx context.Context, execID string) string {
	execID = strings.TrimSpace(execID)
	if execID == "" || a.execRepo == nil {
		return ""
	}
	execution, err := a.execRepo.GetByID(ctx, execID)
	if err != nil {
		applog.Infof("[openai-adapter] resolve task transport scope exec=%s: %v", execID, err)
		return ""
	}
	if execution == nil || strings.TrimSpace(execution.TaskID) == "" {
		return ""
	}
	return "task:" + strings.TrimSpace(execution.TaskID)
}

func buildClientHistory(chatHistory []models.Execution) []openaiclient.Message {
	history := llmprompt.LimitChatHistory(chatHistory)
	var messages []openaiclient.Message
	for _, exec := range history {
		if exec.PromptSent != "" {
			messages = append(messages, openaiclient.Message{Role: "user", Content: exec.PromptSent})
		}
		if replay := llmprompt.ReplayAssistantContent(exec); replay != "" {
			messages = append(messages, openaiclient.Message{Role: "assistant", Content: replay})
		}
	}
	return messages
}

func convertAttachments(attachments []models.Attachment) ([]*openaiclient.FileAttachment, error) {
	if len(attachments) == 0 {
		return nil, nil
	}

	prepared, err := llmattachment.Preprocess(attachments)
	if err != nil {
		return nil, fmt.Errorf("preprocess attachments: %w", err)
	}

	result := make([]*openaiclient.FileAttachment, 0, len(prepared))
	for _, att := range prepared {
		oaAtt, err := openaiclient.NewFileAttachment(att.FilePath)
		if err != nil {
			// Skip unsupported file types silently (e.g. PDFs)
			if _, ok := err.(*openaiclient.UnsupportedFileTypeError); ok {
				applog.Infof("[openai-adapter] convertAttachments skipping unsupported file %s: %v", att.FileName, err)
				continue
			}
			return nil, fmt.Errorf("load attachment %s: %w", att.FileName, err)
		}
		if strings.TrimSpace(att.FileName) != "" {
			oaAtt.FileName = att.FileName
		}
		if strings.TrimSpace(att.MediaType) != "" {
			oaAtt.MediaType = strings.TrimSpace(att.MediaType)
		}
		result = append(result, oaAtt)
	}
	return result, nil
}

func reasoningEffort(model, value string) string {
	effort := llmprompt.NormalizeReasoningEffortValue(value)
	if llmprompt.StringInSlice(effort, llmprompt.CodexSupportedReasoningEfforts(model)) {
		return effort
	}
	return llmprompt.CodexDefaultReasoningEffort(model)
}

func isStreamingMarkerChunk(text string) bool {
	return strings.Contains(text, "[Using tool:") ||
		strings.Contains(text, "[Tool ") ||
		strings.Contains(text, "[/Tool]")
}

func wrapAuthScopeError(agent models.LLMConfig, err error) error {
	if err == nil {
		return nil
	}
	if agent.IsOpenAIOAuth() && strings.Contains(strings.ToLower(err.Error()), "missing scopes:") {
		return fmt.Errorf("openai API call failed: OAuth token is valid but lacks required API scopes for /v1/responses (%w)", err)
	}
	return fmt.Errorf("openai API call: %w", err)
}

func toolSecondaryInfo(name string, input json.RawMessage) string {
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	switch name {
	case "read_file", "write_file", "edit_file":
		if fp, ok := m["file_path"].(string); ok {
			parts := strings.Split(fp, "/")
			return parts[len(parts)-1]
		}
	case "bash":
		if cmd, ok := m["command"].(string); ok {
			cmd = truncateToolSecondary(cmd, 320)
			return "$ " + cmd
		}
	case "grep_search":
		if p, ok := m["pattern"].(string); ok {
			return truncateToolSecondary(p, 140)
		}
	case "list_files":
		if p, ok := m["path"].(string); ok {
			return p
		}
		if p, ok := m["pattern"].(string); ok {
			return p
		}
	case "web_search", "web_search_preview":
		if detail := webSearchSecondaryFromInput(m); detail != "" {
			return truncateToolSecondary(detail, 140)
		}
	}
	return ""
}

func webSearchSecondaryFromInput(m map[string]interface{}) string {
	getString := func(key string) string {
		v, _ := m[key].(string)
		return strings.TrimSpace(v)
	}

	query := getString("query")
	if query != "" {
		return query
	}

	url := getString("url")
	pattern := getString("pattern")

	action := strings.ToLower(getString("action"))
	if action == "" {
		if actionMap, ok := m["action"].(map[string]interface{}); ok {
			if t, ok := actionMap["type"].(string); ok {
				action = strings.ToLower(strings.TrimSpace(t))
			}
		}
	}

	switch action {
	case "findinpage", "find_in_page":
		if pattern != "" && url != "" {
			return "'" + pattern + "' in " + url
		}
		if pattern != "" {
			return "'" + pattern + "'"
		}
		if url != "" {
			return url
		}
	case "openpage", "open_page":
		if url != "" {
			return url
		}
	}

	if pattern != "" && url != "" {
		return "'" + pattern + "' in " + url
	}
	if pattern != "" {
		return "'" + pattern + "'"
	}
	if url != "" {
		return url
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

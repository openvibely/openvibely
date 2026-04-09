package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	llmattachment "github.com/openvibely/openvibely/internal/llm/attachment"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
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

// isMaxTokensStopReason returns true if the stop reason indicates the response
// was truncated due to hitting the output token limit.
func isMaxTokensStopReason(reason string) bool {
	// Responses API: "max_output_tokens" (via incomplete_details.reason)
	// Completions API: "length" (via finish_reason)
	return reason == "max_output_tokens" || reason == "length"
}

// Adapter handles all OpenAI-specific provider logic.
type Adapter struct {
	llmConfigRepo *repository.LLMConfigRepo
	execRepo      *repository.ExecutionRepo
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
	default:
		return ""
	}
}

func agentAllowsBuiltInTool(agentDef *models.Agent, toolName string) bool {
	if agentDef == nil || len(agentDef.Tools) == 0 {
		return true
	}
	mapped := mapBuiltInToolName(toolName)
	if mapped == "" {
		return true
	}
	for _, t := range agentDef.Tools {
		if strings.EqualFold(strings.TrimSpace(t), mapped) {
			return true
		}
	}
	return false
}

func planModeAllowsReadOnlyTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read_file", "list_files", "grep_search":
		return true
	default:
		return false
	}
}

func wrapToolFilterForPlanMode(base func(string) bool, isTaskFollowup bool, chatMode models.ChatMode) func(string) bool {
	if isTaskFollowup || chatMode != models.ChatModePlan {
		return base
	}
	return func(name string) bool {
		if !planModeAllowsReadOnlyTool(name) {
			return false
		}
		if base == nil {
			return true
		}
		return base(name)
	}
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

func runtimeToolNameSet(rt *llmcontracts.RuntimeTools) map[string]struct{} {
	if rt == nil || len(rt.Definitions) == 0 {
		return nil
	}
	names := make(map[string]struct{}, len(rt.Definitions))
	for _, def := range rt.Definitions {
		n := strings.ToLower(strings.TrimSpace(def.Name))
		if n == "" {
			continue
		}
		names[n] = struct{}{}
	}
	return names
}

func isRuntimeTool(runtimeNames map[string]struct{}, name string) bool {
	if len(runtimeNames) == 0 {
		return false
	}
	_, ok := runtimeNames[strings.ToLower(strings.TrimSpace(name))]
	return ok
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

func composeRuntimeToolFilter(base func(string) bool, rt *llmcontracts.RuntimeTools, isTaskFollowup bool, chatMode models.ChatMode) func(string) bool {
	runtimeNames := runtimeToolNameSet(rt)
	return func(name string) bool {
		isActionTool := isRuntimeTool(runtimeNames, name)

		if !isTaskFollowup {
			switch chatMode {
			case models.ChatModePlan:
				// Plan mode: read-only exploration tools only; no action tools.
				if isActionTool {
					return false
				}
				if !planModeAllowsReadOnlyTool(name) {
					return false
				}
			default:
				// Orchestrate mode: action tools only (no filesystem/mcp tools).
				if !isActionTool {
					return false
				}
			}
		}

		if isActionTool {
			if rt != nil && rt.Filter != nil {
				allow, handled := rt.Filter(name)
				if handled {
					return allow
				}
			}
			return true
		}

		if base != nil {
			return base(name)
		}
		return true
	}
}

func appendToolModeSystemPrompt(base string, rt *llmcontracts.RuntimeTools, isTaskFollowup bool, chatMode models.ChatMode) string {
	if rt == nil || isTaskFollowup || chatMode != models.ChatModeOrchestrate {
		return base
	}
	var names []string
	for _, def := range rt.Definitions {
		if n := strings.TrimSpace(def.Name); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return base
	}
	return base + "\n\n" + llmprompt.ChatActionToolModeInstructions + "\nAvailable action tools: " + strings.Join(names, ", ")
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
		log.Printf("[openai-adapter] MCP manager init failed: %v", err)
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
func New(llmConfigRepo *repository.LLMConfigRepo, execRepo *repository.ExecutionRepo) *Adapter {
	return &Adapter{
		llmConfigRepo: llmConfigRepo,
		execRepo:      execRepo,
	}
}

// CallDirect makes a non-streaming OpenAI API call.
func (a *Adapter) CallDirect(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, disableTools bool) (string, llmcontracts.Usage, error) {
	log.Printf("[openai-adapter] CallDirect model=%s max_tokens=%d attachments=%d auth_method=%s disable_tools=%v", agent.Model, agent.MaxTokens, len(attachments), agent.AuthMethod, disableTools)

	client, err := a.getClient(ctx, agent)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}

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

	maxTokens := agent.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	resp, err := client.Send(ctx, fullPrompt, &openaiclient.SendOptions{
		Model:               agent.Model,
		MaxOutputTokens:     maxTokens,
		ReasoningEffort:     reasoningEffort(agent.ReasoningEffort),
		DisableTools:        disableTools,
		SuppressToolMarkers: disableTools,
		Attachments:         oaAttachments,
	})
	if err != nil {
		log.Printf("[openai-adapter] CallDirect error: %v", err)
		return "", llmusage.FromTotal(0), wrapAuthScopeError(agent, err)
	}

	usage := llmusage.FromOpenAI(resp.InputTokens, resp.OutputTokens, resp.CachedInputTokens, resp.ReasoningTokens)
	return resp.Text, usage, nil
}

// CallStreaming makes a streaming OpenAI API call with tool use.
func (a *Adapter) CallStreaming(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string, projectInstructions string, agentDef *models.Agent) (string, string, llmcontracts.Usage, error) {
	log.Printf("[openai-adapter] CallStreaming model=%s max_tokens=%d attachments=%d exec=%s auth_method=%s workDir=%s", agent.Model, agent.MaxTokens, len(attachments), execID, agent.AuthMethod, workDir)

	client, err := a.getClient(ctx, agent)
	if err != nil {
		return "", "", llmusage.FromTotal(0), err
	}

	fullPrompt := llmprompt.BuildTaskPromptHeader() +
		llmprompt.BuildAttachmentInstructions(attachments) +
		prompt
	fullPrompt += "\n\n" + llmprompt.TaskCreationInstructions
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

	sw := llmstream.NewWriter(execID, "", a.execRepo, ctx, 500*time.Millisecond)
	defer sw.Stop()
	inThinking := false

	resp, err := client.SendAgentic(ctx, fullPrompt, &openaiclient.AgenticOptions{
		Model:            agent.Model,
		MaxOutputTokens:  agenticMaxTokens(agent.MaxTokens),
		System:           applyOpenAIOAuthSystemPrompt(llmprompt.BuildAgentSystemPrompt(projectInstructions, effectiveWorkDir), agent),
		ReasoningEffort:  reasoningEffort(agent.ReasoningEffort),
		ReasoningSummary: "auto",
		AutoCompaction:   true,
		WorkDir:          effectiveWorkDir,
		Attachments:      oaAttachments,
		ExtraTools:       extraTools,
		ToolExecutor:     toolExecutor,
		ToolFilter:       toolFilter,
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
			log.Printf("[openai-adapter] CallStreaming context compacted, summary_len=%d", len(summary))
		},
	})
	if err != nil {
		sw.Flush()
		log.Printf("[openai-adapter] CallStreaming error: %v", err)
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
	log.Printf("[openai-adapter] CallStreaming success output_len=%d tokens=%d tools=%d stop=%s compacted=%v", len(output), usage.TotalTokens, len(resp.ToolCalls), resp.StopReason, resp.Compacted)
	if isMaxTokensStopReason(resp.StopReason) {
		return output, textOnly, usage, errMaxTokens
	}
	return output, textOnly, usage, nil
}

// CallChatStreaming makes a streaming OpenAI chat call with history.
func (a *Adapter) CallChatStreaming(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, isTaskFollowup bool, chatMode models.ChatMode, workDir string, agentDef *models.Agent) (string, llmcontracts.Usage, error) {
	log.Printf("[openai-adapter] CallChatStreaming model=%s history=%d message_len=%d context_len=%d attachments=%d exec=%s isTaskFollowup=%v auth_method=%s workDir=%s",
		agent.Model, len(chatHistory), len(message), len(chatSystemContext), len(attachments), execID, isTaskFollowup, agent.AuthMethod, workDir)

	client, err := a.getClient(ctx, agent)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}

	client.History = append(client.History, buildClientHistory(chatHistory)...)
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	systemPromptStr := llmprompt.BuildChatSystemPrompt(isTaskFollowup, chatMode, chatSystemContext, false)
	systemPromptStr = llmprompt.AppendWorktreeContextPrompt(systemPromptStr, workDir)
	systemPromptStr = appendToolModeSystemPrompt(systemPromptStr, rt, isTaskFollowup, chatMode)
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
	toolFilter = composeRuntimeToolFilter(toolFilter, rt, isTaskFollowup, chatMode)

	sw := llmstream.NewWriter(execID, "", a.execRepo, ctx, 500*time.Millisecond)
	defer sw.Stop()
	chatInThinking := false

	disableTools := !isTaskFollowup && chatMode != models.ChatModePlan && rt == nil
	resp, err := client.SendAgentic(ctx, message, &openaiclient.AgenticOptions{
		Model:            agent.Model,
		MaxOutputTokens:  agenticMaxTokens(agent.MaxTokens),
		System:           systemPromptStr,
		ReasoningEffort:  reasoningEffort(agent.ReasoningEffort),
		ReasoningSummary: "auto",
		AutoCompaction:   true,
		DisableTools:     disableTools,
		WorkDir:          effectiveWorkDir,
		Attachments:      oaAttachments,
		ExtraTools:       extraTools,
		ToolExecutor:     toolExecutor,
		ToolFilter:       toolFilter,
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
			log.Printf("[openai-adapter] CallChatStreaming context compacted, summary_len=%d", len(summary))
		},
	})
	if err != nil {
		sw.Flush()
		log.Printf("[openai-adapter] CallChatStreaming error: %v", err)
		return "", llmusage.FromTotal(0), wrapAuthScopeError(agent, err)
	}
	if chatInThinking {
		chatInThinking = false
		llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingEnd}, false)
	}

	sw.Flush()

	output := sw.String()
	usage := llmusage.FromOpenAI(resp.InputTokens, resp.OutputTokens, resp.CachedInputTokens, resp.ReasoningTokens)
	log.Printf("[openai-adapter] CallChatStreaming success output_len=%d tokens=%d tools=%d stop=%s compacted=%v", len(output), usage.TotalTokens, len(resp.ToolCalls), resp.StopReason, resp.Compacted)
	if isMaxTokensStopReason(resp.StopReason) {
		return output, usage, errMaxTokens
	}
	return output, usage, nil
}

// CallCompletionsStreaming uses /v1/chat/completions as a fallback.
func (a *Adapter) CallCompletionsStreaming(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string, projectInstructions string, agentDef *models.Agent) (string, string, llmcontracts.Usage, error) {
	log.Printf("[openai-adapter] CallCompletionsStreaming (fallback) model=%s exec=%s", agent.Model, execID)

	client, err := a.getClient(ctx, agent)
	if err != nil {
		return "", "", llmusage.FromTotal(0), err
	}

	fullPrompt := llmprompt.BuildTaskPromptHeader() +
		llmprompt.BuildAttachmentInstructions(attachments) +
		prompt
	fullPrompt += "\n\n" + llmprompt.TaskCreationInstructions
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

	sw := llmstream.NewWriter(execID, "", a.execRepo, ctx, 500*time.Millisecond)
	defer sw.Stop()

	resp, err := client.SendCompletions(ctx, fullPrompt, &openaiclient.CompletionsOptions{
		Model:           agent.Model,
		MaxOutputTokens: agenticMaxTokens(agent.MaxTokens),
		System:          applyOpenAIOAuthSystemPrompt(llmprompt.BuildAgentSystemPrompt(projectInstructions, effectiveWorkDir), agent),
		WorkDir:         effectiveWorkDir,
		Attachments:     oaAttachments,
		ExtraTools:      extraTools,
		ToolExecutor:    toolExecutor,
		ToolFilter:      toolFilter,
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
	log.Printf("[openai-adapter] CallCompletionsStreaming success output_len=%d tokens=%d tools=%d stop=%s", len(output), usage.TotalTokens, len(resp.ToolCalls), resp.StopReason)
	if isMaxTokensStopReason(resp.StopReason) {
		return output, textOnly, usage, errMaxTokens
	}
	return output, textOnly, usage, nil
}

// CallCompletionsChatStreaming uses /v1/chat/completions for chat with history.
func (a *Adapter) CallCompletionsChatStreaming(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, isTaskFollowup bool, chatMode models.ChatMode, workDir string, agentDef *models.Agent) (string, llmcontracts.Usage, error) {
	log.Printf("[openai-adapter] CallCompletionsChatStreaming (fallback) model=%s history=%d exec=%s", agent.Model, len(chatHistory), execID)

	client, err := a.getClient(ctx, agent)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}

	client.History = append(client.History, buildClientHistory(chatHistory)...)
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	systemPromptStr := llmprompt.BuildChatSystemPrompt(isTaskFollowup, chatMode, chatSystemContext, false)
	systemPromptStr = llmprompt.AppendWorktreeContextPrompt(systemPromptStr, workDir)
	systemPromptStr = appendToolModeSystemPrompt(systemPromptStr, rt, isTaskFollowup, chatMode)
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
	toolFilter = composeRuntimeToolFilter(toolFilter, rt, isTaskFollowup, chatMode)

	sw := llmstream.NewWriter(execID, "", a.execRepo, ctx, 500*time.Millisecond)
	defer sw.Stop()

	disableTools := !isTaskFollowup && chatMode != models.ChatModePlan && rt == nil
	resp, err := client.SendCompletions(ctx, message, &openaiclient.CompletionsOptions{
		Model:           agent.Model,
		MaxOutputTokens: agenticMaxTokens(agent.MaxTokens),
		System:          systemPromptStr,
		DisableTools:    disableTools,
		WorkDir:         effectiveWorkDir,
		Attachments:     oaAttachments,
		ExtraTools:      extraTools,
		ToolExecutor:    toolExecutor,
		ToolFilter:      toolFilter,
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
	log.Printf("[openai-adapter] CallCompletionsChatStreaming success output_len=%d tokens=%d tools=%d stop=%s", len(output), usage.TotalTokens, len(resp.ToolCalls), resp.StopReason)
	if isMaxTokensStopReason(resp.StopReason) {
		return output, usage, errMaxTokens
	}
	return output, usage, nil
}

func (a *Adapter) getClient(ctx context.Context, agent models.LLMConfig) (*openaiclient.Client, error) {
	if agent.IsOpenAIAPIKey() {
		if strings.TrimSpace(agent.APIKey) == "" {
			return nil, fmt.Errorf("OpenAI API key not configured for model %q", agent.Name)
		}
		return openaiclient.NewWithAPIKey(agent.APIKey), nil
	}

	if agent.IsOpenAIOAuth() {
		if strings.TrimSpace(agent.OAuthAccessToken) == "" {
			return nil, fmt.Errorf("OAuth not configured for model %q - click 'Connect with OAuth' on the Models page", agent.Name)
		}

		client := openaiclient.NewWithOAuthToken(agent.OAuthAccessToken, agent.OAuthRefreshToken, agent.OAuthExpiresAt, agent.OAuthAccountID)
		before := client.CurrentAuth()
		if err := client.EnsureValidToken(); err != nil {
			log.Printf("[openai-adapter] getClient token refresh failed for agent=%s: %v", agent.Name, err)
			return nil, fmt.Errorf("OAuth token refresh failed for %q: %w", agent.Name, err)
		}

		after := client.CurrentAuth()
		if agent.ID != "" && (before.Token != after.Token || before.RefreshToken != after.RefreshToken || before.ExpiresAt != after.ExpiresAt) {
			if err := a.llmConfigRepo.UpdateOAuthTokens(ctx, agent.ID, after.Token, after.RefreshToken, after.ExpiresAt); err != nil {
				log.Printf("[openai-adapter] getClient failed to persist refreshed token for agent=%s: %v", agent.Name, err)
			}
		}

		return client, nil
	}

	return nil, fmt.Errorf("OpenAI model %q is configured with auth_method=%q; expected api_key or oauth", agent.Name, agent.AuthMethod)
}

func buildClientHistory(chatHistory []models.Execution) []openaiclient.Message {
	history := llmprompt.LimitChatHistory(chatHistory)
	var messages []openaiclient.Message
	for _, exec := range history {
		if exec.PromptSent != "" {
			messages = append(messages, openaiclient.Message{Role: "user", Content: exec.PromptSent})
		}
		if exec.Output != "" && (exec.Status == models.ExecCompleted || exec.Status == models.ExecFailed) {
			messages = append(messages, openaiclient.Message{Role: "assistant", Content: exec.Output})
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
				log.Printf("[openai-adapter] convertAttachments skipping unsupported file %s: %v", att.FileName, err)
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

func reasoningEffort(value string) string {
	switch llmprompt.NormalizeReasoningEffortValue(value) {
	case "low", "medium", "high":
		return llmprompt.NormalizeReasoningEffortValue(value)
	default:
		return "high"
	}
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

func agenticMaxTokens(maxTokens int) int {
	if maxTokens == 0 {
		return 16384
	}
	return maxTokens
}

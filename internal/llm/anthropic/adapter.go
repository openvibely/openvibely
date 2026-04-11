package anthropic

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
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	mcpclient "github.com/openvibely/openvibely/pkg/mcp_client"
)

// minAgenticMaxTokens is the minimum max_tokens for agentic API calls.
const minAgenticMaxTokens = 16384

// applyAgentToSystemPrompt prepends the agent definition's system prompt and
// skill contents to the base system context string.
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

// Adapter encapsulates Anthropic provider logic.
type Adapter struct {
	llmConfigRepo *repository.LLMConfigRepo
	execRepo      *repository.ExecutionRepo
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
	case "read_file", "list_files", "grep_search",
		"web_search", "web_search_20250305", "web_search_20260209",
		"web_fetch", "web_fetch_20250910", "web_fetch_20260209", "web_fetch_20260309": // provider-native search is read-only
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
			Name:        name,
			Description: strings.TrimSpace(def.Description),
			InputSchema: def.Parameters,
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

func shouldSkipDefaultToolsForChatMode(isTaskFollowup bool, chatMode models.ChatMode, rt *llmcontracts.RuntimeTools) bool {
	// In orchestrate chat with runtime action tools, advertise only runtime tools.
	// Local coding tools are blocked by filter in this mode, so exposing them
	// causes pointless "tool not allowed" turns.
	return !isTaskFollowup && chatMode == models.ChatModeOrchestrate && rt != nil && len(rt.Definitions) > 0
}

func shouldPreferAnthropicProviderWeb(prompt string) bool {
	p := strings.ToLower(strings.TrimSpace(prompt))
	if p == "" {
		return false
	}
	return strings.Contains(p, "http://") || strings.Contains(p, "https://")
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
		log.Printf("[anthropic] MCP manager init failed: %v", err)
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
func New(llmConfigRepo *repository.LLMConfigRepo, execRepo *repository.ExecutionRepo) *Adapter {
	return &Adapter{
		llmConfigRepo: llmConfigRepo,
		execRepo:      execRepo,
	}
}

// Call handles Anthropic LLM requests.
func (a *Adapter) Call(ctx context.Context, req llmcontracts.AgentRequest, workDir string, w *llmstream.Writer) (llmcontracts.AgentResult, error) {
	agent := req.Agent

	// API paths only (OAuth or API key). CLI is handled in service layer.
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

	switch req.Operation {
	case llmcontracts.OperationTask:
		output, textOnly, tokens, err := a.callStreaming(ctx, req.Message, req.Attachments, agent, req.ExecID, workDir, req.ProjectInstructions, extraTools, toolExecutor, toolFilter)
		return llmcontracts.AgentResult{
			Output:         output,
			TextOnlyOutput: textOnly,
			Usage:          llmusage.FromTotal(tokens),
			StopReason:     stopReasonIfMaxTokens(err),
		}, err

	case llmcontracts.OperationStreaming:
		if req.ChatHistory != nil {
			output, tokens, err := a.callChatStreaming(ctx, req.Message, req.Attachments, agent, req.ExecID, req.ChatHistory, req.ChatSystemContext, req.Followup, req.ChatMode, workDir, extraTools, toolExecutor, toolFilter)
			return llmcontracts.AgentResult{
				Output:     output,
				Usage:      llmusage.FromTotal(tokens),
				StopReason: stopReasonIfMaxTokens(err),
			}, err
		}
		output, textOnly, tokens, err := a.callStreaming(ctx, req.Message, req.Attachments, agent, req.ExecID, workDir, req.ProjectInstructions, extraTools, toolExecutor, toolFilter)
		return llmcontracts.AgentResult{
			Output:         output,
			TextOnlyOutput: textOnly,
			Usage:          llmusage.FromTotal(tokens),
			StopReason:     stopReasonIfMaxTokens(err),
		}, err

	case llmcontracts.OperationDirect:
		output, tokens, err := a.callDirect(ctx, req.Message, req.Attachments, agent, workDir, req.ProjectInstructions, extraTools, toolExecutor, toolFilter, req.DisableTools)
		return llmcontracts.AgentResult{
			Output:     output,
			Usage:      llmusage.FromTotal(tokens),
			StopReason: stopReasonIfMaxTokens(err),
		}, err

	default:
		return llmcontracts.AgentResult{}, fmt.Errorf("unsupported operation: %s", req.Operation)
	}
}

// callDirect calls the Anthropic API using OAuth tokens.
func (a *Adapter) callDirect(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, workDir string, projectInstructions string, extraTools []anthropicclient.ToolDefinition, toolExecutor func(context.Context, string, json.RawMessage) (string, bool, error), toolFilter func(string) bool, disableTools bool) (string, int, error) {
	log.Printf("[anthropic] callDirect model=%s max_tokens=%d workDir=%s attachments=%d disable_tools=%v", agent.Model, agent.MaxTokens, workDir, len(attachments), disableTools)

	client, err := a.getClient(ctx, agent)
	if err != nil {
		return "", 0, err
	}

	mcAttachments, err := convertAttachments(attachments)
	if err != nil {
		return "", 0, fmt.Errorf("convert attachments: %w", err)
	}

	fullPrompt := llmprompt.BuildTaskPromptHeader() + prompt
	preferProviderWeb := shouldPreferAnthropicProviderWeb(prompt)
	effectiveDisableTools := disableTools
	skipDefaultTools := preferProviderWeb

	opts := &anthropicclient.AgenticOptions{
		Model:            agent.Model,
		MaxTokens:        agenticMaxTokens(agent.MaxTokens),
		System:           llmprompt.BuildAgentSystemPrompt(projectInstructions, workDir),
		WorkDir:          workDir,
		Attachments:      mcAttachments,
		DisableTools:     effectiveDisableTools,
		SkipDefaultTools: skipDefaultTools,
		AutoCompaction:   true,
		WebSearchEnabled: true,
		ExtraTools:       extraTools,
		ToolExecutor:     toolExecutor,
		ToolFilter:       toolFilter,
	}

	resp, err := client.SendAgentic(ctx, fullPrompt, opts)
	if err != nil {
		log.Printf("[anthropic] callDirect error: %v", err)
		return "", 0, fmt.Errorf("anthropicclient agentic call: %w", err)
	}

	tokensUsed := resp.InputTokens + resp.OutputTokens
	log.Printf("[anthropic] callDirect success model=%s input=%d output=%d tools=%d stop=%s compacted=%v", resp.Model, resp.InputTokens, resp.OutputTokens, len(resp.ToolCalls), resp.StopReason, resp.Compacted)
	if resp.StopReason == "max_tokens" {
		return resp.Text, tokensUsed, errMaxTokens
	}
	return resp.Text, tokensUsed, nil
}

// callChatStreaming calls the Anthropic API with streaming for chat/followup.
func (a *Adapter) callChatStreaming(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, isTaskFollowup bool, chatMode models.ChatMode, workDir string, extraTools []anthropicclient.ToolDefinition, toolExecutor func(context.Context, string, json.RawMessage) (string, bool, error), toolFilter func(string) bool) (string, int, error) {
	log.Printf("[anthropic] callChatStreaming model=%s history=%d exec=%s isTaskFollowup=%v workDir=%s attachments=%d", agent.Model, len(chatHistory), execID, isTaskFollowup, workDir, len(attachments))

	client, err := a.getClient(ctx, agent)
	if err != nil {
		return "", 0, err
	}

	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	systemPromptStr := llmprompt.BuildChatSystemPrompt(isTaskFollowup, chatMode, chatSystemContext, false)
	systemPromptStr = llmprompt.AppendWorktreeContextPrompt(systemPromptStr, workDir)
	systemPromptStr = appendToolModeSystemPrompt(systemPromptStr, rt, isTaskFollowup, chatMode)
	client.History = append(client.History, buildClientHistory(chatHistory)...)

	mcAttachments, err := convertAttachments(attachments)
	if err != nil {
		return "", 0, fmt.Errorf("convert attachments: %w", err)
	}

	sw := llmstream.NewWriter(execID, "", a.execRepo, ctx, 500*time.Millisecond)
	defer sw.Stop()

	extraTools = append(extraTools, runtimeAnthropicTools(rt)...)
	toolExecutor = composeRuntimeToolExecutor(toolExecutor, rt)
	toolFilter = composeRuntimeToolFilter(toolFilter, rt, isTaskFollowup, chatMode)
	skipDefaultTools := shouldSkipDefaultToolsForChatMode(isTaskFollowup, chatMode, rt)
	disableTools := !isTaskFollowup && chatMode != models.ChatModePlan && rt == nil
	preferProviderWeb := shouldPreferAnthropicProviderWeb(message)
	if preferProviderWeb {
		// URL prompts should use provider web tools and avoid local defaults.
		disableTools = false
		skipDefaultTools = true
	}
	chatInThinking := false
	opts := &anthropicclient.AgenticOptions{
		Model:            agent.Model,
		MaxTokens:        agenticMaxTokens(agent.MaxTokens),
		EnableThinking:   true,
		DisableTools:     disableTools,
		SkipDefaultTools: skipDefaultTools,
		System:           systemPromptStr,
		WorkDir:          workDir,
		Attachments:      mcAttachments,
		AutoCompaction:   true,
		WebSearchEnabled: true,
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
			log.Printf("[anthropic] callChatStreaming context compacted, summary_len=%d", len(summary))
		},
	}

	resp, err := client.SendAgentic(ctx, message, opts)
	if err != nil {
		sw.Flush()
		log.Printf("[anthropic] callChatStreaming error: %v", err)
		return "", 0, fmt.Errorf("anthropicclient agentic chat streaming call: %w", err)
	}

	sw.Flush()

	output := sw.String()
	tokensUsed := resp.InputTokens + resp.OutputTokens
	log.Printf("[anthropic] callChatStreaming success output_len=%d tokens=%d tools=%d stop=%s compacted=%v", len(output), tokensUsed, len(resp.ToolCalls), resp.StopReason, resp.Compacted)
	if resp.StopReason == "max_tokens" {
		return output, tokensUsed, errMaxTokens
	}
	return output, tokensUsed, nil
}

// callStreaming calls the Anthropic API with streaming.
func (a *Adapter) callStreaming(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string, projectInstructions string, extraTools []anthropicclient.ToolDefinition, toolExecutor func(context.Context, string, json.RawMessage) (string, bool, error), toolFilter func(string) bool) (string, string, int, error) {
	log.Printf("[anthropic] callStreaming model=%s max_tokens=%d exec=%s workDir=%s attachments=%d", agent.Model, agent.MaxTokens, execID, workDir, len(attachments))

	client, err := a.getClient(ctx, agent)
	if err != nil {
		return "", "", 0, err
	}

	fullPrompt := llmprompt.BuildTaskPromptHeader() + prompt
	preferProviderWeb := shouldPreferAnthropicProviderWeb(prompt)

	mcAttachments, err := convertAttachments(attachments)
	if err != nil {
		return "", "", 0, fmt.Errorf("convert attachments: %w", err)
	}

	sw := llmstream.NewWriter(execID, "", a.execRepo, ctx, 500*time.Millisecond)
	defer sw.Stop()

	inThinking := false
	opts := &anthropicclient.AgenticOptions{
		Model:            agent.Model,
		MaxTokens:        agenticMaxTokens(agent.MaxTokens),
		EnableThinking:   true,
		SkipDefaultTools: preferProviderWeb,
		System:           llmprompt.BuildAgentSystemPrompt(projectInstructions, workDir),
		WorkDir:          workDir,
		Attachments:      mcAttachments,
		AutoCompaction:   true,
		WebSearchEnabled: true,
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
			log.Printf("[anthropic] callStreaming context compacted, summary_len=%d", len(summary))
		},
	}

	resp, err := client.SendAgentic(ctx, fullPrompt, opts)
	if err != nil {
		sw.Flush()
		log.Printf("[anthropic] callStreaming error: %v", err)
		return "", "", 0, fmt.Errorf("anthropicclient agentic streaming call: %w", err)
	}

	sw.Flush()

	output := sw.String()
	textOnly := sw.TextString()
	tokensUsed := resp.InputTokens + resp.OutputTokens
	log.Printf("[anthropic] callStreaming success output_len=%d tokens=%d tools=%d stop=%s compacted=%v", len(output), tokensUsed, len(resp.ToolCalls), resp.StopReason, resp.Compacted)
	if resp.StopReason == "max_tokens" {
		return output, textOnly, tokensUsed, errMaxTokens
	}
	return output, textOnly, tokensUsed, nil
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

	client := anthropicclient.NewWithOAuthToken(agent.OAuthAccessToken, agent.OAuthRefreshToken, agent.OAuthExpiresAt)

	if err := client.EnsureValidToken(); err != nil {
		log.Printf("[anthropic] getClient token refresh failed for agent=%s: %v", agent.Name, err)
		return nil, fmt.Errorf("OAuth token refresh failed for %q: %w", agent.Name, err)
	}

	auth := client.Auth()
	if auth.Token != agent.OAuthAccessToken {
		log.Printf("[anthropic] getClient token refreshed for agent=%s, persisting to DB", agent.Name)
		if err := a.llmConfigRepo.UpdateOAuthTokens(ctx, agent.ID, auth.Token, auth.RefreshToken, auth.ExpiresAt); err != nil {
			log.Printf("[anthropic] getClient failed to persist refreshed tokens for agent=%s: %v", agent.Name, err)
		}
	}

	return client, nil
}

// buildClientHistory converts chat execution history to anthropicclient.Message slices.
func buildClientHistory(chatHistory []models.Execution) []anthropicclient.Message {
	history := llmprompt.LimitChatHistory(chatHistory)
	var messages []anthropicclient.Message
	for _, exec := range history {
		if exec.PromptSent != "" {
			messages = appendMergedMessage(messages, "user", exec.PromptSent)
		}
		if exec.Output != "" && (exec.Status == models.ExecCompleted || exec.Status == models.ExecFailed) {
			messages = appendMergedMessage(messages, "assistant", exec.Output)
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
			log.Printf("[anthropic] convertAttachments error loading %s: %v", att.FilePath, err)
			return nil, fmt.Errorf("load attachment %s: %w", att.FileName, err)
		}
		result = append(result, mcAtt)
	}
	return result, nil
}

// agenticMaxTokens returns the configured max_tokens with a floor for agentic work.
func agenticMaxTokens(configured int) int {
	if configured < minAgenticMaxTokens {
		return minAgenticMaxTokens
	}
	return configured
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

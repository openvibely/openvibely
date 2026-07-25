package openai_compatible

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	llmattachment "github.com/openvibely/openvibely/internal/llm/attachment"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	llmstream "github.com/openvibely/openvibely/internal/llm/stream"
	llmusage "github.com/openvibely/openvibely/internal/llm/usage"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

const defaultOutputBudget = 16384

var errMaxTokens = fmt.Errorf("response truncated: max output tokens limit reached (output budget exhausted before task completed)")

type Adapter struct {
	execRepo  *repository.ExecutionRepo
	streamHub llmstream.ExecutionStreamPublisher
}

func New(execRepo *repository.ExecutionRepo, streamHub llmstream.ExecutionStreamPublisher) *Adapter {
	return &Adapter{execRepo: execRepo, streamHub: streamHub}
}

func (a *Adapter) Call(ctx context.Context, req llmcontracts.AgentRequest, workDir string) (llmcontracts.AgentResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if req.Agent.GetTransport() != "chat_completions" {
		return llmcontracts.AgentResult{}, fmt.Errorf("openai-compatible model %q uses unsupported transport %q", req.Agent.Name, req.Agent.GetTransport())
	}
	if strings.TrimSpace(req.Agent.BaseURL) == "" {
		return llmcontracts.AgentResult{}, fmt.Errorf("OpenAI-compatible base URL not configured for model %q", req.Agent.Name)
	}

	switch req.Operation {
	case llmcontracts.OperationDirect:
		output, usage, err := a.callDirect(ctx, req, workDir)
		return canonicalResult(output, output, usage, err)
	case llmcontracts.OperationStreaming, llmcontracts.OperationTask:
		if requestUsesChatStreaming(req) {
			output, usage, err := a.callChatStreaming(ctx, req, workDir)
			return canonicalResult(output, output, usage, err)
		}
		output, textOnly, usage, err := a.callTaskStreaming(ctx, req, workDir)
		return canonicalResult(output, textOnly, usage, err)
	default:
		return llmcontracts.AgentResult{}, fmt.Errorf("unsupported operation: %s", req.Operation)
	}
}

func (a *Adapter) client(agent models.LLMConfig) (*openaiclient.Client, error) {
	if agent.AuthMethod != models.AuthMethodAPIKey {
		return nil, fmt.Errorf("OpenAI-compatible model %q is configured with auth_method=%q; expected api_key", agent.Name, agent.AuthMethod)
	}
	return openaiclient.NewWithCompatibleAPIKey(strings.TrimSpace(agent.APIKey), agent.BaseURL, agent.GetAuthHeaderName(), agent.GetAuthHeaderValuePrefix()), nil
}

func (a *Adapter) callDirect(ctx context.Context, req llmcontracts.AgentRequest, workDir string) (string, llmcontracts.Usage, error) {
	client, err := a.client(req.Agent)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}
	extraHeaders, extraBody, err := compatibleRequestExtras(req.Agent)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}
	prompt := appendAttachmentSummary(req.Message, req.Attachments)
	attachments, err := convertAttachments(req.Attachments)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}
	systemPrompt := ""
	if !req.RawDirectPrompt {
		systemPrompt = llmprompt.BuildAgentSystemPrompt("", effectiveWorkDir(workDir))
	}
	resp, err := client.SendCompletions(ctx, prompt, &openaiclient.CompletionsOptions{
		Model:            strings.TrimSpace(req.Agent.Model),
		MaxOutputTokens:  req.Agent.GetDefaultMaxTokens(defaultOutputBudget),
		Temperature:      compatibleTemperature(req.Agent),
		System:           systemPrompt,
		WorkDir:          effectiveWorkDir(workDir),
		DisableTools:     req.DisableTools,
		SkipDefaultTools: runtimeSkipDefaultTools(ctx),
		Attachments:      attachments,
		ExtraTools:       runtimeTools(ctx),
		ExtraHeaders:     extraHeaders,
		ExtraBody:        extraBody,
		ToolExecutor:     toolExecutor(ctx, workDir),
		ToolFilter:       toolFilter(ctx, true, models.ChatModeOrchestrate),
	})
	if err != nil {
		return "", llmusage.FromTotal(0), fmt.Errorf("openai-compatible chat completions: %w", err)
	}
	return resp.Text, usageFromResponse(resp), stopError(resp.StopReason)
}

func (a *Adapter) callTaskStreaming(ctx context.Context, req llmcontracts.AgentRequest, workDir string) (string, string, llmcontracts.Usage, error) {
	client, err := a.client(req.Agent)
	if err != nil {
		return "", "", llmusage.FromTotal(0), err
	}
	extraHeaders, extraBody, err := compatibleRequestExtras(req.Agent)
	if err != nil {
		return "", "", llmusage.FromTotal(0), err
	}
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	fullPrompt := llmprompt.BuildTaskPromptHeader() +
		llmprompt.BuildAttachmentInstructions(req.Attachments) +
		req.Message
	fullPrompt = llmprompt.ApplyTaskCreationToolMode(fullPrompt, runtimeToolNames(rt))
	fullPrompt += "\n\n---\nRESPONSE FORMAT REQUIREMENT: You MUST end your final response with exactly one of these status lines:\n" +
		"- If the task completed successfully: [STATUS: SUCCESS]\n" +
		"- If a command failed, a script returned non-zero, or the task could not be completed: [STATUS: FAILED | <describe what went wrong>]\n" +
		"- If the task completed but something needs human attention: [STATUS: NEEDS_FOLLOWUP | <describe what needs attention>]\n" +
		"Example: [STATUS: FAILED | fail.sh returned exit code 1]\n" +
		"Example: [STATUS: NEEDS_FOLLOWUP | tests pass but 3 warnings need review]\n" +
		"Replace <describe what went wrong> or <describe what needs attention> with your actual description.\n" +
		"This status line is MANDATORY. Always include it as the very last line of your response."

	attachments, err := convertAttachments(req.Attachments)
	if err != nil {
		return "", "", llmusage.FromTotal(0), err
	}

	sw := llmstream.NewWriterWithPublisher(req.ExecID, "", a.execRepo, ctx, 500*time.Millisecond, a.streamHub)
	defer sw.Stop()

	resp, err := client.SendCompletions(ctx, fullPrompt, &openaiclient.CompletionsOptions{
		Model:            strings.TrimSpace(req.Agent.Model),
		MaxOutputTokens:  req.Agent.GetDefaultMaxTokens(defaultOutputBudget),
		Temperature:      compatibleTemperature(req.Agent),
		System:           llmprompt.BuildAgentSystemPrompt(req.ProjectInstructions, effectiveWorkDir(workDir)),
		WorkDir:          effectiveWorkDir(workDir),
		DisableTools:     req.DisableTools,
		SkipDefaultTools: runtimeSkipDefaultTools(ctx),
		Attachments:      attachments,
		ExtraTools:       runtimeTools(ctx),
		ExtraHeaders:     extraHeaders,
		ExtraBody:        extraBody,
		ToolExecutor:     toolExecutor(ctx, workDir),
		ToolFilter:       toolFilter(ctx, true, models.ChatModeOrchestrate),
		OnText:           streamText(sw),
		OnToolUse:        streamToolUse(sw),
		OnToolResult:     streamToolResult(sw),
	})
	if err != nil {
		sw.Flush()
		return "", "", llmusage.FromTotal(0), fmt.Errorf("openai-compatible chat completions: %w", err)
	}
	sw.Flush()
	return sw.String(), sw.TextString(), usageFromResponse(resp), stopError(resp.StopReason)
}

func (a *Adapter) callChatStreaming(ctx context.Context, req llmcontracts.AgentRequest, workDir string) (string, llmcontracts.Usage, error) {
	client, err := a.client(req.Agent)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}
	extraHeaders, extraBody, err := compatibleRequestExtras(req.Agent)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}
	client.History = append(client.History, buildClientHistory(req.ChatHistory)...)
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	systemPrompt := llmprompt.BuildChatSystemPrompt(req.Followup, req.ChatMode, req.ChatSystemContext, false)
	if req.ChatMode == models.ChatModeOrchestrate {
		systemPrompt = llmprompt.ApplyChatActionToolMode(systemPrompt, runtimeToolNames(rt))
	}
	systemPrompt = llmprompt.AppendWorktreeContextPrompt(systemPrompt, workDir)

	attachments, err := convertAttachments(req.Attachments)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}

	sw := llmstream.NewWriterWithPublisher(req.ExecID, "", a.execRepo, ctx, 500*time.Millisecond, a.streamHub)
	defer sw.Stop()

	disableTools := req.DisableTools || (!req.Followup && req.ChatMode != models.ChatModePlan && llmcontracts.RuntimeToolsFromContext(ctx) == nil)
	resp, err := client.SendCompletions(ctx, req.Message, &openaiclient.CompletionsOptions{
		Model:            strings.TrimSpace(req.Agent.Model),
		MaxOutputTokens:  req.Agent.GetDefaultMaxTokens(defaultOutputBudget),
		Temperature:      compatibleTemperature(req.Agent),
		System:           systemPrompt,
		WorkDir:          effectiveWorkDir(workDir),
		DisableTools:     disableTools,
		SkipDefaultTools: chatSkipDefaultTools(ctx, req.Followup, req.ChatMode),
		Attachments:      attachments,
		ExtraTools:       runtimeTools(ctx),
		ExtraHeaders:     extraHeaders,
		ExtraBody:        extraBody,
		ToolExecutor:     toolExecutor(ctx, workDir),
		ToolFilter:       toolFilter(ctx, req.Followup, req.ChatMode),
		OnText:           streamText(sw),
		OnToolUse:        streamToolUse(sw),
		OnToolResult:     streamToolResult(sw),
	})
	if err != nil {
		sw.Flush()
		return "", llmusage.FromTotal(0), fmt.Errorf("openai-compatible chat completions: %w", err)
	}
	sw.Flush()
	return sw.String(), usageFromResponse(resp), stopError(resp.StopReason)
}

func compatibleTemperature(agent models.LLMConfig) float64 {
	// Moonshot's Kimi models use model-defined fixed temperatures. Omitting the
	// field lets each Kimi variant select its required value.
	if strings.EqualFold(strings.TrimSpace(agent.PresetSlug), "moonshot") &&
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(agent.Model)), "kimi-") {
		return openaiclient.OmittedTemperature()
	}
	return agent.Temperature
}

func compatibleRequestExtras(agent models.LLMConfig) (map[string]string, map[string]interface{}, error) {
	headers, err := parseStringMapJSON(agent.ExtraHeadersJSON)
	if err != nil {
		return nil, nil, fmt.Errorf("parse extra headers JSON: %w", err)
	}
	body, err := parseObjectJSON(agent.ExtraBodyJSON)
	if err != nil {
		return nil, nil, fmt.Errorf("parse extra body JSON: %w", err)
	}
	return headers, body, nil
}

func parseStringMapJSON(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func parseObjectJSON(raw string) (map[string]interface{}, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func runtimeToolNames(rt *llmcontracts.RuntimeTools) []string {
	if rt == nil {
		return nil
	}
	var names []string
	for _, def := range rt.Definitions {
		if name := strings.TrimSpace(def.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func runtimeTools(ctx context.Context) []openaiclient.ToolDefinition {
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	if rt == nil || len(rt.Definitions) == 0 {
		return nil
	}
	out := make([]openaiclient.ToolDefinition, 0, len(rt.Definitions))
	for _, def := range rt.Definitions {
		name := strings.TrimSpace(def.Name)
		if name == "" {
			continue
		}
		out = append(out, openaiclient.ToolDefinition{Type: "function", Name: name, Description: strings.TrimSpace(def.Description), Parameters: def.Parameters})
	}
	return out
}

func toolExecutor(ctx context.Context, workDir string) func(context.Context, string, json.RawMessage) (string, bool, error) {
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	return func(execCtx context.Context, name string, input json.RawMessage) (string, bool, error) {
		if rt != nil && rt.Executor != nil {
			if output, handled, isError, err := rt.Executor(execCtx, name, input); handled || err != nil {
				return output, isError, err
			}
		}
		out, err := openaiclient.ExecuteTool(execCtx, effectiveWorkDir(workDir), name, input)
		return out, err != nil, err
	}
}

func runtimeSkipDefaultTools(ctx context.Context) bool {
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	return rt != nil && rt.SkipDefaultTools
}

func chatSkipDefaultTools(ctx context.Context, isTaskFollowup bool, chatMode models.ChatMode) bool {
	if isTaskFollowup || chatMode == models.ChatModePlan {
		return runtimeSkipDefaultTools(ctx)
	}
	return true
}

func toolFilter(ctx context.Context, isTaskFollowup bool, chatMode models.ChatMode) func(string) bool {
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	return func(name string) bool {
		isRuntimeTool, access := runtimeToolAccess(rt, name)
		if !isTaskFollowup {
			switch chatMode {
			case models.ChatModePlan:
				if isRuntimeTool {
					if access != llmcontracts.RuntimeToolAccessRead {
						return false
					}
				} else if !planModeAllowsReadOnlyTool(name) {
					return false
				}
			default:
				if !isRuntimeTool {
					return false
				}
			}
		}
		if isRuntimeTool {
			if rt != nil && rt.Filter != nil {
				allow, handled := rt.Filter(name)
				if handled {
					return allow
				}
			}
			return true
		}
		if rt != nil && rt.SkipDefaultTools {
			return false
		}
		return true
	}
}

func runtimeToolAccess(rt *llmcontracts.RuntimeTools, name string) (bool, llmcontracts.RuntimeToolAccess) {
	if rt == nil {
		return false, ""
	}
	for _, def := range rt.Definitions {
		if strings.EqualFold(def.Name, name) {
			access := def.Access
			if access == "" {
				access = llmcontracts.RuntimeToolAccessWrite
			}
			return true, access
		}
	}
	return false, ""
}

func planModeAllowsReadOnlyTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read_file", "list_files", "grep_search", "web_search", "web_search_preview":
		return true
	default:
		return false
	}
}

func buildClientHistory(chatHistory []models.Execution) []openaiclient.Message {
	history := llmprompt.LimitChatHistory(chatHistory)
	messages := make([]openaiclient.Message, 0, len(history)*2)
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
	out := make([]*openaiclient.FileAttachment, 0, len(prepared))
	for _, att := range prepared {
		oaAtt, err := openaiclient.NewFileAttachment(att.FilePath)
		if err != nil {
			if _, ok := err.(*openaiclient.UnsupportedFileTypeError); ok {
				applog.Infof("[openai-compatible-adapter] skipping unsupported attachment %s: %v", att.FileName, err)
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
		out = append(out, oaAtt)
	}
	return out, nil
}

func appendAttachmentSummary(prompt string, attachments []models.Attachment) string {
	if len(attachments) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\nAttached files:\n")
	for _, att := range attachments {
		b.WriteString(fmt.Sprintf("- %s (absolute path: %s)\n", att.FileName, llmprompt.AttachmentAbsPath(att)))
	}
	return b.String()
}

func streamText(sw *llmstream.Writer) func(string) {
	return func(text string) {
		if isStreamingMarkerChunk(text) {
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventRawOutput, Text: text}, false)
			return
		}
		llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventTextDelta, Text: text}, false)
	}
}

func streamToolUse(sw *llmstream.Writer) func(string, json.RawMessage) {
	return func(name string, input json.RawMessage) {
		llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventToolUse, ToolName: name, Secondary: toolSecondaryInfo(name, input)}, false)
	}
}

func streamToolResult(sw *llmstream.Writer) func(string, string, bool) {
	return func(name string, output string, isError bool) {
		llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventToolResult, ToolName: name, Output: output, IsError: isError}, false)
	}
}

func usageFromResponse(resp *openaiclient.AgenticResponse) llmcontracts.Usage {
	if resp == nil {
		return llmusage.FromTotal(0)
	}
	return llmusage.FromOpenAIWithTotal(resp.InputTokens, resp.OutputTokens, resp.CachedInputTokens, resp.ReasoningTokens, resp.TotalTokens)
}

func canonicalResult(output, textOnly string, usage llmcontracts.Usage, err error) (llmcontracts.AgentResult, error) {
	if textOnly == "" {
		textOnly = output
	}
	res := llmcontracts.AgentResult{Output: output, TextOnlyOutput: textOnly, Usage: usage}
	if err != nil && strings.HasPrefix(err.Error(), "response truncated: max") {
		res.StopReason = "max_tokens"
	}
	return res, err
}

func stopError(reason string) error {
	if reason == "length" || reason == "max_output_tokens" {
		return errMaxTokens
	}
	return nil
}

func requestUsesChatStreaming(req llmcontracts.AgentRequest) bool {
	if req.Operation != llmcontracts.OperationStreaming {
		return false
	}
	if req.Followup {
		return true
	}
	mode := strings.TrimSpace(string(req.ChatMode))
	return mode == string(models.ChatModeOrchestrate) || mode == string(models.ChatModePlan)
}

func effectiveWorkDir(workDir string) string {
	if strings.TrimSpace(workDir) == "" {
		return "."
	}
	return workDir
}

func isStreamingMarkerChunk(text string) bool {
	return strings.Contains(text, "[Using tool:") || strings.Contains(text, "[Tool ") || strings.Contains(text, "[/Tool]")
}

func toolSecondaryInfo(name string, input json.RawMessage) string {
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	get := func(key string) string {
		v, _ := m[key].(string)
		return strings.TrimSpace(v)
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read_file", "write_file", "edit_file":
		return get("file_path")
	case "bash":
		return get("command")
	case "grep_search":
		return get("pattern")
	case "list_files":
		if path := get("path"); path != "" {
			return path
		}
		return get("pattern")
	default:
		if command := get("command"); command != "" {
			return command
		}
		return get("file_path")
	}
}

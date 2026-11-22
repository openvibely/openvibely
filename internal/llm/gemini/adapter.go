package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	llmattachment "github.com/openvibely/openvibely/internal/llm/attachment"
	"github.com/openvibely/openvibely/internal/llm/contextlimits"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/llm/jsonview"
	llmoauth "github.com/openvibely/openvibely/internal/llm/oauth"
	"github.com/openvibely/openvibely/internal/llm/preparedhistory"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/llm/recovery"
	llmstream "github.com/openvibely/openvibely/internal/llm/stream"
	llmusage "github.com/openvibely/openvibely/internal/llm/usage"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	conversation "github.com/openvibely/openvibely/internal/transcript"
	geminiclient "github.com/openvibely/openvibely/pkg/gemini_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

const ReplayItemsKey = "contents"

var errMaxTokens = llmcontracts.NewCategorizedError(
	llmcontracts.ErrorOutputTokenLimitReached,
	"Gemini response",
	fmt.Errorf("response truncated: max output tokens limit reached (output budget exhausted before task completed)"),
)

type Adapter struct {
	configRepo    persistence.LLMConfigStore
	execRepo      persistence.ExecutionStore
	streamHub     llmstream.ExecutionStreamPublisher
	oauthRecovery *llmoauth.Manager
	configure     func(*geminiclient.Client)
}

func New(configRepo persistence.LLMConfigStore, execRepo persistence.ExecutionStore, streamHub llmstream.ExecutionStreamPublisher) *Adapter {
	return &Adapter{configRepo: configRepo, execRepo: execRepo, streamHub: streamHub, oauthRecovery: llmoauth.NewManager(configRepo)}
}

// SetClientConfigurer lets tests point the client at a local server.
func (a *Adapter) SetClientConfigurer(fn func(*geminiclient.Client)) { a.configure = fn }

type conversationRecorderKey struct{}

func (a *Adapter) Call(ctx context.Context, req llmcontracts.AgentRequest, workDir string) (llmcontracts.AgentResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := models.LookupModel(models.ProviderGemini, strings.TrimSpace(req.Agent.Model)); !ok {
		return llmcontracts.AgentResult{}, fmt.Errorf("unsupported Gemini model %q", req.Agent.Model)
	}
	if a.execRepo != nil && req.ExecID != "" {
		recorder, err := conversation.OpenExchangeRecorder(ctx, a.execRepo, models.ConversationOwner{ExecutionID: req.ExecID}, llmcontracts.HistoryContinuationFromContext(ctx))
		if err != nil {
			return llmcontracts.AgentResult{}, err
		}
		ctx = context.WithValue(ctx, conversationRecorderKey{}, recorder)
	}

	switch req.Operation {
	case llmcontracts.OperationDirect:
		output, usage, err := a.callDirect(ctx, req, workDir)
		return canonicalResult(output, output, usage, err)
	case llmcontracts.OperationStreaming, llmcontracts.OperationTask:
		output, textOnly, usage, err := a.callStreaming(ctx, req, workDir, requestUsesChatStreaming(req))
		return canonicalResult(output, textOnly, usage, err)
	default:
		return llmcontracts.AgentResult{}, fmt.Errorf("unsupported operation: %s", req.Operation)
	}
}

func (a *Adapter) client(ctx context.Context, agent models.LLMConfig) (*geminiclient.Client, error) {
	var client *geminiclient.Client
	switch agent.AuthMethod {
	case models.AuthMethodAPIKey, "":
		key := strings.TrimSpace(agent.APIKey)
		if key == "" {
			return nil, fmt.Errorf("Gemini API key not configured for model %q", agent.Name)
		}
		client = geminiclient.NewWithAPIKey(key)
	case models.AuthMethodOAuth:
		if strings.TrimSpace(agent.OAuthAccessToken) == "" {
			return nil, fmt.Errorf("Gemini model %q is not connected; sign in with Google on the Models page", agent.Name)
		}
		fresh, err := a.oauthRecovery.EnsureFresh(ctx, agent, 5*time.Minute, llmoauth.GeminiRefreshFunc())
		if err != nil {
			return nil, err
		}
		agent = fresh
		legacy := geminiclient.UsesLegacyOAuth(agent.OAuthRefreshToken)
		if legacy && strings.HasPrefix(agent.Model, "gemini-4-") {
			return nil, fmt.Errorf("reconnect this Google account on the Models page using Google sign-in before using Gemini 4")
		}
		client = geminiclient.NewWithOAuth(agent.OAuthAccessToken, agent.OAuthAccountID)
		client.SetLegacyOAuth(legacy)
		client.SetUnauthorizedHandler(func(refreshCtx context.Context, tokenUsed string) (string, bool, error) {
			recovered, ok, err := a.oauthRecovery.RecoverUnauthorized(refreshCtx, agent, tokenUsed, llmoauth.GeminiRefreshFunc())
			if err != nil || !ok {
				return "", ok, err
			}
			agent = recovered
			return recovered.OAuthAccessToken, true, nil
		})
	default:
		return nil, fmt.Errorf("Gemini model %q uses unsupported auth_method=%q", agent.Name, agent.AuthMethod)
	}
	if a.configure != nil {
		a.configure(client)
	}
	return client, nil
}

func (a *Adapter) callDirect(ctx context.Context, req llmcontracts.AgentRequest, workDir string) (string, llmcontracts.Usage, error) {
	client, err := a.client(ctx, req.Agent)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}
	if llmcontracts.HistoryContinuationFromContext(ctx) {
		client.SetHistory(a.prepareHistory(ctx, req.ChatHistory))
	}
	if req.SummaryInput != nil {
		history, err := decodeSummaryInput(req.SummaryInput)
		if err != nil {
			return "", llmusage.FromTotal(0), fmt.Errorf("decode summary history: %w", err)
		}
		client.SetHistory(history)
	}
	attachments, err := convertAttachments(ctx, req.Attachments, req.Agent.Model)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}
	systemPrompt := ""
	switch {
	case req.RawDirectPrompt:
	case req.LifecycleHookCall:
		systemPrompt = req.ProjectInstructions
	default:
		systemPrompt = llmprompt.BuildAgentSystemPrompt(req.ProjectInstructions, effectiveWorkDir(workDir))
	}
	if preserved, ok := llmcontracts.SummarySystemPromptFromContext(ctx); ok {
		systemPrompt = preserved
	}
	opts := a.baseOptions(ctx, req, workDir)
	opts.System = systemPrompt
	opts.OmitExecutionEnvironment = req.RawDirectPrompt || req.LifecycleHookCall
	opts.DisableTools = req.DisableTools
	opts.SkipDefaultTools = taskSkipDefaultTools(ctx, req.AgentDefinition)
	opts.ToolFilter = toolFilter(ctx, true, models.ChatModeOrchestrate, req.AgentDefinition)
	opts.Attachments = attachments
	opts.OnText = llmcontracts.TextStreamFromContext(ctx)
	resp, err := client.Send(ctx, appendAttachmentSummary(req.Message, req.Attachments), opts)
	err = a.persistReplay(ctx, req.ExecID, client, err)
	if err != nil {
		err = recovery.WithSnapshot(err, ReplayItemsKey, client.History(), func(c geminiclient.Content) ([]byte, error) { return json.Marshal(c) })
	}
	if err != nil {
		return "", llmusage.FromTotal(0), fmt.Errorf("gemini generate content: %w", err)
	}
	return resp.Text, usageFromResponse(resp), stopError(resp.StopReason)
}

func (a *Adapter) callStreaming(ctx context.Context, req llmcontracts.AgentRequest, workDir string, chat bool) (string, string, llmcontracts.Usage, error) {
	client, err := a.client(ctx, req.Agent)
	if err != nil {
		return "", "", llmusage.FromTotal(0), err
	}
	if chat || llmcontracts.HistoryContinuationFromContext(ctx) {
		client.SetHistory(a.prepareHistory(ctx, req.ChatHistory))
	}
	attachments, err := convertAttachments(ctx, req.Attachments, req.Agent.Model)
	if err != nil {
		return "", "", llmusage.FromTotal(0), err
	}

	opts := a.baseOptions(ctx, req, workDir)
	opts.Attachments = attachments
	prompt := req.Message
	if chat {
		rt := llmcontracts.RuntimeToolsFromContext(ctx)
		opts.System = llmprompt.BuildChatSystemPrompt(req.Followup, req.ChatMode, req.ChatSystemContext, false)
		if req.ChatMode == models.ChatModeOrchestrate && !req.Followup {
			opts.System = llmprompt.ApplyChatActionToolMode(opts.System, rt.DefinitionNames())
		}
		opts.DisableTools = req.DisableTools || (!req.Followup && req.ChatMode != models.ChatModePlan && rt == nil)
		opts.SkipDefaultTools = chatSkipDefaultTools(ctx, req.Followup, req.ChatMode, req.AgentDefinition)
		opts.ToolFilter = toolFilter(ctx, req.Followup, req.ChatMode, req.AgentDefinition)
	} else {
		opts.System = llmprompt.BuildAgentSystemPrompt(req.ProjectInstructions, effectiveWorkDir(workDir)) + llmprompt.BuildTaskStatusInstructions()
		opts.OmitExecutionEnvironment = req.RawDirectPrompt || req.LifecycleHookCall
		opts.DisableTools = req.DisableTools
		opts.SkipDefaultTools = taskSkipDefaultTools(ctx, req.AgentDefinition)
		opts.ToolFilter = toolFilter(ctx, true, models.ChatModeOrchestrate, req.AgentDefinition)
		prompt = llmprompt.BuildAttachmentInstructions(req.Attachments) + req.Message
	}

	sw := llmstream.NewStructuredWriterWithPublisher(req.ExecID, "", a.execRepo, ctx, 500*time.Millisecond, a.streamHub)
	defer sw.Stop()
	opts.OnText = streamText(sw)
	opts.OnToolUse = streamToolUse(sw)
	opts.OnToolResult = streamToolResult(sw)

	resp, err := client.Send(ctx, prompt, opts)
	err = a.persistReplay(ctx, req.ExecID, client, err)
	if err != nil {
		err = recovery.WithSnapshot(err, ReplayItemsKey, client.History(), func(c geminiclient.Content) ([]byte, error) { return json.Marshal(c) })
	}
	sw.Flush()
	if err != nil {
		err = errors.Join(err, sw.PersistenceError())
		return "", "", llmusage.FromTotal(0), fmt.Errorf("gemini generate content: %w", err)
	}
	output, outputErr := sw.Result()
	if outputErr != nil {
		return "", "", llmcontracts.Usage{}, outputErr
	}
	return output, sw.TextString(), usageFromResponse(resp), stopError(resp.StopReason)
}

func (a *Adapter) baseOptions(ctx context.Context, req llmcontracts.AgentRequest, workDir string) *geminiclient.Options {
	resolved := contextlimits.Resolve(req.Agent)
	opts := &geminiclient.Options{
		Model:                  strings.TrimSpace(req.Agent.Model),
		ReasoningEffort:        models.NormalizeModelEffort(req.Agent.Provider, req.Agent.Model, req.Agent.ReasoningEffort),
		MaxOutputTokens:        resolved.EffectiveOutputCap,
		ContextWindow:          resolved.EffectiveInputWindow,
		WorkDir:                effectiveWorkDir(workDir),
		ContinueFromHistory:    llmcontracts.HistoryContinuationFromContext(ctx),
		ExtraTools:             runtimeTools(ctx),
		ToolExecutor:           toolExecutor(ctx, workDir),
		OnReplayChanged:        a.replayChangesSaver(ctx, req.ExecID),
		OnToolBoundarySteering: llmcontracts.SteeringCallbackFromContext(ctx),
	}
	if models.ModelSupportsTemperature(req.Agent.Provider, req.Agent.Model) {
		temperature := req.Agent.Temperature
		opts.Temperature = &temperature
	}
	return opts
}

func (a *Adapter) recorder(ctx context.Context, execID string) *conversation.ExchangeRecorder {
	if recorder, ok := ctx.Value(conversationRecorderKey{}).(*conversation.ExchangeRecorder); ok {
		return recorder
	}
	if a.execRepo == nil || execID == "" {
		return nil
	}
	return conversation.NewExchangeRecorder(a.execRepo, models.ConversationOwner{ExecutionID: execID})
}

func replayHeader() conversation.ReplayHeader {
	return conversation.ReplayHeader{Format: "envelope", ItemsKey: ReplayItemsKey}
}

func (a *Adapter) persistReplay(ctx context.Context, execID string, client *geminiclient.Client, callErr error) error {
	recorder := a.recorder(ctx, execID)
	if recorder == nil {
		return callErr
	}
	exchange := client.LastExchange()
	if len(exchange) == 0 && !llmcontracts.HistoryContinuationFromContext(ctx) {
		return callErr
	}
	if err := recorder.Save(context.WithoutCancel(ctx), replayHeader(), conversation.Values(exchange)); err != nil && callErr == nil {
		return fmt.Errorf("persist gemini replay: %w", err)
	}
	return callErr
}

func (a *Adapter) replayChangesSaver(ctx context.Context, execID string) func(context.Context, []geminiclient.Content, int) error {
	recorder := a.recorder(ctx, execID)
	if recorder == nil {
		return nil
	}
	return func(saveCtx context.Context, items []geminiclient.Content, firstChanged int) error {
		return recorder.SaveChanged(saveCtx, replayHeader(), conversation.Values(items), firstChanged)
	}
}

// The service hydrates and budgets history first; never reload storage here.
func (a *Adapter) prepareHistory(ctx context.Context, history []models.Execution) []geminiclient.Content {
	prepared := preparedhistory.Take(ctx, history, func(h []models.Execution) [][]geminiclient.Content {
		return [][]geminiclient.Content{BuildHistory(h)}
	})
	if len(prepared) == 0 {
		return nil
	}
	return prepared[0]
}

// DecodeContents decodes raw Gemini content rows.
func DecodeContents(rows []json.RawMessage) ([]geminiclient.Content, error) {
	out := make([]geminiclient.Content, 0, len(rows))
	for _, raw := range rows {
		var c geminiclient.Content
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		if c.Role == "" || len(c.Parts) == 0 {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// Summary rows use the shared Chat Completions projection.
func decodeSummaryInput(rows []json.RawMessage) ([]geminiclient.Content, error) {
	messages := make([]openaiclient.CompletionsHistoryMessage, 0, len(rows))
	for _, raw := range rows {
		m, err := openaiclient.DecodeCompletionsReplay(raw)
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return ContentsFromCompletions(messages), nil
}

func decodeReplay(turn models.ExecutionReplayMessage) ([]geminiclient.Content, bool) {
	if turn.TranscriptJSON == "" && turn.Rows != nil && !(turn.Rows.Format == "envelope" && strings.EqualFold(turn.Rows.ItemsKey, ReplayItemsKey)) {
		return nil, false
	}
	if turn.TranscriptJSON == "" && turn.Rows == nil {
		return nil, false
	}
	var envelope struct {
		Contents []json.RawMessage `json:"contents"`
	}
	if err := turn.DecodeTranscript(&envelope); err != nil || envelope.Contents == nil {
		return nil, false
	}
	contents, err := DecodeContents(envelope.Contents)
	if err != nil {
		return nil, false
	}
	return contents, true
}

// BuildHistory converts executions into Gemini contents, preferring native replay.
func BuildHistory(history []models.Execution) []geminiclient.Content {
	var out []geminiclient.Content
	for _, exec := range history {
		if len(exec.ReplayMessages) > 0 {
			for _, turn := range exec.ReplayMessages {
				if contents, ok := decodeReplay(turn); ok {
					out = append(out, contents...)
					continue
				}
				if messages, err := openaiclient.DecodeCompletionsHistory(turn); err == nil && len(messages) > 0 {
					out = append(out, ContentsFromCompletions(messages)...)
					continue
				}
				if turn.UserContent != "" {
					out = append(out, geminiclient.TextContent("user", turn.UserContent))
				}
				if turn.AssistantContent != "" {
					out = append(out, geminiclient.TextContent("model", turn.AssistantContent))
				}
			}
			continue
		}
		if exec.PromptSent != "" {
			out = append(out, geminiclient.TextContent("user", exec.PromptSent))
		}
		if reply := llmprompt.ReplayAssistantContent(exec); reply != "" {
			out = append(out, geminiclient.TextContent("model", reply))
		}
	}
	return out
}

// SummaryHistoryRows projects history into Gemini content rows for summary calls.
func SummaryHistoryRows(history []models.Execution) ([]json.RawMessage, error) {
	contents := BuildHistory(history)
	rows := make([]json.RawMessage, 0, len(contents))
	for _, c := range contents {
		raw, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		rows = append(rows, raw)
	}
	return rows, nil
}

func runtimeTools(ctx context.Context) []geminiclient.ToolDefinition {
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	if rt == nil || len(rt.Definitions) == 0 {
		return nil
	}
	out := make([]geminiclient.ToolDefinition, 0, len(rt.Definitions))
	for _, def := range rt.Definitions {
		name := strings.TrimSpace(def.Name)
		if name == "" {
			continue
		}
		out = append(out, geminiclient.ToolDefinition{
			Name:        name,
			Description: strings.TrimSpace(def.Description),
			Parameters:  def.Parameters,
			Schema:      def.Schema,
		})
	}
	return out
}

func mapBuiltInToolName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read_file":
		return "Read"
	case "write_file":
		return "Write"
	case "edit_file":
		return "Edit"
	case "bash", "shell", "process":
		return "Bash"
	case "list_files":
		return "Glob"
	case "grep_search":
		return "Grep"
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

func taskSkipDefaultTools(ctx context.Context, agentDef *models.Agent) bool {
	return agentSkipDefaultTools(agentDef) || llmcontracts.RuntimeSkipDefaultTools(llmcontracts.RuntimeToolsFromContext(ctx))
}

func chatSkipDefaultTools(ctx context.Context, isTaskFollowup bool, chatMode models.ChatMode, agentDef *models.Agent) bool {
	if agentSkipDefaultTools(agentDef) {
		return true
	}
	if isTaskFollowup || chatMode == models.ChatModePlan {
		return llmcontracts.RuntimeSkipDefaultTools(llmcontracts.RuntimeToolsFromContext(ctx))
	}
	return true
}

func toolFilter(ctx context.Context, isTaskFollowup bool, chatMode models.ChatMode, agentDef *models.Agent) func(string) bool {
	base := func(name string) bool {
		return agentAllowsBuiltInTool(agentDef, name)
	}
	return llmcontracts.ComposeRuntimeToolFilter(base, llmcontracts.RuntimeToolsFromContext(ctx), llmcontracts.RuntimeToolPolicyOptions{
		IsTaskFollowup:     isTaskFollowup,
		ChatMode:           chatMode,
		AllowsReadOnlyTool: llmcontracts.DefaultPlanModeAllowsReadOnlyTool,
	})
}

func toolExecutor(ctx context.Context, workDir string) func(context.Context, string, json.RawMessage) (string, bool, error) {
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	return func(execCtx context.Context, name string, input json.RawMessage) (string, bool, error) {
		if rt != nil && rt.Executor != nil {
			if output, handled, isError, err := rt.Executor(execCtx, name, input); handled || err != nil {
				return output, isError, err
			}
		}
		out, err := geminiclient.ExecuteTool(execCtx, effectiveWorkDir(workDir), name, input)
		return out, err != nil, err
	}
}

func convertAttachments(ctx context.Context, attachments []models.Attachment, model string) ([]geminiclient.Attachment, error) {
	spec, _ := models.LookupModel(models.ProviderGemini, model)
	inputs, err := llmattachment.ModelInputsWithProfile(ctx, attachments, spec.ImageResizeProfile)
	if err != nil {
		return nil, err
	}
	out := make([]geminiclient.Attachment, 0, len(inputs))
	for _, input := range inputs {
		out = append(out, geminiclient.Attachment{FileName: input.Name, MediaType: input.MediaType, Data: input.Data})
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
		fmt.Fprintf(&b, "- %s (absolute path: %s)\n", att.FileName, llmprompt.AttachmentAbsPath(att))
	}
	return b.String()
}

func streamText(sw *llmstream.Writer) func(string) {
	return func(text string) {
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

func toolSecondaryInfo(name string, input json.RawMessage) string {
	m, err := jsonview.ToolDisplayFields(input)
	if err != nil {
		return ""
	}
	get := func(key string) string {
		v, _ := m[key].(string)
		return strings.TrimSpace(v)
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read_file", "write_file", "edit_file":
		return get("file_path")
	case "bash", "shell":
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

func usageFromResponse(resp *geminiclient.Response) llmcontracts.Usage {
	if resp == nil {
		return llmusage.FromTotal(0)
	}
	usage := llmusage.FromOpenAIWithTotal(resp.InputTokens, resp.OutputTokens, resp.CachedInputTokens, resp.ReasoningTokens, resp.TotalTokens)
	usage.LastContextTokens = resp.LastContextTokens
	return usage
}

func canonicalResult(output, textOnly string, usage llmcontracts.Usage, err error) (llmcontracts.AgentResult, error) {
	res := llmcontracts.AgentResult{Output: output, TextOnlyOutput: textOnly, Usage: usage}
	if errors.Is(err, errMaxTokens) {
		res.StopReason = "max_tokens"
	}
	return res, err
}

func stopError(reason string) error {
	if reason == "MAX_TOKENS" {
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

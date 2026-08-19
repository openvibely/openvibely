package openai_compatible

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	llmattachment "github.com/openvibely/openvibely/internal/llm/attachment"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmcustomauth "github.com/openvibely/openvibely/internal/llm/customauth"
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
	configRepo *repository.LLMConfigRepo
	execRepo   *repository.ExecutionRepo
	streamHub  llmstream.ExecutionStreamPublisher
}

func New(execRepo *repository.ExecutionRepo, streamHub llmstream.ExecutionStreamPublisher) *Adapter {
	return NewWithConfigRepo(nil, execRepo, streamHub)
}

func NewWithConfigRepo(configRepo *repository.LLMConfigRepo, execRepo *repository.ExecutionRepo, streamHub llmstream.ExecutionStreamPublisher) *Adapter {
	return &Adapter{configRepo: configRepo, execRepo: execRepo, streamHub: streamHub}
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

func (a *Adapter) client(ctx context.Context, agent models.LLMConfig) (*openaiclient.Client, func(*http.Request, []byte) error, error) {
	if agent.AuthMethod == models.AuthMethodOAuth {
		current, err := a.currentOAuthAgent(ctx, agent)
		if err != nil {
			return nil, nil, err
		}
		agent = current
	}
	cfg, err := llmcustomauth.ParseConfig(agent.CustomAuthConfigJSON)
	if err != nil {
		return nil, nil, err
	}
	state, err := llmcustomauth.ParseState(agent.CustomAuthStateJSON)
	if err != nil {
		return nil, nil, err
	}
	var client *openaiclient.Client
	switch agent.AuthMethod {
	case models.AuthMethodAPIKey:
		client = openaiclient.NewWithCompatibleAPIKey(strings.TrimSpace(agent.APIKey), agent.BaseURL, agent.GetAuthHeaderName(), agent.GetAuthHeaderValuePrefix())
	case models.AuthMethodOAuth:
		agent, err = a.ensureFreshOAuth(ctx, agent, false, "")
		if err != nil {
			return nil, nil, err
		}
		client = openaiclient.NewWithCompatibleOAuthToken(agent.OAuthAccessToken, agent.OAuthRefreshToken, agent.OAuthExpiresAt, agent.BaseURL)
		client.SetAuthHeader(cfg.AccessTokenHeader, "Bearer ")
		client.SetOAuthUnauthorizedHandler(func(refreshCtx context.Context, tokenUsed string) (openaiclient.OAuthTokens, bool, error) {
			refreshed, refreshErr := a.ensureFreshOAuth(refreshCtx, agent, true, tokenUsed)
			if refreshErr != nil {
				return openaiclient.OAuthTokens{}, false, refreshErr
			}
			return openaiclient.OAuthTokens{
				AccessToken: refreshed.OAuthAccessToken, RefreshToken: refreshed.OAuthRefreshToken, ExpiresAt: refreshed.OAuthExpiresAt,
			}, true, nil
		})
	default:
		return nil, nil, fmt.Errorf("OpenAI-compatible model %q uses unsupported auth_method=%q", agent.Name, agent.AuthMethod)
	}
	requestPrivate := cfg.AllowPrivateEndpoints || presetUsesPrivateEndpoints(agent.PresetSlug)
	if _, err := llmcustomauth.ValidateEndpoint(agent.BaseURL, requestPrivate); err != nil {
		return nil, nil, fmt.Errorf("invalid custom provider base URL: %w", err)
	}
	client.SetHTTPClient(llmcustomauth.NewHTTPClient(10*time.Minute, requestPrivate))
	finalize := func(req *http.Request, body []byte) error {
		if agent.AuthMethod == models.AuthMethodOAuth {
			if err := a.verifyOAuthRevision(req.Context(), agent); err != nil {
				return err
			}
		}
		if !cfg.Enabled {
			return nil
		}
		auth := client.CurrentAuth()
		token := auth.Token
		if token == "" {
			token = auth.APIKey
		}
		return llmcustomauth.PrepareRequest(req, body, cfg, state, token)
	}
	return client, finalize, nil
}

func (a *Adapter) currentOAuthAgent(ctx context.Context, snapshot models.LLMConfig) (models.LLMConfig, error) {
	if a.configRepo == nil {
		return snapshot, fmt.Errorf("custom OAuth persistence is not configured")
	}
	current, err := a.configRepo.GetByID(ctx, snapshot.ID)
	if err != nil {
		return snapshot, err
	}
	if current == nil || current.Provider != models.ProviderOpenAICompatible || current.AuthMethod != models.AuthMethodOAuth {
		return snapshot, fmt.Errorf("custom OAuth model %q is no longer available", snapshot.Name)
	}
	if current.OAuthConfigRevision != snapshot.OAuthConfigRevision {
		return snapshot, fmt.Errorf("custom OAuth configuration changed; reload the model before sending requests")
	}
	return *current, nil
}

func (a *Adapter) verifyOAuthRevision(ctx context.Context, expected models.LLMConfig) error {
	current, err := a.currentOAuthAgent(ctx, expected)
	if err != nil {
		return err
	}
	if current.BaseURL != expected.BaseURL || current.CustomAuthConfigJSON != expected.CustomAuthConfigJSON ||
		current.CustomAuthStateJSON != expected.CustomAuthStateJSON {
		return fmt.Errorf("custom OAuth configuration changed; reload the model before sending requests")
	}
	return nil
}

func presetUsesPrivateEndpoints(preset string) bool {
	switch strings.ToLower(strings.TrimSpace(preset)) {
	case "vllm", "lm_studio", "sglang", "litellm", "inferrs", "ds4":
		return true
	default:
		return false
	}
}

func (a *Adapter) ensureFreshOAuth(ctx context.Context, agent models.LLMConfig, force bool, tokenUsed string) (models.LLMConfig, error) {
	if a.configRepo == nil {
		return agent, fmt.Errorf("custom OAuth persistence is not configured")
	}
	if !force && agent.OAuthAccessToken != "" &&
		(agent.OAuthExpiresAt == 0 || agent.OAuthExpiresAt > time.Now().Add(5*time.Minute).UnixMilli()) {
		return agent, nil
	}
	tokens, err := llmcustomauth.CoordinatedRefreshDistributed(
		ctx,
		string(agent.Provider)+":"+agent.ID,
		a.configRepo,
		agent.ID,
		func() (llmcustomauth.TokenSet, bool, error) {
			current, loadErr := a.configRepo.GetByID(ctx, agent.ID)
			if loadErr != nil {
				return llmcustomauth.TokenSet{}, false, loadErr
			}
			if current == nil {
				return llmcustomauth.TokenSet{}, false, fmt.Errorf("custom OAuth model %q no longer exists", agent.Name)
			}
			if tokenUsed != "" && current.OAuthAccessToken != "" && current.OAuthAccessToken != tokenUsed {
				return customTokenSet(*current), true, nil
			}
			if !force && current.OAuthAccessToken != "" &&
				(current.OAuthExpiresAt == 0 || current.OAuthExpiresAt > time.Now().Add(5*time.Minute).UnixMilli()) {
				return customTokenSet(*current), true, nil
			}
			return llmcustomauth.TokenSet{}, false, nil
		},
		func() (llmcustomauth.TokenSet, error) {
			current, loadErr := a.configRepo.GetByID(ctx, agent.ID)
			if loadErr != nil {
				return llmcustomauth.TokenSet{}, loadErr
			}
			if current == nil {
				return llmcustomauth.TokenSet{}, fmt.Errorf("custom OAuth model %q no longer exists", agent.Name)
			}
			agent = *current
			if tokenUsed != "" && agent.OAuthAccessToken != "" && agent.OAuthAccessToken != tokenUsed {
				return customTokenSet(agent), nil
			}
			if !force && agent.OAuthAccessToken != "" &&
				(agent.OAuthExpiresAt == 0 || agent.OAuthExpiresAt > time.Now().Add(5*time.Minute).UnixMilli()) {
				return customTokenSet(agent), nil
			}
			if agent.OAuthRefreshToken == "" {
				if agent.OAuthAccessToken == "" {
					return llmcustomauth.TokenSet{}, fmt.Errorf("custom OAuth model %q is not connected", agent.Name)
				}
				return llmcustomauth.TokenSet{}, fmt.Errorf("custom OAuth token for model %q cannot be refreshed; reconnect the model", agent.Name)
			}
			cfg, parseErr := llmcustomauth.ParseConfig(agent.CustomAuthConfigJSON)
			if parseErr != nil {
				return llmcustomauth.TokenSet{}, parseErr
			}
			refreshClient := llmcustomauth.NewHTTPClient(30*time.Second, cfg.AllowPrivateEndpoints)
			refreshed, refreshErr := llmcustomauth.Refresh(ctx, refreshClient, cfg, agent.OAuthRefreshToken, llmcustomauth.RefreshOptions{
				ClientID: agent.OAuthClientID, ClientSecret: agent.OAuthClientSecret,
			})
			if refreshErr != nil {
				return llmcustomauth.TokenSet{}, refreshErr
			}
			updated, persistErr := a.configRepo.UpdateCustomOAuthTokensIfRevision(
				ctx, agent.ID, agent.OAuthConfigRevision,
				refreshed.AccessToken, refreshed.RefreshToken, refreshed.ExpiresAt,
			)
			if persistErr != nil {
				return llmcustomauth.TokenSet{}, persistErr
			}
			if !updated {
				return llmcustomauth.TokenSet{}, fmt.Errorf("custom OAuth configuration changed during token refresh")
			}
			return refreshed, nil
		},
	)
	if err != nil {
		return agent, err
	}
	agent.OAuthAccessToken = tokens.AccessToken
	agent.OAuthRefreshToken = tokens.RefreshToken
	agent.OAuthExpiresAt = tokens.ExpiresAt
	return agent, nil
}

func customTokenSet(agent models.LLMConfig) llmcustomauth.TokenSet {
	return llmcustomauth.TokenSet{
		AccessToken: agent.OAuthAccessToken, RefreshToken: agent.OAuthRefreshToken, ExpiresAt: agent.OAuthExpiresAt,
	}
}

func (a *Adapter) callDirect(ctx context.Context, req llmcontracts.AgentRequest, workDir string) (string, llmcontracts.Usage, error) {
	client, finalizeRequest, err := a.client(ctx, req.Agent)
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
	switch {
	case req.RawDirectPrompt:
		// Message is already fully composed; send it untouched.
	case req.LifecycleHookCall:
		// Lifecycle hooks are structured JSON steps, not coding turns. Keep the
		// hook agent's own prompt — the provider wrapper folded the agent
		// definition into ProjectInstructions — and drop the shared
		// coding-agent framing it has no use for.
		systemPrompt = req.ProjectInstructions
	default:
		systemPrompt = llmprompt.BuildAgentSystemPrompt(req.ProjectInstructions, effectiveWorkDir(workDir))
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
		FinalizeRequest:  finalizeRequest,
		ExtraBody:        extraBody,
		ToolExecutor:     toolExecutor(ctx, workDir),
		ToolFilter:       toolFilter(ctx, true, models.ChatModeOrchestrate),
	})
	err = a.persistReasoningContent(ctx, req.ExecID, req.Agent, client, err)
	if err != nil {
		return "", llmusage.FromTotal(0), fmt.Errorf("openai-compatible chat completions: %w", err)
	}
	return resp.Text, usageFromResponse(resp), stopError(resp.StopReason)
}

func (a *Adapter) callTaskStreaming(ctx context.Context, req llmcontracts.AgentRequest, workDir string) (string, string, llmcontracts.Usage, error) {
	client, finalizeRequest, err := a.client(ctx, req.Agent)
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
	fullPrompt = llmprompt.ApplyTaskCreationToolMode(fullPrompt, rt.DefinitionNames())
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
		FinalizeRequest:  finalizeRequest,
		ExtraBody:        extraBody,
		ToolExecutor:     toolExecutor(ctx, workDir),
		ToolFilter:       toolFilter(ctx, true, models.ChatModeOrchestrate),
		OnText:           streamText(sw),
		OnToolUse:        streamToolUse(sw),
		OnToolResult:     streamToolResult(sw),
	})
	err = a.persistReasoningContent(ctx, req.ExecID, req.Agent, client, err)
	if err != nil {
		sw.Flush()
		return "", "", llmusage.FromTotal(0), fmt.Errorf("openai-compatible chat completions: %w", err)
	}
	sw.Flush()
	return sw.String(), sw.TextString(), usageFromResponse(resp), stopError(resp.StopReason)
}

func (a *Adapter) callChatStreaming(ctx context.Context, req llmcontracts.AgentRequest, workDir string) (string, llmcontracts.Usage, error) {
	client, finalizeRequest, err := a.client(ctx, req.Agent)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}
	extraHeaders, extraBody, err := compatibleRequestExtras(req.Agent)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}
	history, err := a.prepareClientHistory(ctx, req.Agent, req.ChatHistory)
	if err != nil {
		return "", llmusage.FromTotal(0), err
	}
	client.SetCompletionsHistory(history)
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	systemPrompt := llmprompt.BuildChatSystemPrompt(req.Followup, req.ChatMode, req.ChatSystemContext, false)
	if req.ChatMode == models.ChatModeOrchestrate {
		systemPrompt = llmprompt.ApplyChatActionToolMode(systemPrompt, rt.DefinitionNames())
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
		FinalizeRequest:  finalizeRequest,
		ExtraBody:        extraBody,
		ToolExecutor:     toolExecutor(ctx, workDir),
		ToolFilter:       toolFilter(ctx, req.Followup, req.ChatMode),
		OnText:           streamText(sw),
		OnToolUse:        streamToolUse(sw),
		OnToolResult:     streamToolResult(sw),
	})
	err = a.persistReasoningContent(ctx, req.ExecID, req.Agent, client, err)
	if err != nil {
		sw.Flush()
		return "", llmusage.FromTotal(0), fmt.Errorf("openai-compatible chat completions: %w", err)
	}
	sw.Flush()
	return sw.String(), usageFromResponse(resp), stopError(resp.StopReason)
}

func compatibleTemperature(agent models.LLMConfig) float64 {
	// Kimi models use model-defined fixed temperatures. Detect the model rather
	// than the preset so manually configured Moonshot endpoints behave correctly.
	if isKimiModel(agent.Model) {
		return openaiclient.OmittedTemperature()
	}
	return agent.Temperature
}

func isKimiModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "kimi-")
}

func supportsReasoningContentReplay(agent models.LLMConfig) bool {
	switch strings.ToLower(strings.TrimSpace(agent.Model)) {
	case "kimi-k3", "kimi-k2.7-code", "kimi-k2.7-code-highspeed":
		return true
	case "kimi-k2.6":
		body, err := parseObjectJSON(agent.ExtraBodyJSON)
		if err != nil {
			return false
		}
		thinking, _ := body["thinking"].(map[string]interface{})
		if thinking == nil {
			return true
		}
		thinkingType, _ := thinking["type"].(string)
		if strings.EqualFold(strings.TrimSpace(thinkingType), "disabled") {
			return false
		}
		if keep, exists := thinking["keep"]; exists {
			keepValue, ok := keep.(string)
			return ok && strings.EqualFold(strings.TrimSpace(keepValue), "all")
		}
		return true
	default:
		return false
	}
}

func (a *Adapter) persistReasoningContent(ctx context.Context, execID string, agent models.LLMConfig, client *openaiclient.Client, callErr error) error {
	if !supportsReasoningContentReplay(agent) || a.execRepo == nil || strings.TrimSpace(execID) == "" || client == nil {
		return callErr
	}
	transcript := client.LastCompletionsTranscript()
	if len(transcript) == 0 {
		return callErr
	}
	transcriptJSON, err := json.Marshal(transcript)
	if err != nil {
		if callErr != nil {
			return callErr
		}
		return fmt.Errorf("marshal reasoning replay transcript: %w", err)
	}
	var reasoning strings.Builder
	for _, message := range transcript {
		if message.Role == "assistant" {
			reasoning.WriteString(message.ReasoningContent)
		}
	}
	replay := []models.ExecutionReplayMessage{{
		ReasoningContent: reasoning.String(),
		TranscriptJSON:   string(transcriptJSON),
	}}
	err = a.execRepo.ReplaceReasoningReplay(context.WithoutCancel(ctx), execID, reasoning.String(), replay)
	if err != nil {
		if callErr != nil {
			applog.Infof("[openai-compatible] preserve reasoning replay after call error execution=%s: %v", execID, err)
			return callErr
		}
		return fmt.Errorf("persist reasoning replay: %w", err)
	}
	return callErr
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
	if effort := compatibleReasoningEffort(agent); effort != "" {
		if body == nil {
			body = make(map[string]interface{})
		}
		body["reasoning_effort"] = effort
	}
	if strings.EqualFold(strings.TrimSpace(agent.Model), "kimi-k2.6") {
		if body == nil {
			body = make(map[string]interface{})
		}
		thinking, _ := body["thinking"].(map[string]interface{})
		if thinking == nil {
			thinking = make(map[string]interface{})
		}
		thinkingType, _ := thinking["type"].(string)
		if _, exists := thinking["type"]; !exists {
			thinking["type"] = "enabled"
			thinkingType = "enabled"
		}
		if !strings.EqualFold(strings.TrimSpace(thinkingType), "disabled") {
			if _, exists := thinking["keep"]; !exists {
				thinking["keep"] = "all"
			}
		}
		body["thinking"] = thinking
	}
	if isKimiModel(agent.Model) {
		for key := range body {
			if strings.EqualFold(strings.TrimSpace(key), "temperature") {
				delete(body, key)
			}
		}
	}
	return headers, body, nil
}

func compatibleReasoningEffort(agent models.LLMConfig) string {
	model := strings.ToLower(strings.TrimSpace(agent.Model))
	effort := strings.ToLower(strings.TrimSpace(agent.ReasoningEffort))
	switch model {
	case "kimi-k3":
		switch effort {
		case "low", "high", "max":
			return effort
		}
	case "glm-5.2":
		switch effort {
		case "none", "minimal", "low", "medium", "high", "xhigh", "max":
			return effort
		}
	}
	return ""
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

func buildClientHistory(chatHistory []models.Execution, preserveReasoningContent bool) []openaiclient.CompletionsHistoryMessage {
	return buildClientHistoryWithReplay(chatHistory, preserveReasoningContent, nil)
}

func buildClientHistoryWithReplay(
	chatHistory []models.Execution,
	preserveReasoningContent bool,
	replayByExecutionID map[string][]models.ExecutionReplayMessage,
) []openaiclient.CompletionsHistoryMessage {
	history := llmprompt.LimitChatHistory(chatHistory)
	messages := make([]openaiclient.CompletionsHistoryMessage, 0, len(history)*2)
	for _, exec := range history {
		replay := replayByExecutionID[exec.ID]
		if len(replay) == 0 {
			replay = exec.ReplayMessages
		}
		if len(replay) > 0 {
			for _, turn := range replay {
				if transcript, ok := decodeReplayTranscript(turn.TranscriptJSON); ok {
					for _, message := range transcript {
						if !preserveReasoningContent {
							message.ReasoningContent = ""
						}
						messages = append(messages, message)
					}
					continue
				}
				if turn.UserContent != "" {
					messages = append(messages, openaiclient.CompletionsHistoryMessage{Role: "user", Content: turn.UserContent})
				}
				if turn.AssistantContent != "" || (preserveReasoningContent && turn.ReasoningContent != "") {
					reasoningContent := ""
					if preserveReasoningContent {
						reasoningContent = turn.ReasoningContent
					}
					messages = append(messages, openaiclient.CompletionsHistoryMessage{
						Role:             "assistant",
						Content:          turn.AssistantContent,
						ReasoningContent: reasoningContent,
					})
				}
			}
			continue
		}
		if exec.PromptSent != "" {
			messages = append(messages, openaiclient.CompletionsHistoryMessage{Role: "user", Content: exec.PromptSent})
		}
		if replay := llmprompt.ReplayAssistantContent(exec); replay != "" {
			reasoningContent := ""
			if preserveReasoningContent {
				reasoningContent = exec.ReasoningContent
			}
			messages = append(messages, openaiclient.CompletionsHistoryMessage{
				Role:             "assistant",
				Content:          replay,
				ReasoningContent: reasoningContent,
			})
		}
	}
	return messages
}

func decodeReplayTranscript(raw string) ([]openaiclient.CompletionsHistoryMessage, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, false
	}
	var messages []openaiclient.CompletionsHistoryMessage
	if err := json.Unmarshal([]byte(raw), &messages); err != nil || len(messages) == 0 {
		return nil, false
	}
	return messages, true
}

func (a *Adapter) prepareClientHistory(ctx context.Context, agent models.LLMConfig, chatHistory []models.Execution) ([]openaiclient.CompletionsHistoryMessage, error) {
	preserveReasoningContent := supportsReasoningContentReplay(agent)
	if a.execRepo == nil {
		return buildClientHistory(chatHistory, preserveReasoningContent), nil
	}

	limitedHistory := llmprompt.LimitChatHistory(chatHistory)
	history := append([]models.Execution(nil), limitedHistory...)
	ids := make([]string, 0, len(history))
	for _, exec := range history {
		if strings.TrimSpace(exec.ID) != "" {
			ids = append(ids, exec.ID)
		}
	}
	if preserveReasoningContent {
		reasoningByID, err := a.execRepo.ReasoningContentByIDs(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("load Kimi reasoning history: %w", err)
		}
		for i := range history {
			if reasoningContent, ok := reasoningByID[history[i].ID]; ok {
				history[i].ReasoningContent = reasoningContent
			}
		}
	}
	replayByExecutionID, err := a.execRepo.ReplayMessagesByExecutionIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load execution replay history: %w", err)
	}
	return buildClientHistoryWithReplay(history, preserveReasoningContent, replayByExecutionID), nil
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

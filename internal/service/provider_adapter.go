package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openvibely/openvibely/internal/agentplugins"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/events"
	llmanthropic "github.com/openvibely/openvibely/internal/llm/anthropic"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmmixture "github.com/openvibely/openvibely/internal/llm/mixture"
	llmollama "github.com/openvibely/openvibely/internal/llm/ollama"
	llmopenai "github.com/openvibely/openvibely/internal/llm/openai"
	llmopenai_compatible "github.com/openvibely/openvibely/internal/llm/openai_compatible"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	llmusage "github.com/openvibely/openvibely/internal/llm/usage"
	"github.com/openvibely/openvibely/internal/models"
)

// ProviderAdapter isolates provider-specific call routing from core orchestration.
// Implementations choose the active API/OAuth transport for each provider.
type ProviderAdapter interface {
	Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error)
}

var resolvePluginRuntimeBundleFn = agentplugins.ResolveRuntimeBundle

func (s *LLMService) initProviderAdapters() {
	anthropicAdapter := llmanthropic.New(s.llmConfigRepo, s.execRepo, s.executionStreamHub)
	openaiAdapter := llmopenai.New(s.llmConfigRepo, s.execRepo, s.executionStreamHub)
	openaiCompatibleAdapter := llmopenai_compatible.NewWithConfigRepo(s.llmConfigRepo, s.execRepo, s.executionStreamHub)
	ollamaAdapter := llmollama.New(s.execRepo, s.executionStreamHub)
	s.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		models.ProviderAnthropic:        &anthropicProviderAdapter{svc: s, adapter: anthropicAdapter},
		models.ProviderOpenAI:           &openAIProviderAdapter{svc: s, adapter: openaiAdapter},
		models.ProviderOpenAICompatible: &openAICompatibleProviderAdapter{svc: s, adapter: openaiCompatibleAdapter},
		models.ProviderOllama:           &ollamaProviderAdapter{svc: s, adapter: ollamaAdapter},
		models.ProviderMixture:          &mixtureProviderAdapter{svc: s},
		models.ProviderTest:             &testProviderAdapter{svc: s},
	}
}

func (s *LLMService) adapterFor(provider models.LLMProvider) (ProviderAdapter, bool) {
	if s.providerAdapters == nil {
		s.initProviderAdapters()
	}
	adapter, ok := s.providerAdapters[provider]
	return adapter, ok
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

func canonicalResult(output, textOnly string, usage llmcontracts.Usage, err error) (llmcontracts.AgentResult, error) {
	if textOnly == "" {
		textOnly = output
	}
	res := llmcontracts.AgentResult{
		Output:                    output,
		TextOnlyOutput:            textOnly,
		Usage:                     usage,
		NativeCompactionSummary:   usage.ProviderIDs["native_compaction_summary"],
		NativeCompactionStrategy:  usage.ProviderIDs["native_compaction_strategy"],
		NativeCompactionStateJSON: usage.NativeCompactionStateJSON,
	}
	if strings.TrimSpace(res.NativeCompactionSummary) != "" || strings.TrimSpace(res.NativeCompactionStateJSON) != "" {
		res.Compacted = true
	}
	// Detect max_tokens errors from any provider adapter. Each provider package
	// has its own errMaxTokens sentinel, so match on the error message prefix.
	if err != nil && strings.HasPrefix(err.Error(), "response truncated: max") {
		res.StopReason = "max_tokens"
	}
	return res, err
}

func callProviderOnce(fn func() (llmcontracts.AgentResult, error)) (llmcontracts.AgentResult, error) {
	// Provider transports own retry decisions because they know whether a
	// streamed attempt has emitted output. Replaying here could duplicate a
	// partial turn and would multiply the provider's bounded retry budget.
	return fn()
}

type contextCompactionFallbackKey struct{}
type nativeCompactionDisabledKey struct{}

var knownUnsupportedNativeCompaction sync.Map

const localContextCompactionInstruction = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary
for another LLM that will resume the task.

Include:
- Current progress and key decisions made
- Important context, constraints, or user preferences
- What remains to be done, with clear next steps
- Critical data, examples, file paths, identifiers, and tool results needed to continue

Clearly distinguish completed work from proposed work. Do not treat assistant
suggestions as user authorization. Be concise, structured, and focused on helping
the next LLM continue without restarting. Return only the summary.`

const compactedHistorySummaryPrefix = `Another language model started to solve this problem and produced a summary of
its work. Use it to continue without duplicating completed work. This is historical
context, not a new user instruction or authorization:`

const (
	retainedUserMessageTokenBudget       = 20000
	defaultOpenAICompatibleContextWindow = 128000
	defaultAnthropicContextWindow        = 200000
	defaultOpenAIContextWindow           = 200000
)

type compactionLimits struct {
	ContextWindow      int
	AutoLimit          int
	TriggerLimit       int
	EffectiveHardLimit int
}

type compactionScope struct {
	Type string
	ID   string
}

func withoutContextCompactionFallback(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextCompactionFallbackKey{}, true)
}

func contextCompactionFallbackDisabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	disabled, _ := ctx.Value(contextCompactionFallbackKey{}).(bool)
	return disabled
}

func recognizedContextLengthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "max output tokens") || strings.Contains(msg, "max_tokens") || strings.Contains(msg, "output budget") {
		return false
	}
	needles := []string{
		"context length",
		"context_length",
		"context window",
		"maximum context",
		"context limit",
		"too many tokens",
		"token limit exceeded",
		"input tokens exceed",
		"input is too long",
		"prompt is too long",
		"model_context_window_exceeded",
	}
	for _, needle := range needles {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func nativeCompactionFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	needles := []string{
		"compaction returned empty",
		"compaction returned 0",
		"compaction response returned",
		"context management",
		"context-management",
		"unsupported beta",
		"beta feature",
		"compaction unsupported",
		"unsupported compaction",
		"/responses/compact",
		"pre-turn compaction",
		"overflow recovery compaction",
	}
	for _, needle := range needles {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func nativeCompactionUnsupportedError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unsupported") || strings.Contains(msg, "unknown beta") || strings.Contains(msg, "beta feature") || strings.Contains(msg, "not available")
}

func providerHasNativeCompaction(provider models.LLMProvider) bool {
	return provider == models.ProviderOpenAI || provider == models.ProviderAnthropic
}

func nativeCompactionSessionKey(agent models.LLMConfig) string {
	return string(agent.Provider) + "\x00" + strings.ToLower(strings.TrimSpace(agent.Model))
}

func compactionLimitsForAgent(agent models.LLMConfig) compactionLimits {
	window := agent.ContextWindow
	if window <= 0 {
		switch agent.Provider {
		case models.ProviderOpenAI:
			window = openAIContextWindow(agent.Model)
		case models.ProviderAnthropic:
			window = defaultAnthropicContextWindow
		case models.ProviderOpenAICompatible:
			window = defaultOpenAICompatibleContextWindow
		default:
			window = defaultOpenAIContextWindow
		}
	}
	autoLimit := (window * 90) / 100
	triggerLimit := autoLimit
	if agent.CompactionThreshold > 0 && agent.CompactionThreshold < triggerLimit {
		triggerLimit = agent.CompactionThreshold
	}
	return compactionLimits{ContextWindow: window, AutoLimit: autoLimit, TriggerLimit: triggerLimit, EffectiveHardLimit: (window * 95) / 100}
}

func openAIContextWindow(model string) int {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-6-astra":
		return 272000
	case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		return 272000
	case "gpt-5.5", "gpt-5.5-pro", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex", "gpt-5.2-codex", "gpt-5.1-codex-max", "gpt-5.1-codex", "gpt-5.1-codex-mini", "gpt-5-codex", "gpt-5-codex-mini":
		// Keep this in sync with pkg/openai_client.openAIModelContextWindow.
		return 272000
	case "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano":
		return 1047576
	case "gpt-4o", "gpt-4o-mini", "gpt-4-turbo", "gpt-5", "gpt-5-mini", "gpt-5-nano":
		return 128000
	default:
		return defaultOpenAIContextWindow
	}
}

func shouldTriggerContextCompaction(req llmcontracts.AgentRequest) (bool, compactionLimits, int) {
	limits := compactionLimitsForAgent(req.Agent)
	used := estimateModelVisibleRequestTokens(req)
	if req.ContextTokenEstimate > 0 {
		used = req.ContextTokenEstimate
	}
	return used >= limits.TriggerLimit || used >= limits.EffectiveHardLimit, limits, used
}

func estimateModelVisibleRequestTokens(req llmcontracts.AgentRequest) int {
	total := estimatedUTF8Tokens(req.Message) + estimatedUTF8Tokens(req.NativeCompactionStateJSON)
	total += estimateProviderSystemPromptTokens(req)
	total += estimateRuntimeToolDefinitionTokens(llmcontracts.RuntimeToolsFromContext(req.Ctx))
	total += estimateAgentDefinitionTokens(req.AgentDefinition)
	for _, att := range req.Attachments {
		total += estimatedUTF8Tokens(att.FileName) + estimatedUTF8Tokens(att.MediaType) + estimatedUTF8Tokens(att.FilePath)
		if att.FileSize > 0 {
			total += int((att.FileSize + 3) / 4)
		}
	}
	for _, exec := range req.ChatHistory {
		total += estimatedUTF8Tokens(exec.PromptSent) + estimatedUTF8Tokens(exec.Output) + estimatedUTF8Tokens(exec.ErrorMessage) + estimatedUTF8Tokens(exec.ReasoningContent)
		for _, replay := range exec.ReplayMessages {
			total += estimatedUTF8Tokens(replay.UserContent) + estimatedUTF8Tokens(replay.AssistantContent) + estimatedUTF8Tokens(replay.ReasoningContent) + estimatedUTF8Tokens(replay.TranscriptJSON)
		}
	}
	return total
}

func estimateProviderSystemPromptTokens(req llmcontracts.AgentRequest) int {
	switch req.Operation {
	case llmcontracts.OperationStreaming:
		if req.Followup || req.ChatHistory != nil || req.ChatMode == models.ChatModeOrchestrate || req.ChatMode == models.ChatModePlan {
			return estimatedUTF8Tokens(llmprompt.BuildChatSystemPrompt(req.Followup, req.ChatMode, req.ChatSystemContext, false))
		}
		return estimatedUTF8Tokens(llmprompt.BuildAgentSystemPrompt(req.ProjectInstructions, req.WorkDir))
	case llmcontracts.OperationTask:
		return estimatedUTF8Tokens(llmprompt.BuildAgentSystemPrompt(req.ProjectInstructions, req.WorkDir))
	case llmcontracts.OperationDirect:
		if req.RawDirectPrompt {
			return 0
		}
		return estimatedUTF8Tokens(llmprompt.BuildAgentSystemPrompt(req.ProjectInstructions, req.WorkDir))
	default:
		return 0
	}
}

func estimateRuntimeToolDefinitionTokens(rt *llmcontracts.RuntimeTools) int {
	if rt == nil {
		return 0
	}
	total := 0
	for _, def := range rt.Definitions {
		total += estimatedUTF8Tokens(def.Name) + estimatedUTF8Tokens(def.Description) + estimatedUTF8Tokens(string(def.Parameters)) + estimatedUTF8Tokens(string(def.Access))
	}
	return total
}

func estimateAgentDefinitionTokens(agentDef *models.Agent) int {
	if agentDef == nil {
		return 0
	}
	total := estimatedUTF8Tokens(agentDef.Name) + estimatedUTF8Tokens(agentDef.Description) + estimatedUTF8Tokens(agentDef.SystemPrompt) + estimatedUTF8Tokens(strings.Join(agentDef.Tools, "\n"))
	for _, skill := range agentDef.Skills {
		total += estimatedUTF8Tokens(skill.Name) + estimatedUTF8Tokens(skill.Description) + estimatedUTF8Tokens(skill.Tools) + estimatedUTF8Tokens(skill.Content)
	}
	for _, scoped := range agentDef.ToolConfig.ScopedFiles {
		total += estimatedUTF8Tokens(scoped.Directory) + estimatedUTF8Tokens(strings.Join(scoped.Permissions, "\n"))
	}
	return total
}

func (s *LLMService) callProviderWithContextCompactionFallback(adapter ProviderAdapter, req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	if s == nil || s.execRepo == nil || contextCompactionFallbackDisabled(req.Ctx) {
		return s.callProviderWithCompaction(adapter, req)
	}
	scope, ok := s.compactionScopeForRequest(req)
	if !ok {
		return s.callProviderWithCompaction(adapter, req)
	}
	model := req.Agent.Model
	if req.AgentDefinition != nil && req.AgentDefinition.Model != "" && req.AgentDefinition.Model != "inherit" {
		model = req.AgentDefinition.Model
	}
	key := strings.Join([]string{req.Agent.ID, string(req.Agent.Provider), model, req.Agent.BaseURL, string(req.Agent.AuthMethod)}, "|")
	baseline, err := s.execRepo.GetChatContextUsage(req.Ctx, scope.Type, scope.ID, key)
	if err == nil && baseline != nil {
		req.ContextTokenEstimate = estimateContextFromReportedUsage(req, *baseline)
	}
	res, err := s.callProviderWithCompaction(adapter, req)
	if err == nil && req.ExecID != "" {
		usage := models.ChatContextUsage{SourceExecutionID: req.ExecID, ContextTokens: res.Usage.LastContextTokens, OverheadTokens: requestContextOverhead(req)}
		if saveErr := s.execRepo.SaveChatContextUsage(req.Ctx, scope.Type, scope.ID, key, usage); saveErr != nil {
			applog.Infof("[agent-svc] persist context usage: %v", saveErr)
		}
	}
	return res, err
}

func requestContextOverhead(req llmcontracts.AgentRequest) int {
	req.Message = ""
	if req.ChatHistory != nil {
		req.ChatHistory = []models.Execution{}
	}
	req.Attachments = nil
	req.NativeCompactionStateJSON = ""
	return estimateModelVisibleRequestTokens(req)
}

func estimateContextFromReportedUsage(req llmcontracts.AgentRequest, baseline models.ChatContextUsage) int {
	if baseline.ContextTokens <= 0 || baseline.SourceExecutionID == "" {
		return 0
	}
	for i, item := range req.ChatHistory {
		if item.ID != baseline.SourceExecutionID {
			continue
		}
		for _, later := range req.ChatHistory[i+1:] {
			if later.StartsNewContext {
				return 0
			}
		}
		req.ChatHistory = req.ChatHistory[i+1:]
		req.NativeCompactionStateJSON = ""
		// The reported count already includes the previous system/tools. Only
		// add changes to that overhead and new user/tool content.
		return max(1, baseline.ContextTokens+estimateModelVisibleRequestTokens(req)-baseline.OverheadTokens)
	}
	return 0 // Missing anchor: do not reuse an unrelated or truncated baseline.
}

func (s *LLMService) callProviderWithCompaction(adapter ProviderAdapter, req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	if contextCompactionFallbackDisabled(req.Ctx) {
		return adapter.Call(req)
	}
	uncompactedReq := req
	req = s.restoreCompactionCheckpoint(req)
	lastResortBaseReq := req
	locallyCompactedBeforeProvider := false
	triggered, limits, used := shouldTriggerContextCompaction(req)
	if limits.TriggerLimit > 0 {
		req.NativeCompactionTokenThreshold = limits.TriggerLimit
		if req.Agent.CompactionThreshold <= 0 || req.Agent.CompactionThreshold > limits.AutoLimit {
			req.Agent.CompactionThreshold = limits.AutoLimit
		} else {
			req.Agent.CompactionThreshold = limits.TriggerLimit
		}
	}
	if providerHasNativeCompaction(req.Agent.Provider) && knownNativeCompactionUnsupported(req.Agent) {
		req.DisableNativeCompaction = true
		req.Agent.DisableNativeCompaction = true
	}
	if triggered && providerHasNativeCompaction(req.Agent.Provider) && !req.DisableNativeCompaction && !req.Agent.DisableNativeCompaction {
		req.ForceNativeCompaction = true
		req.Agent.ForceNativeCompaction = true
	}
	if triggered && shouldUseLocalSummaryBeforeProvider(req) {
		if req.NativeCompactionStateJSON != "" {
			req.ChatHistory = uncompactedReq.ChatHistory
			req.NativeCompactionStateJSON = ""
		}
		originalReq := req
		compacted, compactErr := s.compactRequestHistoryWithLocalSummary(adapter, req)
		if compactErr != nil {
			applog.Infof("[agent-svc] proactive context compaction failed provider=%s model=%s tokens=%d trigger=%d: %v", req.Agent.Provider, req.Agent.Model, used, limits.TriggerLimit, compactErr)
			return s.callProviderWithLastResortTruncation(adapter, req, compactErr)
		}
		s.persistCompactionCheckpoint(originalReq, compacted, historyCompactionSummary(compacted.ChatHistory), "local_summary")
		lastResortBaseReq = originalReq
		req = compacted
		locallyCompactedBeforeProvider = true
		applog.Infof("[agent-svc] proactive local context compaction provider=%s model=%s tokens=%d trigger=%d history=%d compacted_history=%d", req.Agent.Provider, req.Agent.Model, used, limits.TriggerLimit, len(originalReq.ChatHistory), len(req.ChatHistory))
	}
	res, err := adapter.Call(req)
	if err == nil {
		s.persistNativeCompactionCheckpoint(req, res)
		return res, nil
	}
	// A text summarizer cannot decode native encrypted state. Recover the
	// original transcript before switching strategies, including when the
	// checkpoint consumed every prior execution.
	if req.NativeCompactionStateJSON != "" {
		req.ChatHistory = uncompactedReq.ChatHistory
		req.NativeCompactionStateJSON = ""
	}
	if locallyCompactedBeforeProvider && recognizedContextLengthError(err) {
		applog.Infof("[agent-svc] compacted provider retry still exceeded context; trying last-resort truncation: %v", err)
		return s.callProviderWithLastResortTruncation(adapter, lastResortBaseReq, err)
	}
	if providerHasNativeCompaction(req.Agent.Provider) && (nativeCompactionFailure(err) || recognizedContextLengthError(err)) && len(req.ChatHistory) > 0 {
		if nativeCompactionUnsupportedError(err) {
			knownUnsupportedNativeCompaction.Store(nativeCompactionSessionKey(req.Agent), true)
		}
		compactedReq := req
		compactedReq.DisableNativeCompaction = true
		compactedReq.Agent.DisableNativeCompaction = true
		compacted, compactErr := s.compactRequestHistoryWithLocalSummary(adapter, compactedReq)
		if compactErr != nil {
			applog.Infof("[agent-svc] native-to-local context compaction fallback failed; trying last-resort truncation: %v", compactErr)
			return s.callProviderWithLastResortTruncation(adapter, req, compactErr)
		}
		s.persistCompactionCheckpoint(req, compacted, historyCompactionSummary(compacted.ChatHistory), "local_summary")
		applog.Infof("[agent-svc] retrying provider call once after native-to-local context compaction provider=%s model=%s history=%d compacted_history=%d", req.Agent.Provider, req.Agent.Model, len(req.ChatHistory), len(compacted.ChatHistory))
		return s.callCompactedRetryOrLastResort(adapter, req, compacted)
	}
	if recognizedContextLengthError(err) && len(req.ChatHistory) > 0 {
		compacted, compactErr := s.compactRequestHistoryWithLocalSummary(adapter, req)
		if compactErr != nil {
			applog.Infof("[agent-svc] context compaction fallback failed; trying last-resort truncation: %v", compactErr)
			return s.callProviderWithLastResortTruncation(adapter, req, compactErr)
		}
		s.persistCompactionCheckpoint(req, compacted, historyCompactionSummary(compacted.ChatHistory), "local_summary")
		applog.Infof("[agent-svc] retrying provider call once after local context compaction provider=%s model=%s history=%d compacted_history=%d", req.Agent.Provider, req.Agent.Model, len(req.ChatHistory), len(compacted.ChatHistory))
		return s.callCompactedRetryOrLastResort(adapter, req, compacted)
	}
	return res, err
}

func (s *LLMService) restoreCompactionCheckpoint(req llmcontracts.AgentRequest) llmcontracts.AgentRequest {
	scope, ok := s.compactionScopeForRequest(req)
	if !ok || s == nil || s.execRepo == nil {
		return req
	}
	checkpoint, err := s.execRepo.GetChatCompactionCheckpoint(req.Ctx, scope.Type, scope.ID)
	if err != nil {
		applog.Infof("[agent-svc] restore context compaction checkpoint failed scope=%s:%s: %v", scope.Type, scope.ID, err)
		return req
	}
	if checkpoint == nil || (len(checkpoint.History) == 0 && strings.TrimSpace(checkpoint.ProviderStateJSON) == "") {
		return req
	}
	if checkpoint.ModelConfigID != "" && req.Agent.ID != "" && checkpoint.ModelConfigID != req.Agent.ID {
		return req
	}
	restored := req
	if checkpoint.Strategy == "local_summary" {
		// Retained prompts are synthetic context, not executions to hydrate.
		// Also repair checkpoints written before IDs were stripped.
		for i := range checkpoint.History {
			checkpoint.History[i].ID = ""
		}
	}
	restored.ChatHistory = mergeCheckpointHistory(req.ChatHistory, checkpoint.History, checkpoint.SourceExecutionID)
	restored.NativeCompactionStateJSON = checkpoint.ProviderStateJSON
	return restored
}

func mergeCheckpointHistory(history, checkpoint []models.Execution, sourceExecutionID string) []models.Execution {
	merged := append([]models.Execution(nil), checkpoint...)
	if strings.TrimSpace(sourceExecutionID) == "" {
		return append(merged, history...)
	}
	for i, exec := range history {
		if exec.ID == sourceExecutionID {
			return append(merged, history[i+1:]...)
		}
	}
	return append(merged, history...)
}

func (s *LLMService) persistNativeCompactionCheckpoint(req llmcontracts.AgentRequest, res llmcontracts.AgentResult) {
	providerStateJSON := strings.TrimSpace(res.NativeCompactionStateJSON)
	if providerStateJSON != "" {
		s.persistNativeProviderStateCheckpoint(req, res.NativeCompactionStrategy, providerStateJSON)
		return
	}
	summary := strings.TrimSpace(res.NativeCompactionSummary)
	if summary == "" || len(req.ChatHistory) == 0 {
		return
	}
	strategy := strings.TrimSpace(res.NativeCompactionStrategy)
	if strategy == "" {
		strategy = "native"
	}
	compacted := req
	compacted.ChatHistory = buildCompactedReplacementHistory(req.ChatHistory, summary)
	s.persistCompactionCheckpoint(req, compacted, summary, strategy)
}

func (s *LLMService) persistNativeProviderStateCheckpoint(req llmcontracts.AgentRequest, strategy, providerStateJSON string) {
	if s == nil || s.execRepo == nil {
		return
	}
	scope, ok := s.compactionScopeForRequest(req)
	if !ok {
		return
	}
	sourceID := strings.TrimSpace(req.ExecID)
	if sourceID == "" && len(req.ChatHistory) > 0 {
		sourceID = req.ChatHistory[len(req.ChatHistory)-1].ID
	}
	if strings.TrimSpace(strategy) == "" {
		strategy = "native"
	}
	if err := s.execRepo.UpsertChatCompactionCheckpoint(req.Ctx, models.ChatCompactionCheckpoint{
		ScopeType: scope.Type, ScopeID: scope.ID, ModelConfigID: req.Agent.ID,
		SourceExecutionID: sourceID, Strategy: strategy, ProviderStateJSON: providerStateJSON,
	}); err != nil {
		applog.Infof("[agent-svc] persist native context checkpoint failed scope=%s:%s: %v", scope.Type, scope.ID, err)
	}
}

func (s *LLMService) persistCompactionCheckpoint(originalReq, compactedReq llmcontracts.AgentRequest, summary, strategy string) {
	if s == nil || s.execRepo == nil || len(compactedReq.ChatHistory) == 0 {
		return
	}
	scope, ok := s.compactionScopeForRequest(originalReq)
	if !ok {
		return
	}
	sourceID := ""
	if len(originalReq.ChatHistory) > 0 {
		sourceID = originalReq.ChatHistory[len(originalReq.ChatHistory)-1].ID
	}
	if err := s.execRepo.UpsertChatCompactionCheckpoint(originalReq.Ctx, models.ChatCompactionCheckpoint{
		ScopeType:         scope.Type,
		ScopeID:           scope.ID,
		ModelConfigID:     originalReq.Agent.ID,
		SourceExecutionID: sourceID,
		History:           compactedReq.ChatHistory,
		Summary:           strings.TrimSpace(summary),
		Strategy:          strategy,
	}); err != nil {
		applog.Infof("[agent-svc] persist context compaction checkpoint failed scope=%s:%s: %v", scope.Type, scope.ID, err)
	}
}

func (s *LLMService) compactionScopeForRequest(req llmcontracts.AgentRequest) (compactionScope, bool) {
	if scope, ok := parseCompactionTransportScope(req.TransportScope); ok {
		return scope, true
	}
	if req.Followup && strings.TrimSpace(req.ExecID) != "" && s != nil && s.execRepo != nil {
		if exec, err := s.execRepo.GetByID(req.Ctx, req.ExecID); err == nil && exec != nil && strings.TrimSpace(exec.TaskID) != "" {
			return compactionScope{Type: "task", ID: strings.TrimSpace(exec.TaskID)}, true
		}
	}
	if strings.TrimSpace(req.ProjectID) != "" && req.Operation == llmcontracts.OperationStreaming && !req.Followup {
		return compactionScope{Type: "chat_project", ID: strings.TrimSpace(req.ProjectID)}, true
	}
	return compactionScope{}, false
}

func parseCompactionTransportScope(scope string) (compactionScope, bool) {
	scope = strings.TrimSpace(scope)
	if strings.HasPrefix(scope, "task:") {
		id := strings.TrimSpace(strings.TrimPrefix(scope, "task:"))
		return compactionScope{Type: "task", ID: id}, id != ""
	}
	if strings.HasPrefix(scope, "chat:project:") {
		id := strings.TrimSpace(strings.TrimPrefix(scope, "chat:project:"))
		return compactionScope{Type: "chat_project", ID: id}, id != ""
	}
	return compactionScope{}, false
}

func shouldUseLocalSummaryBeforeProvider(req llmcontracts.AgentRequest) bool {
	if len(req.ChatHistory) == 0 {
		return false
	}
	if req.Agent.Provider == models.ProviderOpenAICompatible {
		return true
	}
	return req.DisableNativeCompaction || req.Agent.DisableNativeCompaction || knownNativeCompactionUnsupported(req.Agent)
}

func knownNativeCompactionUnsupported(agent models.LLMConfig) bool {
	_, ok := knownUnsupportedNativeCompaction.Load(nativeCompactionSessionKey(agent))
	return ok
}

func (s *LLMService) callCompactedRetryOrLastResort(adapter ProviderAdapter, originalReq, compactedReq llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	res, err := adapter.Call(compactedReq)
	if err == nil || !recognizedContextLengthError(err) {
		return res, err
	}
	applog.Infof("[agent-svc] compacted provider retry still exceeded context; trying last-resort truncation: %v", err)
	return s.callProviderWithLastResortTruncation(adapter, originalReq, err)
}

func (s *LLMService) callProviderWithLastResortTruncation(adapter ProviderAdapter, req llmcontracts.AgentRequest, cause error) (llmcontracts.AgentResult, error) {
	if len(req.ChatHistory) == 0 {
		return llmcontracts.AgentResult{}, cause
	}
	truncated := req
	truncated.ChatHistory = llmprompt.LimitChatHistory(req.ChatHistory)
	applog.Infof("[agent-svc] WARNING: context compaction failed; using last-resort latest-20-turn truncation provider=%s model=%s history=%d truncated_history=%d error=%v", req.Agent.Provider, req.Agent.Model, len(req.ChatHistory), len(truncated.ChatHistory), cause)
	return adapter.Call(truncated)
}

func (s *LLMService) compactRequestHistoryWithLocalSummary(adapter ProviderAdapter, req llmcontracts.AgentRequest) (llmcontracts.AgentRequest, error) {
	history := append([]models.Execution(nil), req.ChatHistory...)
	summaryBase := req
	summaryCtx := req.Ctx
	if summaryCtx == nil {
		summaryCtx = context.Background()
	}
	summaryCtx, cancelSummary := context.WithCancel(summaryCtx)
	summaryBase.Ctx = summaryCtx
	defer cancelSummary()
	summaryBase.TransportScope = fmt.Sprintf("compaction:%s:%d", req.ExecID, time.Now().UnixNano())
	for len(history) > 0 {
		summary, err := s.localSummaryCompaction(adapter, summaryBase, history)
		if err == nil {
			compacted := req
			compacted.ChatHistory = buildCompactedReplacementHistory(history, summary)
			compacted.NativeCompactionStateJSON = ""
			compacted.Ctx = llmcontracts.WithNativeCompactionStateJSON(compacted.Ctx, "")
			return compacted, nil
		}
		if !recognizedContextLengthError(err) || len(history) == 1 {
			return llmcontracts.AgentRequest{}, err
		}
		history = history[1:]
	}
	return llmcontracts.AgentRequest{}, fmt.Errorf("no history remains to compact")
}

func (s *LLMService) localSummaryCompaction(adapter ProviderAdapter, req llmcontracts.AgentRequest, history []models.Execution) (string, error) {
	summaryReq := req
	summaryReq.NativeCompactionStateJSON = ""
	summaryReq.Ctx = llmcontracts.WithoutRuntimeTools(withoutContextCompactionFallback(req.Ctx))
	summaryReq.Ctx = llmcontracts.WithNativeCompactionStateJSON(summaryReq.Ctx, "")
	summaryReq.Operation = llmcontracts.OperationDirect
	summaryReq.Message = buildLocalSummaryCompactionPrompt(history)
	summaryReq.Attachments = nil
	summaryReq.ExecID = ""
	summaryReq.Ctx = llmcontracts.WithTransportScope(summaryReq.Ctx, req.TransportScope)
	summaryReq.ChatHistory = nil
	summaryReq.ChatSystemContext = ""
	summaryReq.ProjectInstructions = ""
	summaryReq.AgentDefinition = nil
	summaryReq.DisableTools = true
	summaryReq.RawDirectPrompt = true
	summaryReq.Followup = false
	summaryReq.DisableNativeCompaction = true
	summaryReq.ForceNativeCompaction = false
	summaryReq.NativeCompactionTokenThreshold = 0
	summaryReq.Agent.DisableNativeCompaction = true
	summaryReq.Agent.ForceNativeCompaction = false
	summaryReq.Agent.CompactionThreshold = 0
	res, err := adapter.Call(summaryReq)
	if err != nil {
		return "", err
	}
	summary := strings.TrimSpace(res.TextOnlyOutput)
	if summary == "" {
		summary = strings.TrimSpace(res.Output)
	}
	if summary == "" {
		return "", fmt.Errorf("local context compaction returned an empty summary")
	}
	return summary, nil
}

func buildLocalSummaryCompactionPrompt(history []models.Execution) string {
	var sb strings.Builder
	for _, exec := range history {
		if strings.TrimSpace(exec.PromptSent) != "" {
			sb.WriteString("User: ")
			sb.WriteString(strings.TrimSpace(exec.PromptSent))
			sb.WriteString("\n\n")
		}
		writeExecutionReplayForCompaction(&sb, exec)
		if len(exec.ReplayMessages) == 0 {
			if replay := llmprompt.ReplayAssistantContent(exec); strings.TrimSpace(replay) != "" {
				sb.WriteString("Assistant: ")
				sb.WriteString(strings.TrimSpace(replay))
				sb.WriteString("\n\n")
			}
		}
	}
	if sb.Len() > 0 {
		sb.WriteString("---\n\n")
	}
	sb.WriteString(localContextCompactionInstruction)
	return sb.String()
}

func writeExecutionReplayForCompaction(sb *strings.Builder, exec models.Execution) {
	for _, replay := range exec.ReplayMessages {
		if strings.TrimSpace(replay.TranscriptJSON) != "" {
			sb.WriteString("Structured tool/message replay transcript JSON (preserve tool-call/tool-result relationships):\n")
			sb.WriteString(strings.TrimSpace(replay.TranscriptJSON))
			sb.WriteString("\n\n")
			continue
		}
		if strings.TrimSpace(replay.UserContent) != "" {
			sb.WriteString("Replay user/tool-result content: ")
			sb.WriteString(strings.TrimSpace(replay.UserContent))
			sb.WriteString("\n\n")
		}
		if strings.TrimSpace(replay.AssistantContent) != "" || strings.TrimSpace(replay.ReasoningContent) != "" {
			sb.WriteString("Replay assistant/tool-call content: ")
			sb.WriteString(strings.TrimSpace(replay.AssistantContent))
			if strings.TrimSpace(replay.ReasoningContent) != "" {
				sb.WriteString("\nReasoning summary: ")
				sb.WriteString(strings.TrimSpace(replay.ReasoningContent))
			}
			sb.WriteString("\n\n")
		}
	}
}

func buildCompactedReplacementHistory(history []models.Execution, summary string) []models.Execution {
	retained := retainedUserMessageHistory(history, retainedUserMessageTokenBudget)
	out := make([]models.Execution, 0, len(retained)+1)
	out = append(out, retained...)
	out = append(out, models.Execution{
		Status: models.ExecCompleted,
		Output: compactedHistorySummaryPrefix + "\n\n" + strings.TrimSpace(summary),
	})
	return out
}

func retainedUserMessageHistory(history []models.Execution, tokenBudget int) []models.Execution {
	if tokenBudget <= 0 {
		return nil
	}
	retained := make([]models.Execution, 0, len(history))
	remaining := tokenBudget
	for i := len(history) - 1; i >= 0; i-- {
		content := strings.TrimSpace(history[i].PromptSent)
		if content == "" {
			continue
		}
		tokens := estimatedUTF8Tokens(content)
		if tokens <= remaining {
			retained = append(retained, models.Execution{PromptSent: content, Status: models.ExecCompleted})
			remaining -= tokens
			continue
		}
		if remaining > 0 {
			retained = append(retained, models.Execution{PromptSent: truncateMiddleByEstimatedTokens(content, remaining), Status: models.ExecCompleted})
		}
		break
	}
	for i, j := 0, len(retained)-1; i < j; i, j = i+1, j-1 {
		retained[i], retained[j] = retained[j], retained[i]
	}
	return retained
}

func estimatedUTF8Tokens(text string) int {
	bytes := len([]byte(text))
	if bytes == 0 {
		return 0
	}
	return (bytes + 3) / 4
}

func truncateMiddleByEstimatedTokens(text string, tokenBudget int) string {
	byteBudget := tokenBudget * 4
	if byteBudget <= 0 || len([]byte(text)) <= byteBudget {
		return text
	}
	runes := []rune(text)
	if len(runes) <= 1 {
		return text
	}
	gap := "\n\n[Middle of user message omitted during context compaction]\n\n"
	gapBytes := len([]byte(gap))
	if byteBudget <= gapBytes+8 {
		return takePrefixBytes(runes, byteBudget)
	}
	contentBudget := byteBudget - gapBytes
	headBudget := contentBudget / 2
	tailBudget := contentBudget - headBudget
	head := takePrefixBytes(runes, headBudget)
	tail := takeSuffixBytes(runes, tailBudget)
	return head + gap + tail
}

func takePrefixBytes(runes []rune, byteBudget int) string {
	var sb strings.Builder
	used := 0
	for _, r := range runes {
		b := len(string(r))
		if used+b > byteBudget {
			break
		}
		sb.WriteRune(r)
		used += b
	}
	return sb.String()
}

func takeSuffixBytes(runes []rune, byteBudget int) string {
	used := 0
	start := len(runes)
	for i := len(runes) - 1; i >= 0; i-- {
		b := len(string(runes[i]))
		if used+b > byteBudget {
			break
		}
		used += b
		start = i
	}
	return string(runes[start:])
}

func historyCompactionSummary(history []models.Execution) string {
	if len(history) == 0 {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(history[len(history)-1].Output), compactedHistorySummaryPrefix)
}

func resolveAgentRuntime(ctx context.Context, ad *models.Agent) (raw *models.Agent, merged *models.Agent) {
	if ad == nil {
		return nil, nil
	}
	raw = ad
	merged = ad
	if len(ad.Plugins) == 0 {
		return raw, merged
	}
	runtime, err := resolvePluginRuntimeBundleFn(ctx, ad.Plugins)
	if err != nil {
		applog.Infof("[agent-svc] resolveAgentRuntime failed for %s: %v", ad.Name, err)
		return raw, merged
	}
	merged = agentplugins.MergeAgentWithRuntime(ad, runtime)
	return raw, merged
}

type anthropicProviderAdapter struct {
	svc     *LLMService
	adapter *llmanthropic.Adapter
}

func anthropicAdapterEnabled(agent models.LLMConfig) bool {
	return agent.IsOAuth() || agent.IsAnthropicAPIKey()
}

func unsupportedModelTransport(provider models.LLMProvider, authMethod models.AuthMethod) error {
	return fmt.Errorf("%s model auth method %q is no longer supported; reconfigure the model to use OAuth or an API key", provider, authMethod)
}

func (a *anthropicProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	_, runtimeAgentDef := resolveAgentRuntime(req.Ctx, req.AgentDefinition)
	if runtimeAgentDef != nil {
		req.AgentDefinition = runtimeAgentDef
	}
	return callProviderOnce(func() (llmcontracts.AgentResult, error) {
		switch req.Operation {
		case llmcontracts.OperationDirect:
			if anthropicAdapterEnabled(req.Agent) {
				return a.adapter.Call(req.Ctx, req, req.WorkDir, nil)
			}
			return llmcontracts.AgentResult{}, unsupportedModelTransport(req.Agent.Provider, req.Agent.AuthMethod)

		case llmcontracts.OperationStreaming:
			if anthropicAdapterEnabled(req.Agent) {
				return a.adapter.Call(req.Ctx, req, req.WorkDir, nil)
			}
			return llmcontracts.AgentResult{}, unsupportedModelTransport(req.Agent.Provider, req.Agent.AuthMethod)

		case llmcontracts.OperationTask:
			if anthropicAdapterEnabled(req.Agent) {
				return a.adapter.Call(req.Ctx, req, req.WorkDir, nil)
			}
			return llmcontracts.AgentResult{}, unsupportedModelTransport(req.Agent.Provider, req.Agent.AuthMethod)
		default:
			return llmcontracts.AgentResult{}, fmt.Errorf("unsupported operation: %s", req.Operation)
		}
	})
}

type openAIProviderAdapter struct {
	svc     *LLMService
	adapter *llmopenai.Adapter
}

func (a *openAIProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	req.Ctx = llmcontracts.WithNativeCompactionStateJSON(req.Ctx, req.NativeCompactionStateJSON)
	_, runtimeAgentDef := resolveAgentRuntime(req.Ctx, req.AgentDefinition)
	if runtimeAgentDef != nil {
		req.AgentDefinition = runtimeAgentDef
	}
	// Apply agent definition: inject system prompt + skill content
	if req.AgentDefinition != nil {
		req.ChatSystemContext = ApplyAgentToSystemPrompt(req.ChatSystemContext, req.AgentDefinition)
		req.ProjectInstructions = ApplyAgentToSystemPrompt(req.ProjectInstructions, req.AgentDefinition)
		if req.AgentDefinition.Model != "" && req.AgentDefinition.Model != "inherit" {
			req.Agent.Model = req.AgentDefinition.Model
		}
	}
	return callProviderOnce(func() (llmcontracts.AgentResult, error) {
		switch req.Operation {
		case llmcontracts.OperationDirect:
			if openAIDirectClientEnabled(req.Agent) {
				output, usage, err := a.adapter.CallDirect(req.Ctx, req.Message, req.Attachments, req.Agent, req.WorkDir, req.ProjectInstructions, req.DisableTools, req.RawDirectPrompt, req.LifecycleHookCall)
				return canonicalResult(output, output, usage, err)
			}
			return llmcontracts.AgentResult{}, unsupportedModelTransport(req.Agent.Provider, req.Agent.AuthMethod)

		case llmcontracts.OperationStreaming:
			if openAIDirectClientEnabled(req.Agent) {
				if requestUsesChatStreaming(req) {
					output, usage, err := a.adapter.CallChatStreaming(req.Ctx, req.Message, req.Attachments, req.Agent, req.ExecID, req.TransportScope, req.ChatHistory, req.ChatSystemContext, req.Followup, req.ChatMode, req.WorkDir, req.AgentDefinition)
					return canonicalResult(output, output, usage, err)
				}
				output, textOnly, usage, err := a.adapter.CallStreaming(req.Ctx, req.Message, req.Attachments, req.Agent, req.ExecID, req.WorkDir, req.ProjectInstructions, req.AgentDefinition)
				return canonicalResult(output, textOnly, usage, err)
			}
			return llmcontracts.AgentResult{}, unsupportedModelTransport(req.Agent.Provider, req.Agent.AuthMethod)

		case llmcontracts.OperationTask:
			if openAIDirectClientEnabled(req.Agent) {
				output, textOnly, usage, err := a.adapter.CallStreaming(req.Ctx, req.Message, req.Attachments, req.Agent, req.ExecID, req.WorkDir, req.ProjectInstructions, req.AgentDefinition)
				return canonicalResult(output, textOnly, usage, err)
			}
			return llmcontracts.AgentResult{}, unsupportedModelTransport(req.Agent.Provider, req.Agent.AuthMethod)
		default:
			return llmcontracts.AgentResult{}, fmt.Errorf("unsupported operation: %s", req.Operation)
		}
	})
}

type openAICompatibleProviderAdapter struct {
	svc     *LLMService
	adapter *llmopenai_compatible.Adapter
}

func (a *openAICompatibleProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	_, runtimeAgentDef := resolveAgentRuntime(req.Ctx, req.AgentDefinition)
	if runtimeAgentDef != nil {
		req.AgentDefinition = runtimeAgentDef
	}
	if req.AgentDefinition != nil {
		req.ChatSystemContext = ApplyAgentToSystemPrompt(req.ChatSystemContext, req.AgentDefinition)
		req.ProjectInstructions = ApplyAgentToSystemPrompt(req.ProjectInstructions, req.AgentDefinition)
		if req.AgentDefinition.Model != "" && req.AgentDefinition.Model != "inherit" {
			req.Agent.Model = req.AgentDefinition.Model
		}
	}
	return callProviderOnce(func() (llmcontracts.AgentResult, error) {
		return a.adapter.Call(req.Ctx, req, req.WorkDir)
	})
}

type ollamaProviderAdapter struct {
	svc     *LLMService
	adapter *llmollama.Adapter
}

func (a *ollamaProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	_, runtimeAgentDef := resolveAgentRuntime(req.Ctx, req.AgentDefinition)
	if runtimeAgentDef != nil {
		req.AgentDefinition = runtimeAgentDef
	}
	// Apply agent definition: inject system prompt + skill content
	if req.AgentDefinition != nil {
		req.ChatSystemContext = ApplyAgentToSystemPrompt(req.ChatSystemContext, req.AgentDefinition)
		req.ProjectInstructions = ApplyAgentToSystemPrompt(req.ProjectInstructions, req.AgentDefinition)
	}
	return callProviderOnce(func() (llmcontracts.AgentResult, error) {
		return a.adapter.Call(req.Ctx, req, req.WorkDir, nil)
	})
}

type mixtureProviderAdapter struct {
	svc *LLMService
}

func (a *mixtureProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	cfg, err := llmmixture.ParseConfig(req.Agent.MixtureConfigJSON)
	if err != nil {
		return llmcontracts.AgentResult{}, err
	}
	aggregator, err := a.resolveMixtureSlot(req.Ctx, cfg.Aggregator)
	if err != nil {
		return llmcontracts.AgentResult{}, fmt.Errorf("mixture aggregator: %w", err)
	}
	if aggregator.Provider == models.ProviderMixture {
		return llmcontracts.AgentResult{}, fmt.Errorf("mixture aggregator cannot use provider %s", aggregator.Provider)
	}
	if !cfg.Enabled {
		aggReq := req
		aggReq.Agent = aggregator
		aggReq.Agent.Temperature = cfg.AggregatorTemperature
		return a.callAggregator(aggReq)
	}

	a.publishMixtureProgress(req, "running_references", 0, len(cfg.ReferenceModels), fmt.Sprintf("Running %d reference models...", len(cfg.ReferenceModels)))
	results := a.runMixtureReferences(req, cfg)
	completed := 0
	for _, result := range results {
		if result.Err == "" {
			completed++
		}
	}
	a.publishMixtureProgress(req, "references_complete", len(results), len(cfg.ReferenceModels), "Reference models complete")
	if err := contextErr(req.Ctx); err != nil {
		return llmcontracts.AgentResult{}, err
	}
	contextBlock := llmmixture.PrivateContext(results)
	aggReq := llmmixture.AppendPrivateContext(req, contextBlock)
	aggReq.Agent = aggregator
	aggReq.Agent.Temperature = cfg.AggregatorTemperature
	a.publishMixtureProgress(req, "aggregator_starting", len(results), len(cfg.ReferenceModels), "Aggregator starting...")
	// applog.Debugf("[mixture] aggregator input provider=%s model=%s message_len=%d message=%q chat_context_len=%d chat_context=%q project_instructions_len=%d project_instructions=%q",
	//	aggReq.Agent.Provider,
	//	aggReq.Agent.Model,
	//	len(aggReq.Message),
	//	aggReq.Message,
	//	len(aggReq.ChatSystemContext),
	//	aggReq.ChatSystemContext,
	//	len(aggReq.ProjectInstructions),
	//	aggReq.ProjectInstructions,
	// )
	res, err := a.callAggregator(aggReq)
	res.Usage = mergeMixtureUsage(results, res.Usage)
	return res, err
}

func (a *mixtureProviderAdapter) resolveMixtureSlot(ctx context.Context, slot llmmixture.ModelSlot) (models.LLMConfig, error) {
	id := strings.TrimSpace(slot.AgentConfigID)
	if id == "" {
		return models.LLMConfig{}, fmt.Errorf("model config id is required")
	}
	if a.svc == nil || a.svc.llmConfigRepo == nil {
		return models.LLMConfig{}, fmt.Errorf("model config repository is unavailable")
	}
	cfg, err := a.svc.llmConfigRepo.GetByID(ctx, id)
	if err != nil {
		return models.LLMConfig{}, err
	}
	if cfg == nil {
		return models.LLMConfig{}, fmt.Errorf("model config not found")
	}
	return *cfg, nil
}

func (a *mixtureProviderAdapter) runMixtureReferences(req llmcontracts.AgentRequest, cfg llmmixture.Config) []llmmixture.ReferenceResult {
	results := make([]llmmixture.ReferenceResult, len(cfg.ReferenceModels))
	if len(cfg.ReferenceModels) == 0 {
		return results
	}
	limit := cfg.MaxReferenceWorkers
	if limit <= 0 || limit > len(cfg.ReferenceModels) {
		limit = len(cfg.ReferenceModels)
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var finishedReferences atomic.Int64
	var progressMu sync.Mutex
	for i, slot := range cfg.ReferenceModels {
		i, slot := i, slot
		results[i] = llmmixture.ReferenceResult{
			Index:    i,
			Label:    llmmixture.SlotLabel(slot, ""),
			Provider: strings.TrimSpace(slot.Provider),
			Model:    strings.TrimSpace(slot.Model),
		}
		if err := contextErr(req.Ctx); err != nil {
			results[i].Err = err.Error()
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = a.callMixtureReference(req, cfg, slot, i)
			progressMu.Lock()
			defer progressMu.Unlock()
			completed := int(finishedReferences.Add(1))
			a.publishMixtureProgress(req, "reference_complete", completed, len(cfg.ReferenceModels), fmt.Sprintf("Reference model %d complete", i+1))
		}()
	}
	wg.Wait()
	return results
}

func (a *mixtureProviderAdapter) callMixtureReference(req llmcontracts.AgentRequest, cfg llmmixture.Config, slot llmmixture.ModelSlot, index int) llmmixture.ReferenceResult {
	result := llmmixture.ReferenceResult{Index: index, Label: llmmixture.SlotLabel(slot, ""), Provider: strings.TrimSpace(slot.Provider), Model: strings.TrimSpace(slot.Model)}
	resolved, err := a.resolveMixtureSlot(req.Ctx, slot)
	if err != nil {
		result.Err = "model config not found"
		return result
	}
	result.Label = llmmixture.SlotLabel(slot, resolved.Name)
	result.Provider = string(resolved.Provider)
	result.Model = resolved.Model
	if resolved.Provider == models.ProviderMixture {
		result.Err = "recursive mixture model is not allowed"
		return result
	}
	adapter, ok := a.svc.adapterFor(resolved.Provider)
	if !ok {
		result.Err = fmt.Sprintf("no adapter for provider %s", resolved.Provider)
		return result
	}
	refCtx := llmcontracts.WithoutRuntimeTools(req.Ctx)
	refCtx, cancel := context.WithTimeout(refCtx, time.Duration(cfg.ReferenceTimeoutSeconds)*time.Second)
	defer cancel()
	refReq := req
	refReq.Ctx = refCtx
	refReq.Agent = resolved
	refReq.Agent.Temperature = cfg.ReferenceTemperature
	refReq.Operation = llmcontracts.OperationDirect
	refReq.DisableTools = true
	refReq.RawDirectPrompt = true
	refReq.ExecID = ""
	refReq.AgentDefinition = nil
	refReq.ProjectInstructions = ""
	refReq.ChatSystemContext = ""
	refReq.Attachments = nil
	refReq.Message = llmmixture.ReferencePrompt(req.Message, req.ChatHistory)
	res, err := adapter.Call(refReq)
	if err != nil {
		result.Err = err.Error()
		if refCtx.Err() != nil {
			result.Err = refCtx.Err().Error()
		}
		return result
	}
	result.Output = res.TextOnlyOutput
	if result.Output == "" {
		result.Output = res.Output
	}
	// applog.Debugf("[mixture] reference %d output provider=%s model=%s len=%d output=%q",
	//	index+1, result.Provider, result.Model, len(result.Output), result.Output)
	result.Usage = res.Usage
	return result
}

func (a *mixtureProviderAdapter) callAggregator(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	adapter, ok := a.svc.adapterFor(req.Agent.Provider)
	if !ok {
		return llmcontracts.AgentResult{}, fmt.Errorf("no adapter for mixture aggregator provider %s", req.Agent.Provider)
	}
	return adapter.Call(req)
}

func (a *mixtureProviderAdapter) publishMixtureProgress(req llmcontracts.AgentRequest, phase string, completed, total int, message string) {
	if a == nil || a.svc == nil || a.svc.broadcaster == nil || strings.TrimSpace(req.ExecID) == "" {
		return
	}
	a.svc.broadcaster.Publish(events.TaskEvent{
		Type:                events.MixtureProgress,
		ProjectID:           req.ProjectID,
		ExecID:              req.ExecID,
		Phase:               phase,
		TotalReferences:     total,
		CompletedReferences: completed,
		Message:             message,
	})
}

func mergeMixtureUsage(results []llmmixture.ReferenceResult, aggregator llmcontracts.Usage) llmcontracts.Usage {
	merged := aggregator
	if merged.ProviderRaw == nil {
		merged.ProviderRaw = map[string]int{}
	}
	merged.ProviderRaw["aggregator_total_tokens"] = aggregator.TotalTokens
	for i, result := range results {
		merged.InputTokens += result.Usage.InputTokens
		merged.OutputTokens += result.Usage.OutputTokens
		merged.CachedInputTokens += result.Usage.CachedInputTokens
		merged.ReasoningTokens += result.Usage.ReasoningTokens
		merged.TotalTokens += result.Usage.TotalTokens
		merged.ProviderRaw[fmt.Sprintf("reference_%d_total_tokens", i+1)] = result.Usage.TotalTokens
	}
	return merged
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

type testProviderAdapter struct {
	svc *LLMService
}

type agentRequestRecorder interface {
	RecordAgentRequest(req llmcontracts.AgentRequest)
}

func (a *testProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	if a.svc.llmCaller == nil {
		return llmcontracts.AgentResult{}, fmt.Errorf("test provider requires LLMCaller to be set via SetLLMCaller()")
	}
	if recorder, ok := a.svc.llmCaller.(agentRequestRecorder); ok {
		recorder.RecordAgentRequest(req)
	}
	output, textOnly, tokens, err := a.svc.llmCaller.CallModel(req.Ctx, req.Message, req.Attachments, req.Agent, req.ExecID, req.WorkDir)
	return canonicalResult(output, textOnly, llmusage.FromTotal(tokens), err)
}

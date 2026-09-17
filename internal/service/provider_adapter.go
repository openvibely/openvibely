package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	llmstream "github.com/openvibely/openvibely/internal/llm/stream"
	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	llmusage "github.com/openvibely/openvibely/internal/llm/usage"
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
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
	// Structured stop reasons are categorized by concrete provider adapters.
	// Keep the legacy prefix check only for adapters that cannot expose structure.
	if err != nil && (llmcontracts.ErrorIs(err, llmcontracts.ErrorOutputTokenLimitReached) || strings.HasPrefix(err.Error(), "response truncated: max")) {
		res.StopReason = "max_tokens"
	}
	return res, err
}

func callProviderOnce(fn func() (llmcontracts.AgentResult, error)) (llmcontracts.AgentResult, error) {
	// Provider transports own retry decisions because they know whether a
	// streamed attempt has emitted output. Replaying here could duplicate a
	// partial turn and would multiply the provider's bounded retry budget.
	res, err := fn()
	return res, categorizeProviderError(err)
}

func categorizeProviderError(err error) error {
	if err == nil {
		return nil
	}
	var categorized *llmcontracts.CategorizedError
	if errors.As(err, &categorized) {
		return err
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "max output tokens") || strings.Contains(msg, "max_tokens limit") || strings.Contains(msg, "output budget") {
		return llmcontracts.NewCategorizedError(llmcontracts.ErrorOutputTokenLimitReached, "provider response", err)
	}
	if strings.Contains(msg, "compaction") && (strings.Contains(msg, "unsupported") || strings.Contains(msg, "not available") || strings.Contains(msg, "unknown beta")) {
		return llmcontracts.NewCategorizedError(llmcontracts.ErrorNativeCompactionUnsupported, "native compaction", err)
	}
	for _, needle := range []string{"context length", "context_length", "context window", "maximum context", "too many tokens", "input is too long", "prompt is too long"} {
		if strings.Contains(msg, needle) {
			return llmcontracts.NewCategorizedError(llmcontracts.ErrorContextWindowExceeded, "provider request", err)
		}
	}
	if strings.Contains(msg, "compaction") || strings.Contains(msg, "context management") || strings.Contains(msg, "context-management") {
		return llmcontracts.NewCategorizedError(llmcontracts.ErrorNativeCompactionFailed, "native compaction", err)
	}
	for _, needle := range []string{"connection reset", "connection refused", "broken pipe", "unexpected eof", "stream read", "websocket"} {
		if strings.Contains(msg, needle) {
			return llmcontracts.NewCategorizedError(llmcontracts.ErrorTransportFailure, "provider transport", err)
		}
	}
	return err
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
	defaultReservedOutputTokens          = 16384
	pendingInputSampleBytes              = 1024
	maxArtifactReadBytes                 = 65536
	oversizedInputReaderTool             = "read_input_artifact"
)

type requestBudget struct {
	ContextWindow        int
	SafeInputLimit       int
	ReservedOutputTokens int
	SafetyMargin         int
	FixedTokens          int
	HistoryTokens        int
	NativeStateTokens    int
	PendingTokens        int
	AttachmentTokens     int
}

func (b requestBudget) TotalInputTokens() int {
	return b.FixedTokens + b.HistoryTokens + b.NativeStateTokens + b.PendingTokens + b.AttachmentTokens
}

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
	if llmcontracts.ErrorIs(err, llmcontracts.ErrorContextWindowExceeded) || llmcontracts.ErrorIs(err, llmcontracts.ErrorCompactionInputInfeasible) {
		return true
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
	if llmcontracts.ErrorIs(err, llmcontracts.ErrorNativeCompactionFailed) || llmcontracts.ErrorIs(err, llmcontracts.ErrorNativeCompactionUnsupported) {
		return true
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
	if llmcontracts.ErrorIs(err, llmcontracts.ErrorNativeCompactionUnsupported) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unsupported") || strings.Contains(msg, "unknown beta") || strings.Contains(msg, "beta feature") || strings.Contains(msg, "not available")
}

func providerSupportsNativeCompaction(agent models.LLMConfig) bool {
	if strings.TrimSpace(agent.Model) == "" || agent.AuthMethod == models.AuthMethodCLI {
		return false
	}
	if agent.AuthMethod != "" && agent.AuthMethod != models.AuthMethodAPIKey && agent.AuthMethod != models.AuthMethodOAuth {
		return false
	}
	switch agent.Provider {
	case models.ProviderOpenAI:
		return true
	case models.ProviderAnthropic:
		// Anthropic context management is a Messages capability. A transport
		// override denotes a different concrete protocol and must not inherit it.
		return strings.TrimSpace(agent.Transport) == "" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(agent.Model)), "claude-")
	default:
		return false
	}
}

func providerCompatibilityKey(agent models.LLMConfig) string {
	authIdentity := agent.APIKey
	if agent.AuthMethod == models.AuthMethodOAuth {
		authIdentity = strings.Join([]string{agent.OAuthConnectionID, agent.OAuthAccountID}, "|")
	}
	identity := strings.Join([]string{
		string(agent.Provider), strings.ToLower(strings.TrimSpace(agent.Model)),
		strings.TrimRight(strings.TrimSpace(agent.BaseURL), "/"), strings.TrimRight(strings.TrimSpace(agent.OllamaBaseURL), "/"),
		strings.ToLower(strings.TrimSpace(agent.Transport)), string(agent.AuthMethod), authIdentity,
	}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

func nativeCompactionSessionKey(agent models.LLMConfig) string {
	return providerCompatibilityKey(agent)
}

func providerTransport(req llmcontracts.AgentRequest) string {
	if transport := strings.TrimSpace(req.Agent.GetTransport()); transport != "" {
		return transport
	}
	switch req.Agent.Provider {
	case models.ProviderOpenAI:
		switch strings.ToLower(strings.TrimSpace(req.Agent.Model)) {
		case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-6-astra":
			return "responses_websocket_http_fallback"
		default:
			return "responses_http"
		}
	case models.ProviderAnthropic:
		return "anthropic_messages_http"
	case models.ProviderOllama:
		return "ollama_http"
	case models.ProviderTest:
		return "test"
	default:
		return "unknown"
	}
}

func historyRetentionCounts(original, final []models.Execution) (retained, removed int) {
	if len(original) == len(final) && (len(original) == 0 || &original[0] == &final[0]) {
		return len(final), 0
	}
	matched := make([]bool, len(final))
	for _, source := range original {
		for i, candidate := range final {
			if matched[i] {
				continue
			}
			same := source.ID != "" && source.ID == candidate.ID
			if source.ID == "" && candidate.ID == "" {
				same = source.PromptSent == candidate.PromptSent && source.Output == candidate.Output && source.ReasoningContent == candidate.ReasoningContent
			}
			if same {
				matched[i] = true
				retained++
				break
			}
		}
	}
	return retained, max(0, len(original)-retained)
}

func contextFailureCategory(err error) string {
	category := llmcontracts.ErrorCategoryOf(err)
	if category == "" {
		return "none"
	}
	return string(category)
}

func logContextDecision(originalHistory []models.Execution, req llmcontracts.AgentRequest, strategy string, externalized bool, trigger error) {
	logContextDecisionWithBudget(originalHistory, req, strategy, externalized, trigger, calculateRequestBudget(req))
}

func logContextDecisionWithBudget(originalHistory []models.Execution, req llmcontracts.AgentRequest, strategy string, externalized bool, trigger error, budget requestBudget) {
	retained, removed := historyRetentionCounts(originalHistory, req.ChatHistory)
	applog.Infof("[agent-svc] context decision provider=%s model=%s transport=%s context_window=%d safe_input_limit=%d fixed_tokens=%d history_tokens=%d pending_tokens=%d attachment_tokens=%d reserved_output_tokens=%d safety_margin=%d strategy=%s externalized=%t history_retained=%d history_removed=%d retry_source_execution_id=%s failure_category=%s", req.Agent.Provider, req.Agent.Model, providerTransport(req), budget.ContextWindow, budget.SafeInputLimit, budget.FixedTokens, budget.HistoryTokens+budget.NativeStateTokens, budget.PendingTokens, budget.AttachmentTokens, budget.ReservedOutputTokens, budget.SafetyMargin, strategy, externalized, retained, removed, req.RetrySourceExecutionID, contextFailureCategory(trigger))
}

func logContextFailure(req llmcontracts.AgentRequest, err error) {
	logContextFailureWithBudget(req, err, calculateRequestBudget(req))
}

func logContextFailureWithBudget(req llmcontracts.AgentRequest, err error, budget requestBudget) {
	category := llmcontracts.ErrorCategoryOf(err)
	if category == "" {
		return
	}
	externalized := false
	if rt := llmcontracts.RuntimeToolsFromContext(req.Ctx); rt != nil {
		externalized = rt.HasDefinition(oversizedInputReaderTool)
	}
	applog.Infof("[agent-svc] context failure provider=%s model=%s transport=%s context_window=%d safe_input_limit=%d fixed_tokens=%d history_tokens=%d pending_tokens=%d attachment_tokens=%d reserved_output_tokens=%d safety_margin=%d strategy=failed externalized=%t history_retained=%d history_removed=0 retry_source_execution_id=%s failure_category=%s", req.Agent.Provider, req.Agent.Model, providerTransport(req), budget.ContextWindow, budget.SafeInputLimit, budget.FixedTokens, budget.HistoryTokens+budget.NativeStateTokens, budget.PendingTokens, budget.AttachmentTokens, budget.ReservedOutputTokens, budget.SafetyMargin, externalized, len(req.ChatHistory), req.RetrySourceExecutionID, category)
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
		case models.ProviderOllama:
			window = llmollama.DefaultContextWindow
		default:
			window = defaultOpenAIContextWindow
		}
	}
	autoLimit := (window * 90) / 100
	effectiveHardLimit := (window * 95) / 100
	if agent.Provider == models.ProviderAnthropic {
		autoLimit = anthropicclient.CompactionTriggerLimit(window)
		effectiveHardLimit = anthropicclient.CompactionBlockingLimit(window)
	}
	triggerLimit := autoLimit
	configuredThreshold := agent.CompactionThreshold
	if agent.Provider == models.ProviderAnthropic && configuredThreshold > 0 && configuredThreshold < anthropicclient.MinCompactionThreshold {
		configuredThreshold = anthropicclient.MinCompactionThreshold
	}
	if configuredThreshold > 0 && configuredThreshold < triggerLimit {
		triggerLimit = configuredThreshold
	}
	return compactionLimits{ContextWindow: window, AutoLimit: autoLimit, TriggerLimit: triggerLimit, EffectiveHardLimit: effectiveHardLimit}
}

func requestBudgetForAgent(agent models.LLMConfig) requestBudget {
	limits := compactionLimitsForAgent(agent)
	reserved := agent.GetDefaultMaxTokens(defaultReservedOutputTokens)
	safety := limits.ContextWindow / 50
	switch agent.Provider {
	case models.ProviderAnthropic:
		reserved = anthropicclient.CompactionOutputReserve(llmanthropic.OutputTokenBudget(agent.Model))
		safety = limits.ContextWindow - limits.EffectiveHardLimit - reserved
	case models.ProviderOllama:
		reserved = agent.GetDefaultMaxTokens(4096)
	}
	if limits.ContextWindow < 32768 && reserved > limits.ContextWindow/4 {
		// Keep deliberately tiny synthetic/test windows useful while preserving
		// the concrete provider reservation for production-sized windows.
		reserved = limits.ContextWindow / 4
	}
	if reserved < 1 {
		reserved = 1
	}
	if safety < 64 {
		safety = 64
	}
	hardInput := limits.ContextWindow - reserved - safety
	if hardInput < 1 {
		hardInput = 1
	}
	safe := hardInput
	if agent.Provider != models.ProviderAnthropic && limits.TriggerLimit > 0 && limits.TriggerLimit < safe {
		safe = limits.TriggerLimit
	}
	return requestBudget{ContextWindow: limits.ContextWindow, SafeInputLimit: safe, ReservedOutputTokens: reserved, SafetyMargin: safety}
}

func estimateDefaultProviderToolTokens(provider models.LLMProvider) int {
	var defs any
	switch provider {
	case models.ProviderAnthropic:
		defs = anthropicclient.DefaultTools()
	case models.ProviderOpenAI, models.ProviderOpenAICompatible:
		defs = openaiclient.DefaultTools()
	default:
		return 0
	}
	encoded, err := json.Marshal(defs)
	if err != nil {
		return 0
	}
	return estimatedUTF8Tokens(string(encoded))
}

func calculateRequestBudget(req llmcontracts.AgentRequest) requestBudget {
	budget := requestBudgetForAgent(req.Agent)
	fixedReq := req
	fixedReq.Message = ""
	fixedReq.ChatHistory = nil
	fixedReq.NativeCompactionStateJSON = ""
	fixedReq.Attachments = nil
	budget.FixedTokens = estimateModelVisibleRequestTokens(fixedReq)
	rt := llmcontracts.RuntimeToolsFromContext(req.Ctx)
	if !req.DisableTools && (req.AgentDefinition == nil || !req.AgentDefinition.ToolConfig.SkipDefaultTools) && !llmcontracts.RuntimeSkipDefaultTools(rt) {
		budget.FixedTokens += estimateDefaultProviderToolTokens(req.Agent.Provider)
	}
	budget.PendingTokens = estimatedUTF8Tokens(req.Message)
	budget.NativeStateTokens = estimatedUTF8Tokens(req.NativeCompactionStateJSON)
	for _, exec := range req.ChatHistory {
		budget.HistoryTokens += estimateExecutionTokens(exec)
	}
	for _, att := range req.Attachments {
		budget.AttachmentTokens += estimatedUTF8Tokens(att.FileName) + estimatedUTF8Tokens(att.MediaType) + estimatedUTF8Tokens(att.FilePath)
		if att.FileSize > 0 {
			budget.AttachmentTokens += tokenestimate.FromByteCount(int(att.FileSize))
		}
	}
	return budget
}

func estimateExecutionTokens(exec models.Execution) int {
	total := estimatedUTF8Tokens(exec.PromptSent) + estimatedUTF8Tokens(exec.Output) + estimatedUTF8Tokens(exec.ErrorMessage) + estimatedUTF8Tokens(exec.ReasoningContent)
	for _, replay := range exec.ReplayMessages {
		total += estimatedUTF8Tokens(replay.UserContent) + estimatedUTF8Tokens(replay.AssistantContent) + estimatedUTF8Tokens(replay.ReasoningContent) + estimatedUTF8Tokens(replay.TranscriptJSON)
	}
	return total
}

func historyNeedsCompaction(b requestBudget, triggerLimit int) bool {
	if b.HistoryTokens+b.NativeStateTokens == 0 {
		return false
	}
	if b.FixedTokens+b.HistoryTokens+b.NativeStateTokens >= triggerLimit {
		return true
	}
	// If pending input cannot fit by itself, compaction cannot solve that
	// condition. Externalize or reject it before considering combined pressure.
	if pendingInputInfeasible(b) {
		return false
	}
	return b.TotalInputTokens() >= triggerLimit && b.HistoryTokens+b.NativeStateTokens >= max(256, triggerLimit/20)
}

func pendingInputInfeasible(b requestBudget) bool {
	return b.FixedTokens+b.PendingTokens+b.AttachmentTokens > b.SafeInputLimit
}

func providerSupportsArtifactReader(req llmcontracts.AgentRequest) bool {
	if strings.TrimSpace(req.WorkDir) == "" || req.DisableTools {
		return false
	}
	switch req.Agent.Provider {
	case models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible:
		return true
	default:
		return false
	}
}

func artifactReaderRuntime(path string) *llmcontracts.RuntimeTools {
	return &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{
			Name: oversizedInputReaderTool, Access: llmcontracts.RuntimeToolAccessRead,
			Description: "Read one bounded byte range from the oversized current input artifact. Use targeted offsets and never request the entire file.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":65536}},"required":["offset","limit"],"additionalProperties":false}`),
		}},
		Executor: func(_ context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			if name != oversizedInputReaderTool {
				return "", false, false, nil
			}
			var args struct {
				Offset int64 `json:"offset"`
				Limit  int   `json:"limit"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", true, true, err
			}
			if args.Offset < 0 || args.Limit < 1 || args.Limit > maxArtifactReadBytes {
				return "", true, true, fmt.Errorf("offset must be non-negative and limit must be between 1 and %d", maxArtifactReadBytes)
			}
			file, err := os.Open(path)
			if err != nil {
				return "", true, true, err
			}
			defer file.Close()
			if _, err := file.Seek(args.Offset, io.SeekStart); err != nil {
				return "", true, true, err
			}
			buf := make([]byte, args.Limit)
			n, err := file.Read(buf)
			if err != nil && err != io.EOF {
				return "", true, true, err
			}
			return string(buf[:n]), true, false, nil
		},
		Filter: func(name string) (bool, bool) {
			if name == oversizedInputReaderTool {
				return true, true
			}
			return false, false
		},
	}
}

func (s *LLMService) preparePendingInput(req llmcontracts.AgentRequest) (llmcontracts.AgentRequest, bool, func(), error) {
	return s.preparePendingInputWithBudget(req, calculateRequestBudget(req))
}

func (s *LLMService) preparePendingInputWithBudget(req llmcontracts.AgentRequest, budget requestBudget) (llmcontracts.AgentRequest, bool, func(), error) {
	if !pendingInputInfeasible(budget) {
		return req, false, func() {}, nil
	}
	if !providerSupportsArtifactReader(req) {
		return req, false, func() {}, llmcontracts.NewCategorizedError(llmcontracts.ErrorPendingInputInfeasible, "request preflight", fmt.Errorf("pending input requires %d tokens but safe input limit is %d and no authorized artifact reader is available", budget.PendingTokens, budget.SafeInputLimit))
	}
	root := ""
	if s != nil {
		root = strings.TrimSpace(s.globalSkillRoot)
	}
	if root == "" {
		root = strings.TrimSpace(os.Getenv("OPENVIBELY_APP_DATA_DIR"))
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return req, false, func() {}, llmcontracts.NewCategorizedError(llmcontracts.ErrorPendingInputInfeasible, "artifact root", err)
		}
		root = filepath.Join(home, ".openvibely")
	}
	id := strings.TrimSpace(req.ExecID)
	if id == "" || filepath.Base(id) != id || id == "." || id == ".." {
		return req, false, func() {}, llmcontracts.NewCategorizedError(llmcontracts.ErrorPendingInputInfeasible, "artifact identity", fmt.Errorf("execution id is unavailable or unsafe"))
	}
	dir := filepath.Join(root, "task-inputs", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return req, false, func() {}, llmcontracts.NewCategorizedError(llmcontracts.ErrorPendingInputInfeasible, "create input artifact", err)
	}
	cleanup := func() {
		if err := os.RemoveAll(dir); err != nil {
			applog.Infof("[agent-svc] cleanup oversized input artifact exec=%s: %v", id, err)
		}
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		cleanup()
		return req, false, func() {}, llmcontracts.NewCategorizedError(llmcontracts.ErrorPendingInputInfeasible, "secure input artifact directory", err)
	}
	path := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(path, []byte(req.Message), 0o600); err != nil {
		cleanup()
		return req, false, func() {}, llmcontracts.NewCategorizedError(llmcontracts.ErrorPendingInputInfeasible, "write input artifact", err)
	}
	original := req.Message
	lines := 0
	if original != "" {
		lines = strings.Count(original, "\n") + 1
	}
	head := takePrefixBytes([]rune(original), pendingInputSampleBytes)
	tail := takeSuffixBytes([]rune(original), pendingInputSampleBytes)
	req.Ctx = llmcontracts.WithRuntimeTools(req.Ctx, llmcontracts.CompositeRuntimeTools(artifactReaderRuntime(path), llmcontracts.RuntimeToolsFromContext(req.Ctx)))
	req.Message = fmt.Sprintf("The user supplied an oversized input. The complete original remains stored in the execution record.\n\nFull input:\n  %s\n\nBytes:\n  %d\n\nLines:\n  %d\n\nUse %s with targeted offsets and bounded reads. Do not load the complete artifact into a single model request.\n\nBeginning sample:\n%s\n\nEnding sample:\n%s", path, len([]byte(original)), lines, oversizedInputReaderTool, head, tail)
	return req, true, cleanup, nil
}

func ensureRequestFits(req llmcontracts.AgentRequest, op string) error {
	return ensureRequestFitsWithBudget(req, calculateRequestBudget(req), op)
}

func ensureRequestFitsWithBudget(req llmcontracts.AgentRequest, budget requestBudget, op string) error {
	if budget.TotalInputTokens() <= budget.SafeInputLimit {
		return nil
	}
	return llmcontracts.NewCategorizedError(llmcontracts.ErrorContextWindowExceeded, op, fmt.Errorf("complete request requires %d input tokens; safe limit is %d (fixed=%d history=%d native=%d pending=%d attachments=%d reserve=%d safety=%d)", budget.TotalInputTokens(), budget.SafeInputLimit, budget.FixedTokens, budget.HistoryTokens, budget.NativeStateTokens, budget.PendingTokens, budget.AttachmentTokens, budget.ReservedOutputTokens, budget.SafetyMargin))
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
	case "gpt-5.3-codex-spark":
		return 128000
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
			total += tokenestimate.FromByteCount(int(att.FileSize))
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

func resolveProviderRequestForBudget(req llmcontracts.AgentRequest) llmcontracts.AgentRequest {
	if req.ProviderRuntimeResolved {
		return req
	}
	_, runtimeAgentDef := resolveAgentRuntime(req.Ctx, req.AgentDefinition)
	if runtimeAgentDef != nil {
		req.AgentDefinition = runtimeAgentDef
		if model := strings.TrimSpace(runtimeAgentDef.Model); model != "" && model != "inherit" {
			req.Agent.Model = model
		}
	}
	req.ProviderRuntimeResolved = true
	return req
}

func (s *LLMService) callProviderWithCompaction(adapter ProviderAdapter, req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	req = resolveProviderRequestForBudget(req)
	// Mixture is virtual. Its adapter resolves concrete reference and aggregator
	// models, then re-enters this budget boundary for each pinned route.
	if req.Agent.Provider == models.ProviderMixture {
		return adapter.Call(req)
	}
	if contextCompactionFallbackDisabled(req.Ctx) {
		budget := calculateRequestBudget(req)
		if err := ensureRequestFitsWithBudget(req, budget, "provider request"); err != nil {
			return llmcontracts.AgentResult{}, err
		}
		return adapter.Call(req)
	}
	uncompactedReq := req
	originalHistory := req.ChatHistory
	req = s.restoreCompactionCheckpoint(req)
	compactionStrategy := "none"
	if req.NativeCompactionStateJSON != "" {
		compactionStrategy = "provider_native_checkpoint"
	} else if len(req.ChatHistory) != len(originalHistory) {
		compactionStrategy = "textual_checkpoint"
	}
	lastResortBaseReq := req
	locallyCompactedBeforeProvider := false
	budget := calculateRequestBudget(req)
	if req.ContextTokenEstimate > 0 {
		reportedHistory := req.ContextTokenEstimate - budget.FixedTokens - budget.PendingTokens - budget.AttachmentTokens
		if reportedHistory > budget.HistoryTokens {
			budget.HistoryTokens = reportedHistory
		}
	}
	limits := compactionLimitsForAgent(req.Agent)
	if budget.SafeInputLimit < limits.TriggerLimit {
		limits.TriggerLimit = budget.SafeInputLimit
	}
	historyPressure := historyNeedsCompaction(budget, limits.TriggerLimit)
	triggered, used := historyPressure, budget.TotalInputTokens()
	if limits.TriggerLimit > 0 {
		req.NativeCompactionTokenThreshold = limits.TriggerLimit
		req.Agent.CompactionThreshold = limits.TriggerLimit
	}
	if providerSupportsNativeCompaction(req.Agent) && knownNativeCompactionUnsupported(req.Agent) {
		req.DisableNativeCompaction = true
		req.Agent.DisableNativeCompaction = true
	}
	if triggered && providerSupportsNativeCompaction(req.Agent) && !req.DisableNativeCompaction && !req.Agent.DisableNativeCompaction {
		req.ForceNativeCompaction = true
		req.Agent.ForceNativeCompaction = true
		compactionStrategy = "provider_native"
	}
	if triggered && shouldUseLocalSummaryBeforeProviderWithBudget(req, budget) {
		if req.NativeCompactionStateJSON != "" {
			req.ChatHistory = uncompactedReq.ChatHistory
			req.NativeCompactionStateJSON = ""
		}
		originalReq := req
		compacted, compactErr := s.compactRequestHistoryWithLocalSummary(adapter, req)
		if compactErr != nil {
			applog.Infof("[agent-svc] proactive context compaction failed provider=%s model=%s tokens=%d trigger=%d failure_category=%s", req.Agent.Provider, req.Agent.Model, used, limits.TriggerLimit, contextFailureCategory(compactErr))
			return s.callProviderWithLastResortTruncation(adapter, req, compactErr)
		}
		s.persistCompactionCheckpoint(originalReq, compacted, historyCompactionSummary(compacted.ChatHistory), "local_summary")
		lastResortBaseReq = originalReq
		req = compacted
		budget = calculateRequestBudget(req)
		req.ForceNativeCompaction = false
		req.Agent.ForceNativeCompaction = false
		compactionStrategy = "local_summary"
		locallyCompactedBeforeProvider = true
		applog.Infof("[agent-svc] proactive local context compaction provider=%s model=%s tokens=%d trigger=%d history=%d compacted_history=%d", req.Agent.Provider, req.Agent.Model, used, limits.TriggerLimit, len(originalReq.ChatHistory), len(req.ChatHistory))
	}
	var externalized bool
	var prepErr error
	var cleanupArtifact func()
	req, externalized, cleanupArtifact, prepErr = s.preparePendingInputWithBudget(req, budget)
	if prepErr != nil {
		logContextFailureWithBudget(req, prepErr, budget)
		return llmcontracts.AgentResult{}, prepErr
	}
	defer cleanupArtifact()
	if externalized {
		budget = calculateRequestBudget(req)
	}
	openAIHistoryOnlyCompaction := req.Agent.Provider == models.ProviderOpenAI && (req.ForceNativeCompaction || req.Agent.ForceNativeCompaction)
	if !openAIHistoryOnlyCompaction {
		if err := ensureRequestFitsWithBudget(req, budget, "provider request"); err != nil {
			logContextFailureWithBudget(req, err, budget)
			return llmcontracts.AgentResult{}, err
		}
	} else if pendingInputInfeasible(budget) {
		err := llmcontracts.NewCategorizedError(llmcontracts.ErrorPendingInputInfeasible, "native compaction continuation", fmt.Errorf("pending request remains infeasible after preprocessing"))
		logContextFailureWithBudget(req, err, budget)
		return llmcontracts.AgentResult{}, err
	}
	logContextDecisionWithBudget(originalHistory, req, compactionStrategy, externalized, nil, budget)
	res, err := adapter.Call(req)
	err = categorizeProviderError(err)
	if err != nil {
		logContextFailureWithBudget(req, err, budget)
	}
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
		applog.Infof("[agent-svc] compacted provider retry exceeded context; failure_category=%s", contextFailureCategory(err))
		return s.callProviderWithLastResortTruncation(adapter, lastResortBaseReq, err)
	}
	if providerSupportsNativeCompaction(req.Agent) && (nativeCompactionFailure(err) || recognizedContextLengthError(err)) && len(req.ChatHistory) > 0 {
		if nativeCompactionUnsupportedError(err) {
			knownUnsupportedNativeCompaction.Store(nativeCompactionSessionKey(req.Agent), true)
		}
		compactedReq := req
		compactedReq.DisableNativeCompaction = true
		compactedReq.Agent.DisableNativeCompaction = true
		compacted, compactErr := s.compactRequestHistoryWithLocalSummary(adapter, compactedReq, err)
		if compactErr != nil {
			applog.Infof("[agent-svc] native-to-local context compaction fallback failed; failure_category=%s", contextFailureCategory(compactErr))
			return s.callProviderWithLastResortTruncation(adapter, req, compactErr)
		}
		s.persistCompactionCheckpoint(req, compacted, historyCompactionSummary(compacted.ChatHistory), "local_summary")
		applog.Infof("[agent-svc] retrying provider call once after native-to-local context compaction provider=%s model=%s history=%d compacted_history=%d", req.Agent.Provider, req.Agent.Model, len(req.ChatHistory), len(compacted.ChatHistory))
		return s.callCompactedRetryOrLastResort(adapter, req, compacted, err)
	}
	if recognizedContextLengthError(err) && len(req.ChatHistory) > 0 {
		compacted, compactErr := s.compactRequestHistoryWithLocalSummary(adapter, req, err)
		if compactErr != nil {
			applog.Infof("[agent-svc] context compaction fallback failed; failure_category=%s", contextFailureCategory(compactErr))
			return s.callProviderWithLastResortTruncation(adapter, req, compactErr)
		}
		s.persistCompactionCheckpoint(req, compacted, historyCompactionSummary(compacted.ChatHistory), "local_summary")
		applog.Infof("[agent-svc] retrying provider call once after local context compaction provider=%s model=%s history=%d compacted_history=%d", req.Agent.Provider, req.Agent.Model, len(req.ChatHistory), len(compacted.ChatHistory))
		return s.callCompactedRetryOrLastResort(adapter, req, compacted, err)
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
	if strings.TrimSpace(checkpoint.ProviderStateJSON) != "" && checkpoint.CompatibilityKey != providerCompatibilityKey(req.Agent) {
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
		CompatibilityKey:  providerCompatibilityKey(req.Agent),
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
		CompatibilityKey:  providerCompatibilityKey(originalReq.Agent),
		SourceExecutionID: sourceID, History: compactedReq.ChatHistory,
		Summary:  strings.TrimSpace(summary),
		Strategy: strategy,
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
	return shouldUseLocalSummaryBeforeProviderWithBudget(req, calculateRequestBudget(req))
}

func shouldUseLocalSummaryBeforeProviderWithBudget(req llmcontracts.AgentRequest, budget requestBudget) bool {
	if len(req.ChatHistory) == 0 {
		return false
	}
	if !providerSupportsNativeCompaction(req.Agent) {
		return true
	}
	if req.Agent.Provider == models.ProviderAnthropic && budget.TotalInputTokens() > budget.SafeInputLimit {
		// Anthropic context_management is part of the ordinary complete request,
		// not a separate history-only compaction request. Reduce history locally
		// before dispatch when that complete request is already unsafe.
		return true
	}
	return req.DisableNativeCompaction || req.Agent.DisableNativeCompaction || knownNativeCompactionUnsupported(req.Agent)
}

func knownNativeCompactionUnsupported(agent models.LLMConfig) bool {
	_, ok := knownUnsupportedNativeCompaction.Load(nativeCompactionSessionKey(agent))
	return ok
}

func (s *LLMService) callCompactedRetryOrLastResort(adapter ProviderAdapter, originalReq, compactedReq llmcontracts.AgentRequest, trigger error) (llmcontracts.AgentResult, error) {
	compactedReq.ForceNativeCompaction = false
	compactedReq.Agent.ForceNativeCompaction = false
	budget := calculateRequestBudget(compactedReq)
	prepared, externalized, cleanupArtifact, prepErr := s.preparePendingInputWithBudget(compactedReq, budget)
	if prepErr != nil {
		logContextFailureWithBudget(compactedReq, prepErr, budget)
		return llmcontracts.AgentResult{}, prepErr
	}
	defer cleanupArtifact()
	if externalized {
		budget = calculateRequestBudget(prepared)
	}
	if err := ensureRequestFitsWithBudget(prepared, budget, "compacted retry"); err != nil {
		logContextFailureWithBudget(prepared, err, budget)
		return s.callProviderWithLastResortTruncation(adapter, originalReq, err)
	}
	logContextDecisionWithBudget(originalReq.ChatHistory, prepared, "local_summary_retry", externalized, trigger, budget)
	res, err := adapter.Call(prepared)
	err = categorizeProviderError(err)
	if err != nil {
		logContextFailureWithBudget(prepared, err, budget)
	}
	if err == nil || !recognizedContextLengthError(err) {
		return res, err
	}
	applog.Infof("[agent-svc] compacted provider retry exceeded context; failure_category=%s", contextFailureCategory(err))
	return s.callProviderWithLastResortTruncation(adapter, originalReq, err)
}

func (s *LLMService) callProviderWithLastResortTruncation(adapter ProviderAdapter, req llmcontracts.AgentRequest, cause error) (llmcontracts.AgentResult, error) {
	budget := calculateRequestBudget(req)
	truncated, externalized, cleanupArtifact, prepErr := s.preparePendingInputWithBudget(req, budget)
	if prepErr != nil {
		logContextFailureWithBudget(req, prepErr, budget)
		return llmcontracts.AgentResult{}, prepErr
	}
	defer cleanupArtifact()
	if externalized {
		budget = calculateRequestBudget(truncated)
	}
	truncated.ForceNativeCompaction = false
	truncated.Agent.ForceNativeCompaction = false
	truncated.DisableNativeCompaction = true
	truncated.Agent.DisableNativeCompaction = true
	truncated.ChatHistory = historyWithinRequestBudget(truncated, llmprompt.LimitChatHistory(req.ChatHistory))
	budget = calculateRequestBudget(truncated)
	if err := ensureRequestFitsWithBudget(truncated, budget, "last-resort request"); err != nil {
		combined := llmcontracts.NewCategorizedError(llmcontracts.ErrorContextWindowExceeded, "last-resort request", fmt.Errorf("%v; original recovery error: %w", err, cause))
		logContextFailureWithBudget(truncated, combined, budget)
		return llmcontracts.AgentResult{}, combined
	}
	logContextDecisionWithBudget(req.ChatHistory, truncated, "last_resort", externalized, cause, budget)
	applog.Infof("[agent-svc] using token-budgeted last-resort truncation provider=%s model=%s failure_category=%s", req.Agent.Provider, req.Agent.Model, contextFailureCategory(cause))
	res, err := adapter.Call(truncated)
	err = categorizeProviderError(err)
	if err != nil {
		logContextFailureWithBudget(truncated, err, budget)
	}
	return res, err
}

func historyWithinRequestBudget(req llmcontracts.AgentRequest, history []models.Execution) []models.Execution {
	base := req
	base.ChatHistory = nil
	baseBudget := calculateRequestBudget(base)
	remaining := baseBudget.SafeInputLimit - baseBudget.TotalInputTokens()
	if remaining <= 0 {
		return nil
	}
	out := make([]models.Execution, 0, len(history))
	for i := len(history) - 1; i >= 0 && remaining > 0; i-- {
		exec := history[i]
		tokens := estimateExecutionTokens(exec)
		if tokens > remaining {
			exec.Output = ""
			exec.ErrorMessage = ""
			exec.ReasoningContent = ""
			exec.ReplayMessages = nil
			exec.PromptSent = truncateMiddleByEstimatedTokens(exec.PromptSent, remaining)
			tokens = estimateExecutionTokens(exec)
		}
		if tokens <= 0 || tokens > remaining {
			continue
		}
		out = append(out, exec)
		remaining -= tokens
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (s *LLMService) compactRequestHistoryWithLocalSummary(adapter ProviderAdapter, req llmcontracts.AgentRequest, triggerErrors ...error) (llmcontracts.AgentRequest, error) {
	history := append([]models.Execution(nil), req.ChatHistory...)
	var trigger error
	if len(triggerErrors) > 0 {
		trigger = triggerErrors[0]
	}
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
		summary, err := s.localSummaryCompaction(adapter, summaryBase, history, trigger)
		if err == nil {
			compacted := req
			compacted.ChatHistory = buildCompactedReplacementHistory(history, summary)
			compacted.NativeCompactionStateJSON = ""
			compacted.ForceNativeCompaction = false
			compacted.Agent.ForceNativeCompaction = false
			compacted.ChatHistory = historyWithinRequestBudget(compacted, compacted.ChatHistory)
			compacted.Ctx = llmcontracts.WithNativeCompactionStateJSON(compacted.Ctx, "")
			return compacted, nil
		}
		if !recognizedContextLengthError(err) || len(history) == 1 {
			return llmcontracts.AgentRequest{}, err
		}
		history = history[1:]
	}
	return llmcontracts.AgentRequest{}, llmcontracts.NewCategorizedError(llmcontracts.ErrorLocalCompactionFailed, "textual compaction", fmt.Errorf("no history remains to compact"))
}

func (s *LLMService) localSummaryCompaction(adapter ProviderAdapter, req llmcontracts.AgentRequest, history []models.Execution, triggers ...error) (string, error) {
	var trigger error
	if len(triggers) > 0 {
		trigger = triggers[0]
	}
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
	budget := requestBudgetForAgent(summaryReq.Agent)
	if estimatedUTF8Tokens(summaryReq.Message) > budget.SafeInputLimit {
		summaryReq.Message = truncateMiddleByEstimatedTokens(summaryReq.Message, budget.SafeInputLimit)
	}
	budget = calculateRequestBudget(summaryReq)
	if err := ensureRequestFitsWithBudget(summaryReq, budget, "textual compaction request"); err != nil {
		return "", llmcontracts.NewCategorizedError(llmcontracts.ErrorCompactionInputInfeasible, "textual compaction request", err)
	}
	logContextDecisionWithBudget(history, summaryReq, "textual_summary", false, trigger, budget)
	res, err := adapter.Call(summaryReq)
	err = categorizeProviderError(err)
	if err != nil {
		logContextFailureWithBudget(summaryReq, err, budget)
		if recognizedContextLengthError(err) {
			return "", err
		}
		return "", llmcontracts.NewCategorizedError(llmcontracts.ErrorLocalCompactionFailed, "textual compaction", err)
	}
	summary := strings.TrimSpace(res.TextOnlyOutput)
	if summary == "" {
		summary = strings.TrimSpace(res.Output)
	}
	if summary == "" {
		return "", llmcontracts.NewCategorizedError(llmcontracts.ErrorLocalCompactionFailed, "textual compaction", fmt.Errorf("provider returned an empty summary"))
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
	return tokenestimate.FromText(text)
}

func truncateMiddleByEstimatedTokens(text string, tokenBudget int) string {
	byteBudget := tokenestimate.ByteBudget(tokenBudget)
	if byteBudget <= 0 || len([]byte(text)) <= byteBudget {
		return text
	}
	gap := "\n\n[Middle of user message omitted during context compaction]\n\n"
	if byteBudget <= len(gap)+8 {
		gap = "\n[omitted]\n"
	}
	return tokenestimate.TruncateMiddle(text, byteBudget, gap)
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

func prepareAgentRuntimeRequest(req llmcontracts.AgentRequest) llmcontracts.AgentRequest {
	if !req.ProviderRuntimeResolved {
		req = resolveProviderRequestForBudget(req)
	}
	if req.AgentDefinition == nil {
		return req
	}
	req.ChatSystemContext = ApplyAgentToSystemPrompt(req.ChatSystemContext, req.AgentDefinition)
	req.ProjectInstructions = ApplyAgentToSystemPrompt(req.ProjectInstructions, req.AgentDefinition)
	return req
}

type anthropicAdapterCaller interface {
	Call(context.Context, llmcontracts.AgentRequest, string, *llmstream.Writer) (llmcontracts.AgentResult, error)
}

type anthropicProviderAdapter struct {
	svc     *LLMService
	adapter anthropicAdapterCaller
}

func anthropicAdapterEnabled(agent models.LLMConfig) bool {
	return agent.IsOAuth() || agent.IsAnthropicAPIKey()
}

func unsupportedModelTransport(provider models.LLMProvider, authMethod models.AuthMethod) error {
	return fmt.Errorf("%s model auth method %q is no longer supported; reconfigure the model to use OAuth or an API key", provider, authMethod)
}

func (a *anthropicProviderAdapter) callSupportedOperation(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	if anthropicAdapterEnabled(req.Agent) {
		return a.adapter.Call(req.Ctx, req, req.WorkDir, nil)
	}
	return llmcontracts.AgentResult{}, unsupportedModelTransport(req.Agent.Provider, req.Agent.AuthMethod)
}

func (a *anthropicProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	if !req.ProviderRuntimeResolved {
		req = resolveProviderRequestForBudget(req)
	}
	return callProviderOnce(func() (llmcontracts.AgentResult, error) {
		switch req.Operation {
		case llmcontracts.OperationDirect, llmcontracts.OperationStreaming, llmcontracts.OperationTask:
			return a.callSupportedOperation(req)
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
	req = prepareAgentRuntimeRequest(req)
	if req.AgentDefinition != nil {
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
	req = prepareAgentRuntimeRequest(req)
	if req.AgentDefinition != nil {
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
	req = prepareAgentRuntimeRequest(req)
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
	res, err := a.svc.callProviderWithCompaction(adapter, refReq)
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
	return a.svc.callProviderWithCompaction(adapter, req)
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

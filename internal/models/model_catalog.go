package models

import "strings"

// PromptProfile identifies the provider system-prompt snapshot used by a model.
// Prompt contents stay in internal/llm/prompt; the catalog only selects them.
type PromptProfile string

const (
	PromptProfileCodexReserve   PromptProfile = "codex/gpt-reserve.md"
	PromptProfileCodexGPT6Astra PromptProfile = "codex/gpt-6-astra.md"
	PromptProfileCodexGPT6Sol   PromptProfile = "codex/gpt-6-sol.md"
	PromptProfileCodexGPT6Luna  PromptProfile = "codex/gpt-6-luna.md"
	PromptProfileCodexGPT55     PromptProfile = "codex/gpt-5.5.md"
	PromptProfileClaudeCode     PromptProfile = "claude-code-standard"
)

// ChatCompletionsReasoningMode describes legacy Chat Completions reasoning
// behavior. Most catalog models use the Responses API and leave this unset.
type ChatCompletionsReasoningMode string

const (
	ChatCompletionsReasoningNone         ChatCompletionsReasoningMode = ""
	ChatCompletionsReasoningWhenToolFree ChatCompletionsReasoningMode = "when_tool_free"
	ChatCompletionsReasoningRequired     ChatCompletionsReasoningMode = "required"
)

// ModelSpec is the single source of truth for built-in OpenAI and Anthropic
// models. A routine model release should require only one entry here.
type ModelSpec struct {
	Provider    LLMProvider
	ID          string
	Label       string
	Visible     bool
	MatchPrefix bool

	ReasoningEfforts       []string
	DefaultReasoningEffort string
	ContextWindow          int
	PromptProfile          PromptProfile

	SupportsTemperature        bool
	SupportsWebSearch          bool
	SupportsResponsesWebSearch bool
	SupportsOAuth              bool
	SupportsNativeCompaction   bool
	NativeCompactionStrategy   string
	RequiresAdaptiveThinking   bool
	UsesAdaptiveThinking       bool
	DefaultOutputTokens        int
	MaxOutputTokens            int

	Transport                    string
	ResponsesLiteWebsocket       bool
	RemoteCompactionV2           bool
	GPT6Workflow                 bool
	ChatCompletionsReasoningMode ChatCompletionsReasoningMode
}

const (
	standardOpenAIContext    = 272000
	standardAnthropicContext = 200000
	anthropicCompaction      = "compact_20260112"
)

var modelCatalog = []ModelSpec{
	// OpenAI models shown in the Models editor.
	{Provider: ProviderOpenAI, ID: "gpt-6-astra", Label: "gpt-6-astra", Visible: true, MatchPrefix: true, ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexGPT6Astra, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", ResponsesLiteWebsocket: true, GPT6Workflow: true},
	{Provider: ProviderOpenAI, ID: "gpt-6.1-sol", Label: "gpt-6.1-sol", Visible: true, MatchPrefix: true, ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", ChatCompletionsReasoningMode: ChatCompletionsReasoningRequired},
	{Provider: ProviderOpenAI, ID: "gpt-6-sol", Label: "gpt-6-sol", Visible: true, MatchPrefix: true, ReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexGPT6Sol, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", ResponsesLiteWebsocket: true, GPT6Workflow: true, ChatCompletionsReasoningMode: ChatCompletionsReasoningWhenToolFree},
	{Provider: ProviderOpenAI, ID: "gpt-6-luna", Label: "gpt-6-luna", Visible: true, MatchPrefix: true, ReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexGPT6Luna, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", ResponsesLiteWebsocket: true, GPT6Workflow: true, ChatCompletionsReasoningMode: ChatCompletionsReasoningWhenToolFree},
	{Provider: ProviderOpenAI, ID: "gpt-5.6-sol", Label: "gpt-5.6-sol", Visible: true, ReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, Transport: "responses_websocket_http_fallback", ResponsesLiteWebsocket: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.6-terra", Label: "gpt-5.6-terra", Visible: true, ReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, Transport: "responses_websocket_http_fallback", ResponsesLiteWebsocket: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.6-luna", Label: "gpt-5.6-luna", Visible: true, ReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, Transport: "responses_websocket_http_fallback", ResponsesLiteWebsocket: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.5", Label: "gpt-5.5", Visible: true, MatchPrefix: true, ReasoningEfforts: efforts("low", "medium", "high", "xhigh"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexGPT55, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, Transport: "responses_http", RemoteCompactionV2: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.5-pro", Label: "gpt-5.5-pro", Visible: true, ReasoningEfforts: efforts("low", "medium", "high", "xhigh"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, Transport: "responses_http", RemoteCompactionV2: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.4", Label: "gpt-5.4", Visible: true, MatchPrefix: true, ReasoningEfforts: efforts("low", "medium", "high", "xhigh"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5.4-mini", Label: "gpt-5.4-mini", Visible: true, ReasoningEfforts: efforts("low", "medium", "high", "xhigh"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5.3-codex", Label: "gpt-5.3-codex", Visible: true, MatchPrefix: true, ReasoningEfforts: efforts("low", "medium", "high", "xhigh"), DefaultReasoningEffort: "high", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5-codex-mini", Label: "gpt-5-codex-mini", Visible: true, ReasoningEfforts: efforts("low", "medium", "high"), DefaultReasoningEffort: "high", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsOAuth: true, Transport: "responses_http"},

	// OpenAI models retained for runtime context/search compatibility but not
	// offered as new built-in selections.
	{Provider: ProviderOpenAI, ID: "gpt-5.3-codex-spark", ContextWindow: 128000, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5.2-codex", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5.3", MatchPrefix: true, ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5.2", MatchPrefix: true, ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5.1-codex-max", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5.1-codex-mini", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5.1-codex", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5-codex", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-4.1", ContextWindow: 1047576, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-4.1-mini", ContextWindow: 1047576, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-4.1-nano", ContextWindow: 1047576, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-4o", ContextWindow: 128000, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-4o-mini", ContextWindow: 128000, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-4-turbo", ContextWindow: 128000, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5", ContextWindow: 128000, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5-mini", ContextWindow: 128000, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5-nano", ContextWindow: 128000, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, Transport: "responses_http"},

	// Anthropic models shown in the Models editor. ContextWindow is always the
	// standard window; opt-in 1M context remains a separate request setting.
	{Provider: ProviderAnthropic, ID: "claude-opus-5-5", Label: "Claude Opus 5.5", Visible: true, ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 128000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-fable-5-1", Label: "Claude Fable 5.1", Visible: true, ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-mythos-5-1", Label: "Claude Mythos 5.1", Visible: true, ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-fable-5", Label: "Claude Fable 5", Visible: true, ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-mythos-5", Label: "Claude Mythos 5", Visible: true, ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-opus-5", Label: "Claude Opus 5", Visible: true, ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-sonnet-5", Label: "Claude Sonnet 5", Visible: true, ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-opus-4-8", Label: "Claude Opus 4.8", Visible: true, ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-opus-4-7", Label: "Claude Opus 4.7", Visible: true, ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-opus-4-6", Label: "Claude Opus 4.6", Visible: true, ReasoningEfforts: efforts("low", "medium", "high", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-sonnet-4-6", Label: "Claude Sonnet 4.6", Visible: true, ReasoningEfforts: efforts("low", "medium", "high", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, DefaultOutputTokens: 32000, MaxOutputTokens: 64000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-sonnet-4-5-20250929", Label: "Claude Sonnet 4.5", Visible: true, ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, DefaultOutputTokens: 32000, MaxOutputTokens: 64000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-haiku-4-5-20251001", Label: "Claude Haiku 4.5", Visible: true, ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, DefaultOutputTokens: 32000, MaxOutputTokens: 64000, Transport: "anthropic_messages_http"},

	// Supported Anthropic aliases/families not offered as new selections.
	{Provider: ProviderAnthropic, ID: "claude-mythos-preview", ReasoningEfforts: efforts("low", "medium", "high", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, DefaultOutputTokens: 32000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-opus-4-5", ReasoningEfforts: efforts("low", "medium", "high"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 32000, MaxOutputTokens: 64000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-sonnet-4-5", ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, DefaultOutputTokens: 32000, MaxOutputTokens: 64000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-sonnet-4-0", ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, DefaultOutputTokens: 32000, MaxOutputTokens: 64000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-3-7-sonnet", ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, DefaultOutputTokens: 32000, MaxOutputTokens: 64000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-opus-4-1", ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 32000, MaxOutputTokens: 32000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-opus-4-0", ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 32000, MaxOutputTokens: 32000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-3-5-sonnet", ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsOAuth: true, DefaultOutputTokens: 8192, MaxOutputTokens: 8192, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-3-5-haiku", ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsOAuth: true, DefaultOutputTokens: 8192, MaxOutputTokens: 8192, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-3-sonnet", ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsOAuth: true, DefaultOutputTokens: 8192, MaxOutputTokens: 8192, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-3-opus", ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsOAuth: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 4096, MaxOutputTokens: 4096, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-3-haiku", ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsOAuth: true, DefaultOutputTokens: 4096, MaxOutputTokens: 4096, Transport: "anthropic_messages_http"},
}

func efforts(values ...string) []string { return values }

// LookupModel finds a catalog model. Anthropic date-suffixed IDs and the [1m]
// request suffix inherit the longest matching base model's capabilities.
func LookupModel(provider LLMProvider, id string) (ModelSpec, bool) {
	normalized := strings.ToLower(strings.TrimSpace(id))
	if provider == ProviderAnthropic {
		normalized = strings.TrimSuffix(normalized, "[1m]")
	}
	best := -1
	for i := range modelCatalog {
		spec := modelCatalog[i]
		if spec.Provider != provider {
			continue
		}
		if normalized == spec.ID || ((provider == ProviderAnthropic || spec.MatchPrefix) && strings.HasPrefix(normalized, spec.ID+"-")) {
			if best < 0 || len(spec.ID) > len(modelCatalog[best].ID) {
				best = i
			}
		}
	}
	if best < 0 {
		return ModelSpec{}, false
	}
	return cloneModelSpec(modelCatalog[best]), true
}

// ProviderModels returns visible built-in choices in display order.
func ProviderModels(provider LLMProvider) []ModelSpec {
	result := make([]ModelSpec, 0)
	for _, spec := range modelCatalog {
		if spec.Provider == provider && spec.Visible {
			result = append(result, cloneModelSpec(spec))
		}
	}
	return result
}

// NormalizeModelEffort returns value when the selected model supports it.
func NormalizeModelEffort(provider LLMProvider, id, value string) string {
	effort := strings.ToLower(strings.TrimSpace(value))
	spec, ok := LookupModel(provider, id)
	if !ok {
		return ""
	}
	for _, supported := range spec.ReasoningEfforts {
		if effort == supported {
			return effort
		}
	}
	return ""
}

func cloneModelSpec(spec ModelSpec) ModelSpec {
	spec.ReasoningEfforts = append([]string(nil), spec.ReasoningEfforts...)
	return spec
}

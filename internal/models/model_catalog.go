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
	PromptProfileClaudeCode     PromptProfile = "anthropic_system_prompt.md"
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
	Provider      LLMProvider
	ID            string
	Label         string
	Default       bool
	VisionDefault bool

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
	// OpenAI models.
	{Provider: ProviderOpenAI, ID: "gpt-6-astra", Label: "gpt-6-astra", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexGPT6Astra, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", ResponsesLiteWebsocket: true, GPT6Workflow: true},
	{Provider: ProviderOpenAI, ID: "gpt-6.1-sol", Label: "gpt-6.1-sol", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", ChatCompletionsReasoningMode: ChatCompletionsReasoningRequired},
	{Provider: ProviderOpenAI, ID: "gpt-6-sol", Label: "gpt-6-sol", ReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexGPT6Sol, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", ResponsesLiteWebsocket: true, GPT6Workflow: true, ChatCompletionsReasoningMode: ChatCompletionsReasoningWhenToolFree},
	{Provider: ProviderOpenAI, ID: "gpt-6-luna", Label: "gpt-6-luna", ReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexGPT6Luna, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", ResponsesLiteWebsocket: true, GPT6Workflow: true, ChatCompletionsReasoningMode: ChatCompletionsReasoningWhenToolFree},
	{Provider: ProviderOpenAI, ID: "gpt-5.6-sol", Label: "gpt-5.6-sol", Default: true, ReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", ResponsesLiteWebsocket: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.6-terra", Label: "gpt-5.6-terra", ReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", ResponsesLiteWebsocket: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.6-luna", Label: "gpt-5.6-luna", ReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", ResponsesLiteWebsocket: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.5", Label: "gpt-5.5", ReasoningEfforts: efforts("low", "medium", "high", "xhigh"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexGPT55, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_http", RemoteCompactionV2: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.5-pro", Label: "gpt-5.5-pro", ReasoningEfforts: efforts("low", "medium", "high", "xhigh"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_http", RemoteCompactionV2: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.4", Label: "gpt-5.4", ReasoningEfforts: efforts("low", "medium", "high", "xhigh"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsNativeCompaction: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5.4-mini", Label: "gpt-5.4-mini", ReasoningEfforts: efforts("low", "medium", "high", "xhigh"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsNativeCompaction: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5.3-codex", Label: "gpt-5.3-codex", ReasoningEfforts: efforts("low", "medium", "high", "xhigh"), DefaultReasoningEffort: "high", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsNativeCompaction: true, Transport: "responses_http"},

	// Anthropic models. ContextWindow is always the
	// standard window; opt-in 1M context remains a separate request setting.
	{Provider: ProviderAnthropic, ID: "claude-opus-5-5", Label: "Claude Opus 5.5", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 128000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-fable-5-1", Label: "Claude Fable 5.1", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-mythos-5-1", Label: "Claude Mythos 5.1", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-fable-5", Label: "Claude Fable 5", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-mythos-5", Label: "Claude Mythos 5", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-opus-5", Label: "Claude Opus 5", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-sonnet-5-5", Label: "Claude Sonnet 5.5", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-sonnet-5", Label: "Claude Sonnet 5", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-opus-4-8", Label: "Claude Opus 4.8", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-opus-4-7", Label: "Claude Opus 4.7", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-opus-4-6", Label: "Claude Opus 4.6", ReasoningEfforts: efforts("low", "medium", "high", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-sonnet-4-6", Label: "Claude Sonnet 4.6", ReasoningEfforts: efforts("low", "medium", "high", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, DefaultOutputTokens: 32000, MaxOutputTokens: 64000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-sonnet-4-5-20250929", Label: "Claude Sonnet 4.5", Default: true, VisionDefault: true, ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, DefaultOutputTokens: 32000, MaxOutputTokens: 64000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-haiku-4-5-20251001", Label: "Claude Haiku 4.5", ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, DefaultOutputTokens: 32000, MaxOutputTokens: 64000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-haiku-5-5", Label: "Claude Haiku 5.5", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
}

func efforts(values ...string) []string { return values }

// LookupModel finds an exact supported catalog model.
func LookupModel(provider LLMProvider, id string) (ModelSpec, bool) {
	normalized := strings.ToLower(strings.TrimSpace(id))
	for i := range modelCatalog {
		spec := modelCatalog[i]
		if spec.Provider == provider && normalized == spec.ID {
			return cloneModelSpec(spec), true
		}
	}
	return ModelSpec{}, false
}

// DefaultModel returns the provider's catalog-owned fallback model.
func DefaultModel(provider LLMProvider) (ModelSpec, bool) {
	return catalogModelByRole(provider, func(spec ModelSpec) bool { return spec.Default })
}

// VisionDefaultModel returns the provider's catalog-owned vision fallback.
func VisionDefaultModel(provider LLMProvider) (ModelSpec, bool) {
	return catalogModelByRole(provider, func(spec ModelSpec) bool { return spec.VisionDefault })
}

func catalogModelByRole(provider LLMProvider, matches func(ModelSpec) bool) (ModelSpec, bool) {
	for _, spec := range modelCatalog {
		if spec.Provider == provider && matches(spec) {
			return cloneModelSpec(spec), true
		}
	}
	return ModelSpec{}, false
}

// ProviderModels returns supported built-in choices in display order.
func ProviderModels(provider LLMProvider) []ModelSpec {
	result := make([]ModelSpec, 0)
	for _, spec := range modelCatalog {
		if spec.Provider == provider {
			result = append(result, cloneModelSpec(spec))
		}
	}
	return result
}

// BuiltInModelSupported reports whether an OpenAI or Anthropic model is in the
// current catalog. Other providers manage their own model identifiers.
func BuiltInModelSupported(provider LLMProvider, id string) bool {
	switch provider {
	case ProviderOpenAI, ProviderAnthropic:
		_, ok := LookupModel(provider, id)
		return ok
	default:
		return true
	}
}

// BuiltInModelSupportedForAuth additionally checks availability for ChatGPT sign-in.
func BuiltInModelSupportedForAuth(provider LLMProvider, id string, auth AuthMethod) bool {
	if !BuiltInModelSupported(provider, id) {
		return false
	}
	if (provider == ProviderOpenAI || provider == ProviderAnthropic) && auth == AuthMethodOAuth {
		spec, _ := LookupModel(provider, id)
		return spec.SupportsOAuth
	}
	return true
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

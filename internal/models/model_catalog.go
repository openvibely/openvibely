package models

import "strings"

// PromptProfile identifies the provider system-prompt snapshot used by a model.
// Prompt contents stay in internal/llm/prompt; the catalog only selects them.
type PromptProfile string

const (
	PromptProfileCodexGPT62Astra PromptProfile = "codex/gpt-6.2-astra.md"
	PromptProfileCodexGPT62Sol   PromptProfile = "codex/gpt-6.2-sol.md"
	PromptProfileCodexGPT61Sol   PromptProfile = "codex/gpt-6.1-sol.md"
	PromptProfileCodexGPT61Terra PromptProfile = "codex/gpt-6.1-terra.md"
	PromptProfileCodexReserve    PromptProfile = "codex/gpt-reserve.md"
	PromptProfileCodexGPT6Astra  PromptProfile = "codex/gpt-6-astra.md"
	PromptProfileCodexGPT6Sol    PromptProfile = "codex/gpt-6-sol.md"
	PromptProfileCodexGPT6Luna   PromptProfile = "codex/gpt-6-luna.md"
	PromptProfileCodexGPT55      PromptProfile = "codex/gpt-5.5.md"
	PromptProfileClaudeCode      PromptProfile = "anthropic_system_prompt.md"
)

// ChatCompletionsReasoningMode describes legacy Chat Completions reasoning
// behavior. Most catalog models use the Responses API and leave this unset.
type ChatCompletionsReasoningMode string

const (
	ChatCompletionsReasoningNone         ChatCompletionsReasoningMode = ""
	ChatCompletionsReasoningWhenToolFree ChatCompletionsReasoningMode = "when_tool_free"
	ChatCompletionsReasoningRequired     ChatCompletionsReasoningMode = "required"
)

// ImageResizeProfile selects the limits applied to newly submitted images.
type ImageResizeProfile string

const (
	ImageResize6000 ImageResizeProfile = "6000px"
	ImageResize1568 ImageResizeProfile = "1568px"
	ImageResize2000 ImageResizeProfile = "2000px"
)

// ModelSpec is the single source of truth for built-in OpenAI and Anthropic
// models. A routine model release should require only one entry here.
type ModelSpec struct {
	Provider           LLMProvider
	ID                 string
	Label              string
	Default            bool
	VisionDefault      bool
	ImageResizeProfile ImageResizeProfile

	ReasoningEfforts       []string
	DefaultReasoningEffort string
	ContextWindow          int
	PromptProfile          PromptProfile

	SupportsFastMode           bool
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
	standardGeminiContext    = 1048576
	standardGeminiOutput     = 65536
	anthropicCompaction      = "compact_20260112"
)

var modelCatalog = []ModelSpec{
	// OpenAI models.
	// Ultra effort requires automatic
	// delegation; expose the reasoning levels supported by our current workflow.
	{Provider: ProviderOpenAI, ID: "gpt-6.2-astra", ImageResizeProfile: ImageResize6000, Label: "gpt-6.2-astra", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "low", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexGPT62Astra, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", SupportsFastMode: true, ResponsesLiteWebsocket: true, GPT6Workflow: true, ChatCompletionsReasoningMode: ChatCompletionsReasoningRequired},
	{Provider: ProviderOpenAI, ID: "gpt-6.2-sol", ImageResizeProfile: ImageResize6000, Label: "gpt-6.2-sol", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "low", ContextWindow: 500000, PromptProfile: PromptProfileCodexGPT62Sol, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", SupportsFastMode: true, ResponsesLiteWebsocket: true, GPT6Workflow: true, ChatCompletionsReasoningMode: ChatCompletionsReasoningRequired},
	{Provider: ProviderOpenAI, ID: "gpt-6-astra", ImageResizeProfile: ImageResize6000, Label: "gpt-6-astra", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexGPT6Astra, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", SupportsFastMode: true, ResponsesLiteWebsocket: true, GPT6Workflow: true},
	// Sol 6.1 defaults to low effort and 500K context.
	{Provider: ProviderOpenAI, ID: "gpt-6.1-sol", ImageResizeProfile: ImageResize6000, Label: "gpt-6.1-sol", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "low", ContextWindow: 500000, PromptProfile: PromptProfileCodexGPT61Sol, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", SupportsFastMode: true, ResponsesLiteWebsocket: true, GPT6Workflow: true, ChatCompletionsReasoningMode: ChatCompletionsReasoningRequired},
	// Terra 6.1 uses low effort,
	// a 500K default context, and its own prompt. Keep API-supported none effort.
	{Provider: ProviderOpenAI, ID: "gpt-6.1-terra", ImageResizeProfile: ImageResize6000, Label: "gpt-6.1-terra", ReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "low", ContextWindow: 500000, PromptProfile: PromptProfileCodexGPT61Terra, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", SupportsFastMode: true, ResponsesLiteWebsocket: true, GPT6Workflow: true},
	{Provider: ProviderOpenAI, ID: "gpt-6-sol", ImageResizeProfile: ImageResize6000, Label: "gpt-6-sol", ReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexGPT6Sol, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", SupportsFastMode: true, ResponsesLiteWebsocket: true, GPT6Workflow: true, ChatCompletionsReasoningMode: ChatCompletionsReasoningWhenToolFree},
	{Provider: ProviderOpenAI, ID: "gpt-6-luna", ImageResizeProfile: ImageResize6000, Label: "gpt-6-luna", ReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexGPT6Luna, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", SupportsFastMode: true, ResponsesLiteWebsocket: true, GPT6Workflow: true, ChatCompletionsReasoningMode: ChatCompletionsReasoningWhenToolFree},
	{Provider: ProviderOpenAI, ID: "gpt-5.6-sol", ImageResizeProfile: ImageResize6000, Label: "gpt-5.6-sol", Default: true, ReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", SupportsFastMode: true, ResponsesLiteWebsocket: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.6-terra", ImageResizeProfile: ImageResize6000, Label: "gpt-5.6-terra", ReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", SupportsFastMode: true, ResponsesLiteWebsocket: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.6-luna", ImageResizeProfile: ImageResize6000, Label: "gpt-5.6-luna", ReasoningEfforts: efforts("none", "low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, Transport: "responses_websocket_http_fallback", SupportsFastMode: true, ResponsesLiteWebsocket: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.5", ImageResizeProfile: ImageResize6000, Label: "gpt-5.5", ReasoningEfforts: efforts("low", "medium", "high", "xhigh"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexGPT55, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsNativeCompaction: true, Transport: "responses_http", RemoteCompactionV2: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.5-pro", ImageResizeProfile: ImageResize6000, Label: "gpt-5.5-pro", ReasoningEfforts: efforts("low", "medium", "high", "xhigh"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsNativeCompaction: true, Transport: "responses_http", RemoteCompactionV2: true},
	{Provider: ProviderOpenAI, ID: "gpt-5.4", ImageResizeProfile: ImageResize6000, Label: "gpt-5.4", ReasoningEfforts: efforts("low", "medium", "high", "xhigh"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsNativeCompaction: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5.4-mini", ImageResizeProfile: ImageResize6000, Label: "gpt-5.4-mini", ReasoningEfforts: efforts("low", "medium", "high", "xhigh"), DefaultReasoningEffort: "medium", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsNativeCompaction: true, Transport: "responses_http"},
	{Provider: ProviderOpenAI, ID: "gpt-5.3-codex", ImageResizeProfile: ImageResize6000, Label: "gpt-5.3-codex", ReasoningEfforts: efforts("low", "medium", "high", "xhigh"), DefaultReasoningEffort: "high", ContextWindow: standardOpenAIContext, PromptProfile: PromptProfileCodexReserve, SupportsTemperature: true, SupportsWebSearch: true, SupportsResponsesWebSearch: true, SupportsNativeCompaction: true, Transport: "responses_http"},

	// Anthropic models. ContextWindow is always the
	// standard window; opt-in 1M context remains a separate request setting.
	{Provider: ProviderAnthropic, ID: "claude-haiku-5-5", ImageResizeProfile: ImageResize2000, Label: "Claude Haiku 5.5", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsOAuth: true, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-opus-5-5", ImageResizeProfile: ImageResize2000, Label: "Claude Opus 5.5", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsFastMode: true, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 128000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-sonnet-5-5", ImageResizeProfile: ImageResize2000, Label: "Claude Sonnet 5.5", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	// Fable and Mythos 5.2 use medium effort, fast mode, and 128k default output.
	{Provider: ProviderAnthropic, ID: "claude-fable-5-2", ImageResizeProfile: ImageResize2000, Label: "Claude Fable 5.2", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsFastMode: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 128000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-mythos-5-2", ImageResizeProfile: ImageResize2000, Label: "Claude Mythos 5.2", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), DefaultReasoningEffort: "medium", ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsFastMode: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 128000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-fable-5-1", ImageResizeProfile: ImageResize2000, Label: "Claude Fable 5.1", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-mythos-5-1", ImageResizeProfile: ImageResize2000, Label: "Claude Mythos 5.1", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-fable-5", ImageResizeProfile: ImageResize2000, Label: "Claude Fable 5", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-mythos-5", ImageResizeProfile: ImageResize2000, Label: "Claude Mythos 5", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-opus-5", ImageResizeProfile: ImageResize2000, Label: "Claude Opus 5", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsFastMode: true, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-sonnet-5", ImageResizeProfile: ImageResize2000, Label: "Claude Sonnet 5", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, RequiresAdaptiveThinking: true, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-opus-4-8", ImageResizeProfile: ImageResize2000, Label: "Claude Opus 4.8", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsFastMode: true, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-opus-4-7", ImageResizeProfile: ImageResize2000, Label: "Claude Opus 4.7", ReasoningEfforts: efforts("low", "medium", "high", "xhigh", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-opus-4-6", ImageResizeProfile: ImageResize1568, Label: "Claude Opus 4.6", ReasoningEfforts: efforts("low", "medium", "high", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, UsesAdaptiveThinking: true, DefaultOutputTokens: 64000, MaxOutputTokens: 128000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-sonnet-4-6", ImageResizeProfile: ImageResize1568, Label: "Claude Sonnet 4.6", ReasoningEfforts: efforts("low", "medium", "high", "max"), ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, SupportsNativeCompaction: true, NativeCompactionStrategy: anthropicCompaction, DefaultOutputTokens: 32000, MaxOutputTokens: 64000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-haiku-4-5-20251001", ImageResizeProfile: ImageResize1568, Label: "Claude Haiku 4.5", ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, DefaultOutputTokens: 32000, MaxOutputTokens: 64000, Transport: "anthropic_messages_http"},
	{Provider: ProviderAnthropic, ID: "claude-sonnet-4-5-20250929", ImageResizeProfile: ImageResize1568, Label: "Claude Sonnet 4.5", Default: true, VisionDefault: true, ContextWindow: standardAnthropicContext, PromptProfile: PromptProfileClaudeCode, SupportsTemperature: true, SupportsWebSearch: true, SupportsOAuth: true, DefaultOutputTokens: 32000, MaxOutputTokens: 64000, Transport: "anthropic_messages_http"},
	// Google Gemini models.
	{Provider: ProviderGemini, ID: "gemini-3.1-pro-preview", ImageResizeProfile: ImageResize2000, Label: "Gemini 3.1 Pro Preview", ContextWindow: standardGeminiContext, SupportsTemperature: true, SupportsOAuth: true, DefaultOutputTokens: standardGeminiOutput, MaxOutputTokens: standardGeminiOutput, Transport: "gemini_generate_content"},
	{Provider: ProviderGemini, ID: "gemini-3-pro-preview", ImageResizeProfile: ImageResize2000, Label: "Gemini 3 Pro Preview", ContextWindow: standardGeminiContext, SupportsTemperature: true, SupportsOAuth: true, DefaultOutputTokens: standardGeminiOutput, MaxOutputTokens: standardGeminiOutput, Transport: "gemini_generate_content"},
	{Provider: ProviderGemini, ID: "gemini-3-flash-preview", ImageResizeProfile: ImageResize2000, Label: "Gemini 3 Flash Preview", ContextWindow: standardGeminiContext, SupportsTemperature: true, SupportsOAuth: true, DefaultOutputTokens: standardGeminiOutput, MaxOutputTokens: standardGeminiOutput, Transport: "gemini_generate_content"},
	{Provider: ProviderGemini, ID: "gemini-3.8-flash", ImageResizeProfile: ImageResize2000, Label: "Gemini 3.8 Flash", ContextWindow: standardGeminiContext, SupportsTemperature: true, SupportsOAuth: true, DefaultOutputTokens: standardGeminiOutput, MaxOutputTokens: standardGeminiOutput, Transport: "gemini_generate_content"},
	{Provider: ProviderGemini, ID: "gemini-3.5-flash", ImageResizeProfile: ImageResize2000, Label: "Gemini 3.5 Flash", ContextWindow: standardGeminiContext, SupportsTemperature: true, SupportsOAuth: true, DefaultOutputTokens: standardGeminiOutput, MaxOutputTokens: standardGeminiOutput, Transport: "gemini_generate_content"},
	{Provider: ProviderGemini, ID: "gemini-3-flash", ImageResizeProfile: ImageResize2000, Label: "Gemini 3 Flash", ContextWindow: standardGeminiContext, SupportsTemperature: true, SupportsOAuth: true, DefaultOutputTokens: standardGeminiOutput, MaxOutputTokens: standardGeminiOutput, Transport: "gemini_generate_content"},
	{Provider: ProviderGemini, ID: "gemini-3.5-flash-lite", ImageResizeProfile: ImageResize2000, Label: "Gemini 3.5 Flash-Lite", ContextWindow: standardGeminiContext, SupportsTemperature: true, SupportsOAuth: true, DefaultOutputTokens: standardGeminiOutput, MaxOutputTokens: standardGeminiOutput, Transport: "gemini_generate_content"},
	{Provider: ProviderGemini, ID: "gemini-3.1-flash-lite", ImageResizeProfile: ImageResize2000, Label: "Gemini 3.1 Flash-Lite", ContextWindow: standardGeminiContext, SupportsTemperature: true, SupportsOAuth: true, DefaultOutputTokens: standardGeminiOutput, MaxOutputTokens: standardGeminiOutput, Transport: "gemini_generate_content"},
	{Provider: ProviderGemini, ID: "gemini-2.5-pro", ImageResizeProfile: ImageResize2000, Label: "Gemini 2.5 Pro", Default: true, VisionDefault: true, ContextWindow: standardGeminiContext, SupportsTemperature: true, SupportsOAuth: true, DefaultOutputTokens: standardGeminiOutput, MaxOutputTokens: standardGeminiOutput, Transport: "gemini_generate_content"},
	{Provider: ProviderGemini, ID: "gemini-2.5-flash", ImageResizeProfile: ImageResize2000, Label: "Gemini 2.5 Flash", ContextWindow: standardGeminiContext, SupportsTemperature: true, SupportsOAuth: true, DefaultOutputTokens: standardGeminiOutput, MaxOutputTokens: standardGeminiOutput, Transport: "gemini_generate_content"},
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
	case ProviderOpenAI, ProviderAnthropic, ProviderGemini:
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
	if (provider == ProviderOpenAI || provider == ProviderAnthropic || provider == ProviderGemini) && auth == AuthMethodOAuth {
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

// ModelSupportsFastMode is catalog-owned; arbitrary compatible models do not inherit it.
func ModelSupportsFastMode(provider LLMProvider, model string) bool {
	spec, ok := LookupModel(provider, model)
	return ok && spec.SupportsFastMode
}

func ValidServiceTier(agent LLMConfig, tier string) bool {
	return tier == "" || (ModelSupportsFastMode(agent.Provider, agent.Model) && (tier == "priority" || tier == "default"))
}

func (a LLMConfig) EffectiveServiceTier() string {
	if ValidServiceTier(a, a.ServiceTier) {
		return a.ServiceTier
	}
	return ""
}

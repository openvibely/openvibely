// Package websearch provides centralized constants and capability gating for
// provider-native web search support.
//
// Both OpenAI and Anthropic support provider-executed web search tools that do
// not require local tool execution. This package defines the canonical tool
// names, provider-specific schema versions, and model capability checks so
// future schema revisions only need updating in one place.
package websearch

import "strings"

// --- Provider-native tool names (canonical) ---

const (
	// OpenAI Responses API web search tool type.
	// Sent as {"type": "web_search"} in the tools array.
	OpenAIWebSearchToolType = "web_search"
	// Legacy OpenAI web search tool type accepted by older integrations.
	OpenAILegacyWebSearchToolType = "web_search_preview"

	// Anthropic server tool names (versioned per API contract).
	AnthropicWebSearchToolName      = "web_search_20260209"
	AnthropicWebSearchToolNameOlder = "web_search_20250305"
	AnthropicWebFetchToolName       = "web_fetch_20260209"
)

// --- Canonical tool names for allowlist mapping ---

const (
	// CanonicalWebSearch is the display/allowlist name for web search.
	CanonicalWebSearch = "WebSearch"
	// CanonicalWebFetch is the display/allowlist name for web fetch.
	CanonicalWebFetch = "WebFetch"
)

// IsProviderNativeSearchTool returns true if the tool name is a provider-native
// web search or web fetch tool that should NOT be routed through local
// ExecuteTool. These are executed server-side by the provider.
func IsProviderNativeSearchTool(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "web_search", "web_search_preview",
		"web_search_20260209",
		"web_search_20250305",
		"web_fetch_20260209",
		"web_fetch_20250910":
		return true
	default:
		return false
	}
}

// MapProviderSearchToolName maps a provider-native search tool name to its
// canonical display name for allowlist checks and UI rendering.
func MapProviderSearchToolName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "web_search", "web_search_preview", "web_search_20260209", "web_search_20250305":
		return CanonicalWebSearch
	case "web_fetch_20260209", "web_fetch_20250910":
		return CanonicalWebFetch
	default:
		return ""
	}
}

// --- OpenAI model capability gating ---

// OpenAIModelSupportsSearch returns true if the given OpenAI model slug
// supports the native web search tool. Based on models_cache.json metadata
// where supports_search_tool=true.
func OpenAIModelSupportsSearch(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	// gpt-5.5 family
	case strings.HasPrefix(m, "gpt-5.5"):
		return true
	// gpt-5.4 family
	case strings.HasPrefix(m, "gpt-5.4"):
		return true
	// gpt-5.3-codex family
	case strings.HasPrefix(m, "gpt-5.3"):
		return true
	// gpt-5.2 family
	case strings.HasPrefix(m, "gpt-5.2"):
		return true
	default:
		return false
	}
}

// OpenAIWebSearchToolPayload returns the tool object to include in the
// Responses API tools array for web search. Returns nil if the model does
// not support search.
func OpenAIWebSearchToolPayload(model string) map[string]any {
	if !OpenAIModelSupportsSearch(model) {
		return nil
	}
	return map[string]any{
		"type": OpenAIWebSearchToolType,
	}
}

// --- OpenAI stream event type checks ---

// IsOpenAIWebSearchOutputItem returns true if the output item type is a
// web search call/result from the Responses API stream.
func IsOpenAIWebSearchOutputItem(itemType string) bool {
	switch strings.ToLower(strings.TrimSpace(itemType)) {
	case "web_search_call", "tool_search_call":
		return true
	default:
		return false
	}
}

package websearch

import "testing"

func TestOpenAIModelSupportsSearch(t *testing.T) {
	tests := []struct {
		model    string
		expected bool
	}{
		{"gpt-5.4", true},
		{"gpt-5.4-mini", true},
		{"GPT-5.4", true},
		{"gpt-5.3-codex", true},
		{"gpt-5.3", true},
		{"gpt-5.2", true},
		{"gpt-5.2-mini", true},
		{"gpt-4o", false},
		{"gpt-4o-mini", false},
		{"gpt-4.1", false},
		{"o3", false},
		{"o3-mini", false},
		{"claude-sonnet-4-20250514", false},
		{"", false},
	}
	for _, tt := range tests {
		got := OpenAIModelSupportsSearch(tt.model)
		if got != tt.expected {
			t.Errorf("OpenAIModelSupportsSearch(%q) = %v, want %v", tt.model, got, tt.expected)
		}
	}
}

func TestOpenAIWebSearchToolPayload(t *testing.T) {
	// Supported model should get a payload
	p := OpenAIWebSearchToolPayload("gpt-5.4-mini")
	if p == nil {
		t.Fatal("expected non-nil payload for gpt-5.4-mini")
	}
	if p["type"] != OpenAIWebSearchToolType {
		t.Errorf("type = %v, want %v", p["type"], OpenAIWebSearchToolType)
	}

	// Unsupported model should get nil
	p = OpenAIWebSearchToolPayload("gpt-4o")
	if p != nil {
		t.Fatal("expected nil payload for gpt-4o")
	}
}

func TestIsProviderNativeSearchTool(t *testing.T) {
	tests := []struct {
		name     string
		expected bool
	}{
		{"web_search", true},
		{"web_search_preview", true},
		{"web_search_20260209", true},
		{"Web_Search_Preview", true},
		{"web_search_20250305", true},
		{"web_fetch_20260209", true},
		{"web_fetch_20250910", true},
		{"read_file", false},
		{"bash", false},
		{"", false},
	}
	for _, tt := range tests {
		got := IsProviderNativeSearchTool(tt.name)
		if got != tt.expected {
			t.Errorf("IsProviderNativeSearchTool(%q) = %v, want %v", tt.name, got, tt.expected)
		}
	}
}

func TestMapProviderSearchToolName(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"web_search", CanonicalWebSearch},
		{"web_search_preview", CanonicalWebSearch},
		{"web_search_20260209", CanonicalWebSearch},
		{"web_search_20250305", CanonicalWebSearch},
		{"web_fetch_20260209", CanonicalWebFetch},
		{"web_fetch_20250910", CanonicalWebFetch},
		{"read_file", ""},
		{"bash", ""},
	}
	for _, tt := range tests {
		got := MapProviderSearchToolName(tt.name)
		if got != tt.expected {
			t.Errorf("MapProviderSearchToolName(%q) = %q, want %q", tt.name, got, tt.expected)
		}
	}
}

func TestIsOpenAIWebSearchOutputItem(t *testing.T) {
	tests := []struct {
		itemType string
		expected bool
	}{
		{"web_search_call", true},
		{"tool_search_call", true},
		{"Web_Search_Call", true},
		{"function_call", false},
		{"message", false},
		{"", false},
	}
	for _, tt := range tests {
		got := IsOpenAIWebSearchOutputItem(tt.itemType)
		if got != tt.expected {
			t.Errorf("IsOpenAIWebSearchOutputItem(%q) = %v, want %v", tt.itemType, got, tt.expected)
		}
	}
}

package openai

import (
	"encoding/json"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

func TestRuntimeOpenAIToolsMarksOnlyAllowlistedReadToolsAsync(t *testing.T) {
	rt := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{
		{Name: "memory_view", Description: "read memory", Access: llmcontracts.RuntimeToolAccessRead, Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "create_task", Description: "write", Access: llmcontracts.RuntimeToolAccessWrite, Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "unknown_read", Description: "not allowlisted", Access: llmcontracts.RuntimeToolAccessRead, Parameters: json.RawMessage(`{"type":"object"}`)},
	}}
	tools := runtimeOpenAITools(rt, true)
	if len(tools) != 3 {
		t.Fatalf("tools = %d, want 3", len(tools))
	}
	asyncByName := map[string]bool{}
	for _, tool := range tools {
		asyncByName[tool.Name] = tool.Async
	}
	if !asyncByName["memory_view"] {
		t.Fatalf("memory_view should be async: %#v", tools)
	}
	if asyncByName["create_task"] || asyncByName["unknown_read"] {
		t.Fatalf("write or unallowlisted tool was async: %#v", tools)
	}

	tools = runtimeOpenAITools(rt, false)
	for _, tool := range tools {
		if tool.Async {
			t.Fatalf("async disabled but tool was async: %#v", tools)
		}
	}
}

func TestOpenAIAsyncRuntimeToolsEnabledRequiresAstraFirstPartyAuth(t *testing.T) {
	tests := []struct {
		name  string
		agent models.LLMConfig
		want  bool
	}{
		{name: "astra api key", agent: models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-6-astra", APIKey: "sk"}, want: true},
		{name: "astra oauth", agent: models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodOAuth, Model: " GPT-6-ASTRA ", OAuthAccessToken: "tok"}, want: true},
		{name: "non astra", agent: models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-5.6-sol", APIKey: "sk"}},
		{name: "openai compatible", agent: models.LLMConfig{Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-6-astra", APIKey: "sk"}},
		{name: "legacy cli", agent: models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodCLI, Model: "gpt-6-astra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := openAIAsyncRuntimeToolsEnabled(tt.agent); got != tt.want {
				t.Fatalf("openAIAsyncRuntimeToolsEnabled = %v, want %v", got, tt.want)
			}
		})
	}
}

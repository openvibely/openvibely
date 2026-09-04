package models

import (
	"testing"
	"time"
)

func TestModelSupportsTemperature(t *testing.T) {
	tests := []struct {
		name     string
		provider LLMProvider
		model    string
		want     bool
	}{
		{name: "Astra", provider: ProviderOpenAI, model: "gpt-6-astra", want: false},
		{name: "Astra normalized", provider: ProviderOpenAI, model: " GPT-6-ASTRA ", want: false},
		{name: "other OpenAI", provider: ProviderOpenAI, model: "gpt-5.6-sol", want: true},
		{name: "Kimi", provider: ProviderOpenAICompatible, model: "kimi-k3", want: false},
		{name: "other compatible", provider: ProviderOpenAICompatible, model: "glm-5.2", want: true},
		{name: "mixture", provider: ProviderMixture, model: "default", want: false},
		{name: "Anthropic", provider: ProviderAnthropic, model: "claude-opus-5", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ModelSupportsTemperature(tt.provider, tt.model); got != tt.want {
				t.Fatalf("ModelSupportsTemperature(%q, %q) = %v, want %v", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}

func TestLLMConfig_IsOAuth(t *testing.T) {
	tests := []struct {
		name     string
		config   LLMConfig
		expected bool
	}{
		{
			name:     "ClaudeMax with OAuth",
			config:   LLMConfig{Provider: ProviderAnthropic, AuthMethod: AuthMethodOAuth},
			expected: true,
		},
		{
			name:     "ClaudeMax with CLI",
			config:   LLMConfig{Provider: ProviderAnthropic, AuthMethod: AuthMethodCLI},
			expected: false,
		},
		{
			name:     "ClaudeMax with empty auth method",
			config:   LLMConfig{Provider: ProviderAnthropic, AuthMethod: ""},
			expected: false,
		},
		{
			name:     "Ollama with OAuth (should be false)",
			config:   LLMConfig{Provider: ProviderOllama, AuthMethod: AuthMethodOAuth},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.config.IsOAuth(); got != tt.expected {
				t.Errorf("IsOAuth() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestLLMConfig_HasValidOAuthToken(t *testing.T) {
	futureExpiry := time.Now().Add(2 * time.Hour).UnixMilli()
	pastExpiry := time.Now().Add(-1 * time.Hour).UnixMilli()

	tests := []struct {
		name     string
		config   LLMConfig
		expected bool
	}{
		{
			name: "Valid token",
			config: LLMConfig{
				Provider:         ProviderAnthropic,
				AuthMethod:       AuthMethodOAuth,
				OAuthAccessToken: "valid-token",
				OAuthExpiresAt:   futureExpiry,
			},
			expected: true,
		},
		{
			name: "Opaque token with unknown expiry",
			config: LLMConfig{
				Provider:         ProviderOpenAICompatible,
				AuthMethod:       AuthMethodOAuth,
				OAuthAccessToken: "opaque-token",
				OAuthExpiresAt:   0,
			},
			expected: true,
		},
		{
			name: "Expired token",
			config: LLMConfig{
				Provider:         ProviderAnthropic,
				AuthMethod:       AuthMethodOAuth,
				OAuthAccessToken: "expired-token",
				OAuthExpiresAt:   pastExpiry,
			},
			expected: false,
		},
		{
			name: "Empty token",
			config: LLMConfig{
				Provider:       ProviderAnthropic,
				AuthMethod:     AuthMethodOAuth,
				OAuthExpiresAt: futureExpiry,
			},
			expected: false,
		},
		{
			name: "CLI auth method (not OAuth)",
			config: LLMConfig{
				Provider:         ProviderAnthropic,
				AuthMethod:       AuthMethodCLI,
				OAuthAccessToken: "some-token",
				OAuthExpiresAt:   futureExpiry,
			},
			expected: false,
		},
		{
			name: "Wrong provider",
			config: LLMConfig{
				Provider:         ProviderOllama,
				AuthMethod:       AuthMethodOAuth,
				OAuthAccessToken: "some-token",
				OAuthExpiresAt:   futureExpiry,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.config.HasValidOAuthToken(); got != tt.expected {
				t.Errorf("HasValidOAuthToken() = %v, want %v", got, tt.expected)
			}
		})
	}
}
func TestLLMConfigIsCallableMixtureSlot(t *testing.T) {
	tests := []struct {
		name string
		cfg  LLMConfig
		want bool
	}{
		{name: "openai api key", cfg: LLMConfig{Provider: ProviderOpenAI, AuthMethod: AuthMethodAPIKey}, want: true},
		{name: "openai oauth", cfg: LLMConfig{Provider: ProviderOpenAI, AuthMethod: AuthMethodOAuth}, want: true},
		{name: "openai cli", cfg: LLMConfig{Provider: ProviderOpenAI, AuthMethod: AuthMethodCLI}, want: false},
		{name: "anthropic api key", cfg: LLMConfig{Provider: ProviderAnthropic, AuthMethod: AuthMethodAPIKey}, want: true},
		{name: "anthropic oauth", cfg: LLMConfig{Provider: ProviderAnthropic, AuthMethod: AuthMethodOAuth}, want: true},
		{name: "anthropic cli", cfg: LLMConfig{Provider: ProviderAnthropic, AuthMethod: AuthMethodCLI}, want: false},
		{name: "openai compatible api key", cfg: LLMConfig{Provider: ProviderOpenAICompatible, AuthMethod: AuthMethodAPIKey}, want: true},
		{name: "openai compatible oauth", cfg: LLMConfig{Provider: ProviderOpenAICompatible, AuthMethod: AuthMethodOAuth}, want: true},
		{name: "openai compatible cli", cfg: LLMConfig{Provider: ProviderOpenAICompatible, AuthMethod: AuthMethodCLI}, want: false},
		{name: "ollama", cfg: LLMConfig{Provider: ProviderOllama}, want: true},
		{name: "test", cfg: LLMConfig{Provider: ProviderTest}, want: true},
		{name: "mixture", cfg: LLMConfig{Provider: ProviderMixture}, want: false},
		{name: "unknown", cfg: LLMConfig{Provider: LLMProvider("internal"), AuthMethod: AuthMethodAPIKey}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsCallableMixtureSlot(); got != tt.want {
				t.Fatalf("IsCallableMixtureSlot() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLLMConfigAuthHeaderValuePrefixSupportsRawAPIKeys(t *testing.T) {
	legacy := LLMConfig{}
	if got := legacy.GetAuthHeaderValuePrefix(); got != "Bearer " {
		t.Fatalf("legacy prefix = %q, want Bearer", got)
	}
	raw := LLMConfig{AuthHeaderName: "X-API-Key"}
	if got := raw.GetAuthHeaderValuePrefix(); got != "" {
		t.Fatalf("raw prefix = %q, want empty", got)
	}
	explicit := LLMConfig{AuthHeaderName: "X-API-Key", AuthHeaderValuePrefix: "Token "}
	if got := explicit.GetAuthHeaderValuePrefix(); got != "Token " {
		t.Fatalf("explicit prefix = %q, want Token", got)
	}
}

package models

import (
	"testing"
	"time"
)

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

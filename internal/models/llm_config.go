package models

import (
	"strings"
	"time"
)

type LLMProvider string

const (
	ProviderAnthropic        LLMProvider = "anthropic"
	ProviderOpenAI           LLMProvider = "openai"
	ProviderOpenAICompatible LLMProvider = "openai_compatible"
	ProviderOllama           LLMProvider = "ollama"
	ProviderMixture          LLMProvider = "mixture"
	ProviderTest             LLMProvider = "test"
)

// AuthMethod controls how configs authenticate.
type AuthMethod string

const (
	AuthMethodCLI    AuthMethod = "cli"     // Use Claude/Codex CLI (default)
	AuthMethodOAuth  AuthMethod = "oauth"   // Use OAuth tokens (Claude Max or OpenAI)
	AuthMethodAPIKey AuthMethod = "api_key" // Use API key directly (OpenAI)
)

type LLMConfig struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	Provider        LLMProvider `json:"provider"`
	Model           string      `json:"model"`
	ReasoningEffort string      `json:"reasoning_effort,omitempty"`
	APIKey          string      `json:"-"`
	MaxTokens       int         `json:"max_tokens"`
	Temperature     float64     `json:"temperature"`
	IsDefault       bool        `json:"is_default"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`

	// OAuth fields (used when AuthMethod=oauth for Claude Max or OpenAI)
	AuthMethod        AuthMethod `json:"auth_method"`
	OAuthAccessToken  string     `json:"-"` // Never serialized
	OAuthRefreshToken string     `json:"-"` // Never serialized
	OAuthExpiresAt    int64      `json:"oauth_expires_at,omitempty"`
	OAuthAccountID    string     `json:"-"` // ChatGPT workspace/account id for OpenAI OAuth

	// Per-model worker pool configuration
	MaxWorkers    int `json:"max_workers"`    // 0 = use global default
	WorkerTimeout int `json:"worker_timeout"` // Inactivity timeout; 0 = use global default (seconds)

	// Configurable OAuth endpoints (used for OpenAI OAuth; Claude Max uses hardcoded endpoints)
	OAuthClientID     string `json:"-"` // Never serialized
	OAuthClientSecret string `json:"-"` // Never serialized
	OAuthAuthorizeURL string `json:"oauth_authorize_url,omitempty"`
	OAuthTokenURL     string `json:"oauth_token_url,omitempty"`
	OAuthScopes       string `json:"oauth_scopes,omitempty"`

	// Ollama-specific fields
	OllamaBaseURL string `json:"ollama_base_url,omitempty"` // e.g. "http://localhost:11434"

	// Provider-neutral endpoint fields used by OpenAI-compatible Chat Completions.
	BaseURL               string `json:"base_url,omitempty"`
	Transport             string `json:"transport,omitempty"`
	PresetSlug            string `json:"preset_slug,omitempty"`
	ModelsURL             string `json:"models_url,omitempty"`
	AuthHeaderName        string `json:"auth_header_name,omitempty"`
	AuthHeaderValuePrefix string `json:"auth_header_value_prefix,omitempty"`
	ExtraHeadersJSON      string `json:"-"`
	ExtraBodyJSON         string `json:"extra_body_json,omitempty"`
	DefaultMaxTokens      int    `json:"default_max_tokens,omitempty"`
	TokenExchangeFormat   string `json:"token_exchange_format,omitempty"`
	TokenRefreshFormat    string `json:"token_refresh_format,omitempty"`
	CustomAuthConfigJSON  string `json:"-"`
	CustomAuthStateJSON   string `json:"-"`
	OAuthConfigRevision   int64  `json:"-"`
	MixtureConfigJSON     string `json:"mixture_config_json,omitempty"`

	// Bounded Models-page summary fields populated by the compact card query.
	MixtureAggregatorID    string `json:"-"`
	MixtureAggregatorLabel string `json:"-"`
	MixtureReferenceCount  int    `json:"-"`

	// Auto-start configuration
	AutoStartTasks bool `json:"auto_start_tasks"` // When enabled, tasks created with this model start immediately
}

// IsOAuth returns true if this config uses OAuth authentication.
func (c *LLMConfig) IsOAuth() bool {
	return c.AuthMethod == AuthMethodOAuth &&
		(c.Provider == ProviderAnthropic || c.Provider == ProviderOpenAI || c.Provider == ProviderOpenAICompatible)
}

// IsAnthropicAPIKey returns true if this is an Anthropic config using API key authentication.
func (c *LLMConfig) IsAnthropicAPIKey() bool {
	return c.Provider == ProviderAnthropic && (c.AuthMethod == AuthMethodAPIKey || c.APIKey != "")
}

// IsAnthropicCLI returns true if this is an Anthropic config using CLI authentication.
func (c *LLMConfig) IsAnthropicCLI() bool {
	return c.Provider == ProviderAnthropic && c.AuthMethod == AuthMethodCLI && c.APIKey == "" && c.OAuthAccessToken == ""
}

// HasValidOAuthToken returns true if the OAuth token is present and not expired.
func (c *LLMConfig) HasValidOAuthToken() bool {
	if !c.IsOAuth() || c.OAuthAccessToken == "" {
		return false
	}
	if c.OAuthExpiresAt == 0 {
		return c.Provider == ProviderOpenAICompatible
	}
	return c.OAuthExpiresAt > time.Now().UnixMilli()
}

// IsOpenAIOAuth returns true if this is an OpenAI config using OAuth authentication.
func (c *LLMConfig) IsOpenAIOAuth() bool {
	return c.Provider == ProviderOpenAI && c.AuthMethod == AuthMethodOAuth
}

// IsOpenAIAPIKey returns true if this is an OpenAI config using API key authentication.
func (c *LLMConfig) IsOpenAIAPIKey() bool {
	return c.Provider == ProviderOpenAI && c.AuthMethod == AuthMethodAPIKey
}

// GetOllamaBaseURL returns the Ollama base URL, defaulting to localhost:11434.
func (c *LLMConfig) GetOllamaBaseURL() string {
	if c.OllamaBaseURL != "" {
		return c.OllamaBaseURL
	}
	return "http://localhost:11434"
}

// IsOpenAICompatibleAPIKey returns true if this config uses an OpenAI-compatible Chat Completions endpoint.
func (c *LLMConfig) IsOpenAICompatibleAPIKey() bool {
	return c.Provider == ProviderOpenAICompatible && c.AuthMethod == AuthMethodAPIKey
}

// IsCallableMixtureSlot returns true when this config can be used as a non-mixture
// aggregator/reference slot. Reference calls use direct no-tools requests, so
// CLI-backed provider rows are intentionally excluded.
func (c *LLMConfig) IsCallableMixtureSlot() bool {
	switch c.Provider {
	case ProviderOpenAI:
		return c.IsOpenAIAPIKey() || c.IsOpenAIOAuth()
	case ProviderAnthropic:
		return c.IsAnthropicAPIKey() || c.IsOAuth()
	case ProviderOpenAICompatible:
		return c.IsOpenAICompatibleAPIKey() || c.IsOAuth()
	case ProviderOllama, ProviderTest:
		return true
	default:
		return false
	}
}

// GetTransport returns the API transport for generic provider configs.
func (c *LLMConfig) GetTransport() string {
	if strings.TrimSpace(c.Transport) != "" {
		return strings.TrimSpace(c.Transport)
	}
	if c.Provider == ProviderOpenAICompatible {
		return "chat_completions"
	}
	return ""
}

// GetAuthHeaderName returns the inference auth header name.
func (c *LLMConfig) GetAuthHeaderName() string {
	if strings.TrimSpace(c.AuthHeaderName) != "" {
		return strings.TrimSpace(c.AuthHeaderName)
	}
	return "Authorization"
}

// GetAuthHeaderValuePrefix returns the inference auth header value prefix.
func (c *LLMConfig) GetAuthHeaderValuePrefix() string {
	if c.AuthHeaderValuePrefix != "" {
		return c.AuthHeaderValuePrefix
	}
	// A configured header name with an empty prefix intentionally requests a
	// raw API-key value. Legacy rows have both fields empty and retain Bearer.
	if strings.TrimSpace(c.AuthHeaderName) != "" {
		return ""
	}
	return "Bearer "
}

// GetDefaultMaxTokens returns the configured provider output cap or fallback.
func (c *LLMConfig) GetDefaultMaxTokens(fallback int) int {
	if c.DefaultMaxTokens > 0 {
		return c.DefaultMaxTokens
	}
	return fallback
}

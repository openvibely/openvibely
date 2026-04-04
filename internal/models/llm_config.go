package models

import "time"

type LLMProvider string

const (
	ProviderAnthropic LLMProvider = "anthropic"
	ProviderOpenAI    LLMProvider = "openai"
	ProviderOllama    LLMProvider = "ollama"
	ProviderTest      LLMProvider = "test"
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
	WorkerTimeout int `json:"worker_timeout"` // 0 = use global default (seconds)

	// Configurable OAuth endpoints (used for OpenAI OAuth; Claude Max uses hardcoded endpoints)
	OAuthClientID     string `json:"-"` // Never serialized
	OAuthClientSecret string `json:"-"` // Never serialized
	OAuthAuthorizeURL string `json:"oauth_authorize_url,omitempty"`
	OAuthTokenURL     string `json:"oauth_token_url,omitempty"`
	OAuthScopes       string `json:"oauth_scopes,omitempty"`

	// Ollama-specific fields
	OllamaBaseURL string `json:"ollama_base_url,omitempty"` // e.g. "http://localhost:11434"

	// Auto-start configuration
	AutoStartTasks bool `json:"auto_start_tasks"` // When enabled, tasks created with this model start immediately
}

// IsOAuth returns true if this config uses OAuth authentication.
func (c *LLMConfig) IsOAuth() bool {
	return c.AuthMethod == AuthMethodOAuth && (c.Provider == ProviderAnthropic || c.Provider == ProviderOpenAI)
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

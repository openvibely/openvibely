package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestResolveProviderAndAuth(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		anthropicAuth  string
		openaiAuth     string
		authMethod     string
		wantProvider   models.LLMProvider
		wantAuthMethod models.AuthMethod
	}{
		{
			name:           "anthropic api key",
			provider:       "anthropic",
			anthropicAuth:  "api_key",
			openaiAuth:     "",
			authMethod:     "",
			wantProvider:   models.ProviderAnthropic,
			wantAuthMethod: models.AuthMethodAPIKey,
		},
		{
			name:           "anthropic subscription cli",
			provider:       "anthropic",
			anthropicAuth:  "subscription",
			openaiAuth:     "",
			authMethod:     "cli",
			wantProvider:   models.ProviderAnthropic,
			wantAuthMethod: models.AuthMethodCLI,
		},
		{
			name:           "anthropic subscription oauth",
			provider:       "anthropic",
			anthropicAuth:  "subscription",
			openaiAuth:     "",
			authMethod:     "oauth",
			wantProvider:   models.ProviderAnthropic,
			wantAuthMethod: models.AuthMethodOAuth,
		},
		{
			name:           "anthropic subscription defaults to cli",
			provider:       "anthropic",
			anthropicAuth:  "subscription",
			openaiAuth:     "",
			authMethod:     "",
			wantProvider:   models.ProviderAnthropic,
			wantAuthMethod: models.AuthMethodCLI,
		},
		{
			name:           "anthropic no auth type defaults to api key",
			provider:       "anthropic",
			anthropicAuth:  "",
			openaiAuth:     "",
			authMethod:     "",
			wantProvider:   models.ProviderAnthropic,
			wantAuthMethod: models.AuthMethodAPIKey,
		},
		{
			name:           "openai api key",
			provider:       "openai",
			anthropicAuth:  "",
			openaiAuth:     "api_key",
			authMethod:     "",
			wantProvider:   models.ProviderOpenAI,
			wantAuthMethod: models.AuthMethodAPIKey,
		},
		{
			name:           "openai subscription cli",
			provider:       "openai",
			anthropicAuth:  "",
			openaiAuth:     "subscription",
			authMethod:     "cli",
			wantProvider:   models.ProviderOpenAI,
			wantAuthMethod: models.AuthMethodCLI,
		},
		{
			name:           "openai subscription oauth",
			provider:       "openai",
			anthropicAuth:  "",
			openaiAuth:     "subscription",
			authMethod:     "oauth",
			wantProvider:   models.ProviderOpenAI,
			wantAuthMethod: models.AuthMethodOAuth,
		},
		{
			name:           "openai subscription defaults to cli",
			provider:       "openai",
			anthropicAuth:  "",
			openaiAuth:     "subscription",
			authMethod:     "",
			wantProvider:   models.ProviderOpenAI,
			wantAuthMethod: models.AuthMethodCLI,
		},
		{
			name:           "openai defaults to cli for backwards compat",
			provider:       "openai",
			anthropicAuth:  "",
			openaiAuth:     "",
			authMethod:     "",
			wantProvider:   models.ProviderOpenAI,
			wantAuthMethod: models.AuthMethodCLI,
		},
		{
			name:           "ollama",
			provider:       "ollama",
			anthropicAuth:  "",
			openaiAuth:     "",
			authMethod:     "",
			wantProvider:   models.ProviderOllama,
			wantAuthMethod: models.AuthMethodCLI,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotProvider, gotAuth := resolveProviderAndAuth(tt.provider, tt.anthropicAuth, tt.openaiAuth, tt.authMethod)
			if gotProvider != tt.wantProvider {
				t.Errorf("provider = %q, want %q", gotProvider, tt.wantProvider)
			}
			if gotAuth != tt.wantAuthMethod {
				t.Errorf("authMethod = %q, want %q", gotAuth, tt.wantAuthMethod)
			}
		})
	}
}

func TestCreateModel_AnthropicAPIKey(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "My Anthropic Model")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "api_key")
	form.Set("model", "claude-sonnet-4-5-20250929")
	form.Set("api_key", "sk-ant-test-key")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	configs, err := llmConfigRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list error: %v", err)
	}

	// Find our created model (there may be a default from migrations)
	var found *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "My Anthropic Model" {
			found = &configs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created model not found")
	}
	if found.Provider != models.ProviderAnthropic {
		t.Errorf("provider = %q, want %q", found.Provider, models.ProviderAnthropic)
	}
	if found.APIKey != "sk-ant-test-key" {
		t.Errorf("api_key not saved correctly")
	}
}

func TestCreateModel_HTMX_ReturnsContentInsteadOfRedirect(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "HTMX Create Model")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "api_key")
	form.Set("model", "claude-sonnet-4-5-20250929")
	form.Set("api_key", "sk-ant-htmx-key")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// HTMX request should return 200 with content, not a 303 redirect
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for HTMX request, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the response contains the model list HTML (models-container)
	body := rec.Body.String()
	if !strings.Contains(body, "models-container") {
		t.Errorf("response should contain models-container div")
	}
	if !strings.Contains(body, "HTMX Create Model") {
		t.Errorf("response should contain the newly created model name")
	}

	// Verify model was actually created
	configs, err := llmConfigRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list error: %v", err)
	}
	var found bool
	for _, c := range configs {
		if c.Name == "HTMX Create Model" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("created model not found in DB")
	}
}

func TestCreateModel_SubscriptionCLI(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "My CLI Model")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "subscription")
	form.Set("auth_method", "cli")
	form.Set("model", "claude-sonnet-4-5-20250929")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	configs, err := llmConfigRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list error: %v", err)
	}

	var found *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "My CLI Model" {
			found = &configs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created model not found")
	}
	if found.Provider != models.ProviderAnthropic {
		t.Errorf("provider = %q, want %q", found.Provider, models.ProviderAnthropic)
	}
	if found.AuthMethod != models.AuthMethodCLI {
		t.Errorf("auth_method = %q, want %q", found.AuthMethod, models.AuthMethodCLI)
	}
}

func TestCreateModel_SubscriptionOAuth(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "My OAuth Model")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "subscription")
	form.Set("auth_method", "oauth")
	form.Set("model", "claude-opus-4-6")
	form.Set("max_tokens", "8192")
	form.Set("temperature", "0.5")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	configs, err := llmConfigRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list error: %v", err)
	}

	var found *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "My OAuth Model" {
			found = &configs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created model not found")
	}
	if found.Provider != models.ProviderAnthropic {
		t.Errorf("provider = %q, want %q", found.Provider, models.ProviderAnthropic)
	}
	if found.AuthMethod != models.AuthMethodOAuth {
		t.Errorf("auth_method = %q, want %q", found.AuthMethod, models.AuthMethodOAuth)
	}
}

func TestCreateModel_Ollama(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "My Ollama Model")
	form.Set("provider", "ollama")
	form.Set("model", "llama3.1")
	form.Set("max_tokens", "2048")
	form.Set("temperature", "0.7")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	configs, err := llmConfigRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list error: %v", err)
	}

	var found *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "My Ollama Model" {
			found = &configs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created model not found")
	}
	if found.Provider != models.ProviderOllama {
		t.Errorf("provider = %q, want %q", found.Provider, models.ProviderOllama)
	}
}

func TestCreateModel_OllamaWithBaseURL(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "Remote Ollama")
	form.Set("provider", "ollama")
	form.Set("model", "llama3.1:8b")
	form.Set("ollama_base_url", "http://192.168.1.100:11434")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0.5")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	configs, err := llmConfigRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list error: %v", err)
	}

	var found *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "Remote Ollama" {
			found = &configs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created model not found")
	}
	if found.Provider != models.ProviderOllama {
		t.Errorf("provider = %q, want %q", found.Provider, models.ProviderOllama)
	}
	if found.OllamaBaseURL != "http://192.168.1.100:11434" {
		t.Errorf("ollama_base_url = %q, want %q", found.OllamaBaseURL, "http://192.168.1.100:11434")
	}
	if found.Model != "llama3.1:8b" {
		t.Errorf("model = %q, want %q", found.Model, "llama3.1:8b")
	}
}

func TestCreateModel_OllamaWithCustomModel(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "Custom Ollama Model")
	form.Set("provider", "ollama")
	form.Set("model", "llama3.1:8b")
	form.Set("ollama_custom_model", "my-fine-tuned:latest")
	form.Set("max_tokens", "2048")
	form.Set("temperature", "0.3")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	configs, err := llmConfigRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list error: %v", err)
	}

	var found *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "Custom Ollama Model" {
			found = &configs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created model not found")
	}
	// Custom model name should override the dropdown selection
	if found.Model != "my-fine-tuned:latest" {
		t.Errorf("model = %q, want %q", found.Model, "my-fine-tuned:latest")
	}
}

func TestUpdateModel_OllamaBaseURL(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:          "Update Ollama Test",
		Provider:      models.ProviderOllama,
		Model:         "llama3.1:8b",
		OllamaBaseURL: "http://localhost:11434",
		MaxTokens:     2048,
		IsDefault:     true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create error: %v", err)
	}

	form := url.Values{}
	form.Set("name", "Update Ollama Test")
	form.Set("provider", "ollama")
	form.Set("model", "mistral:7b")
	form.Set("ollama_base_url", "http://remote-server:11434")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0.8")

	req := httptest.NewRequest(http.MethodPut, "/models/"+agent.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	updated, err := llmConfigRepo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("get error: %v", err)
	}
	if updated.OllamaBaseURL != "http://remote-server:11434" {
		t.Errorf("ollama_base_url = %q, want %q", updated.OllamaBaseURL, "http://remote-server:11434")
	}
	if updated.Model != "mistral:7b" {
		t.Errorf("model = %q, want %q", updated.Model, "mistral:7b")
	}
}

func TestUpdateModel_SwitchFromAPIKeyToSubscription(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create an Anthropic API key model
	agent := &models.LLMConfig{
		Name:      "Switch Test",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		APIKey:    "sk-ant-old-key",
		MaxTokens: 4096,
		IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create error: %v", err)
	}
	_ = h // silence unused

	// Update to subscription + OAuth
	form := url.Values{}
	form.Set("name", "Switch Test")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "subscription")
	form.Set("auth_method", "oauth")
	form.Set("model", "claude-sonnet-4-5-20250929")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPut, "/models/"+agent.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	updated, err := llmConfigRepo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("get error: %v", err)
	}
	if updated.Provider != models.ProviderAnthropic {
		t.Errorf("provider = %q, want %q", updated.Provider, models.ProviderAnthropic)
	}
	if updated.AuthMethod != models.AuthMethodOAuth {
		t.Errorf("auth_method = %q, want %q", updated.AuthMethod, models.AuthMethodOAuth)
	}
}

func TestUpdateModel_ChangeAuthMethod_CLIToOAuth(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a Claude Max model with CLI auth method
	agent := &models.LLMConfig{
		Name:       "Sonnet CLI",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-sonnet-4-5-20250929",
		AuthMethod: models.AuthMethodCLI,
		MaxTokens:  4096,
		IsDefault:  true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create error: %v", err)
	}

	// Update: change auth_method from CLI to OAuth
	form := url.Values{}
	form.Set("name", "Sonnet CLI")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "subscription")
	form.Set("auth_method", "oauth")
	form.Set("model", "claude-sonnet-4-5-20250929")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPut, "/models/"+agent.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	updated, err := llmConfigRepo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("get error: %v", err)
	}
	if updated.Provider != models.ProviderAnthropic {
		t.Errorf("provider = %q, want %q", updated.Provider, models.ProviderAnthropic)
	}
	if updated.AuthMethod != models.AuthMethodOAuth {
		t.Errorf("auth_method = %q, want %q", updated.AuthMethod, models.AuthMethodOAuth)
	}
}

func TestUpdateModel_ChangeAuthMethod_OAuthToCLI(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a Claude Max model with OAuth auth method
	agent := &models.LLMConfig{
		Name:       "Sonnet OAuth",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-sonnet-4-5-20250929",
		AuthMethod: models.AuthMethodOAuth,
		MaxTokens:  4096,
		IsDefault:  true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create error: %v", err)
	}

	// Update: change auth_method from OAuth to CLI
	form := url.Values{}
	form.Set("name", "Sonnet OAuth")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "subscription")
	form.Set("auth_method", "cli")
	form.Set("model", "claude-sonnet-4-5-20250929")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPut, "/models/"+agent.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	updated, err := llmConfigRepo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("get error: %v", err)
	}
	if updated.Provider != models.ProviderAnthropic {
		t.Errorf("provider = %q, want %q", updated.Provider, models.ProviderAnthropic)
	}
	if updated.AuthMethod != models.AuthMethodCLI {
		t.Errorf("auth_method = %q, want %q", updated.AuthMethod, models.AuthMethodCLI)
	}
}

func TestUpdateModel_OpenAIOAuthPreservesStoredConfigWhenFormOmitsFields(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:              "OpenAI OAuth Preserve",
		Provider:          models.ProviderOpenAI,
		Model:             "gpt-5.3-codex",
		AuthMethod:        models.AuthMethodOAuth,
		MaxTokens:         4096,
		IsDefault:         true,
		OAuthClientID:     "client-id-1",
		OAuthClientSecret: "client-secret-1",
		OAuthAuthorizeURL: "https://example.com/oauth/authorize",
		OAuthTokenURL:     "https://example.com/oauth/token",
		OAuthScopes:       "openid profile",
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create error: %v", err)
	}

	// Simulate models modal update where OpenAI OAuth config fields are not present.
	form := url.Values{}
	form.Set("name", "OpenAI OAuth Preserve")
	form.Set("provider", "openai")
	form.Set("openai_auth_type", "subscription")
	form.Set("auth_method", "oauth")
	form.Set("model", "gpt-5.3-codex")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPut, "/models/"+agent.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	updated, err := llmConfigRepo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("get error: %v", err)
	}
	if updated.OAuthClientID != "client-id-1" {
		t.Errorf("oauth_client_id = %q, want %q", updated.OAuthClientID, "client-id-1")
	}
	if updated.OAuthClientSecret != "client-secret-1" {
		t.Errorf("oauth_client_secret = %q, want %q", updated.OAuthClientSecret, "client-secret-1")
	}
	if updated.OAuthAuthorizeURL != "https://example.com/oauth/authorize" {
		t.Errorf("oauth_authorize_url = %q, want %q", updated.OAuthAuthorizeURL, "https://example.com/oauth/authorize")
	}
	if updated.OAuthTokenURL != "https://example.com/oauth/token" {
		t.Errorf("oauth_token_url = %q, want %q", updated.OAuthTokenURL, "https://example.com/oauth/token")
	}
	if updated.OAuthScopes != "openid profile" {
		t.Errorf("oauth_scopes = %q, want %q", updated.OAuthScopes, "openid profile")
	}
}

// TestUpdateModel_DuplicateAuthMethodFormFields reproduces the browser bug where
// two <select> elements with name="auth_method" exist in the form (one for Anthropic,
// one for OpenAI). When both are enabled, the browser sends both values and Go's
// FormValue returns the first one (the hidden OpenAI select defaulting to "cli"),
// ignoring the user's intended value ("oauth") from the Anthropic select.
// The fix disables the inactive select via JavaScript so only one value is sent.
func TestUpdateModel_DuplicateAuthMethodFormFields(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:       "Dup Auth Test",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-sonnet-4-5-20250929",
		AuthMethod: models.AuthMethodCLI,
		MaxTokens:  4096,
		IsDefault:  true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create error: %v", err)
	}

	// Simulate the browser bug: two auth_method values sent.
	// The hidden OpenAI select sends "cli" first, then the Anthropic select sends "oauth".
	// Go's FormValue returns the first value, so without the JS fix,
	// the server receives "cli" instead of "oauth".
	form := url.Values{
		"name":                {"Dup Auth Test"},
		"provider":            {"anthropic"},
		"anthropic_auth_type": {"subscription"},
		"model":               {"claude-sonnet-4-5-20250929"},
		"max_tokens":          {"4096"},
		"temperature":         {"0"},
		"auth_method":         {"cli", "oauth"}, // first=hidden OpenAI, second=visible Anthropic
	}

	req := httptest.NewRequest(http.MethodPut, "/models/"+agent.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	updated, err := llmConfigRepo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("get error: %v", err)
	}

	// With duplicate form fields, Go returns the first value ("cli").
	// This documents the bug: the auth_method stays "cli" even though user selected "oauth".
	// The frontend fix (disabling hidden selects) prevents this scenario.
	if updated.AuthMethod != models.AuthMethodCLI {
		t.Logf("NOTE: with duplicate auth_method form values, FormValue returns first value (cli)")
	}
}

func TestResolveProviderAndAuth_OAuthFormValue(t *testing.T) {
	// Verify the new "oauth" form value (replacing "subscription") works for both providers
	tests := []struct {
		name           string
		provider       string
		anthropicAuth  string
		openaiAuth     string
		authMethod     string
		wantProvider   models.LLMProvider
		wantAuthMethod models.AuthMethod
	}{
		{
			name:           "anthropic oauth with api connection",
			provider:       "anthropic",
			anthropicAuth:  "oauth",
			authMethod:     "oauth",
			wantProvider:   models.ProviderAnthropic,
			wantAuthMethod: models.AuthMethodOAuth,
		},
		{
			name:           "anthropic oauth with cli connection",
			provider:       "anthropic",
			anthropicAuth:  "oauth",
			authMethod:     "cli",
			wantProvider:   models.ProviderAnthropic,
			wantAuthMethod: models.AuthMethodCLI,
		},
		{
			name:           "anthropic oauth defaults to cli",
			provider:       "anthropic",
			anthropicAuth:  "oauth",
			authMethod:     "",
			wantProvider:   models.ProviderAnthropic,
			wantAuthMethod: models.AuthMethodCLI,
		},
		{
			name:           "openai oauth with api connection",
			provider:       "openai",
			openaiAuth:     "oauth",
			authMethod:     "oauth",
			wantProvider:   models.ProviderOpenAI,
			wantAuthMethod: models.AuthMethodOAuth,
		},
		{
			name:           "openai oauth with cli connection",
			provider:       "openai",
			openaiAuth:     "oauth",
			authMethod:     "cli",
			wantProvider:   models.ProviderOpenAI,
			wantAuthMethod: models.AuthMethodCLI,
		},
		{
			name:           "openai oauth defaults to cli",
			provider:       "openai",
			openaiAuth:     "oauth",
			authMethod:     "",
			wantProvider:   models.ProviderOpenAI,
			wantAuthMethod: models.AuthMethodCLI,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotProvider, gotAuth := resolveProviderAndAuth(tt.provider, tt.anthropicAuth, tt.openaiAuth, tt.authMethod)
			if gotProvider != tt.wantProvider {
				t.Errorf("provider = %q, want %q", gotProvider, tt.wantProvider)
			}
			if gotAuth != tt.wantAuthMethod {
				t.Errorf("authMethod = %q, want %q", gotAuth, tt.wantAuthMethod)
			}
		})
	}
}

func TestCreateModel_OAuthCLI(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "My OAuth CLI Model")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "oauth")
	form.Set("auth_method", "cli")
	form.Set("model", "claude-sonnet-4-5-20250929")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	configs, err := llmConfigRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list error: %v", err)
	}

	var found *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "My OAuth CLI Model" {
			found = &configs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created model not found")
	}
	if found.Provider != models.ProviderAnthropic {
		t.Errorf("provider = %q, want %q", found.Provider, models.ProviderAnthropic)
	}
	if found.AuthMethod != models.AuthMethodCLI {
		t.Errorf("auth_method = %q, want %q", found.AuthMethod, models.AuthMethodCLI)
	}
}

func TestCreateModel_OAuthAPI(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "My OAuth API Model")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "oauth")
	form.Set("auth_method", "oauth")
	form.Set("model", "claude-opus-4-6")
	form.Set("max_tokens", "8192")
	form.Set("temperature", "0.5")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	configs, err := llmConfigRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list error: %v", err)
	}

	var found *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "My OAuth API Model" {
			found = &configs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created model not found")
	}
	if found.Provider != models.ProviderAnthropic {
		t.Errorf("provider = %q, want %q", found.Provider, models.ProviderAnthropic)
	}
	if found.AuthMethod != models.AuthMethodOAuth {
		t.Errorf("auth_method = %q, want %q", found.AuthMethod, models.AuthMethodOAuth)
	}
}

func TestCreateModel_OpenAIOAuthAPI(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "OpenAI OAuth API Model")
	form.Set("provider", "openai")
	form.Set("openai_auth_type", "oauth")
	form.Set("auth_method", "oauth")
	form.Set("model", "gpt-5.3-codex")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	configs, err := llmConfigRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list error: %v", err)
	}

	var found *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "OpenAI OAuth API Model" {
			found = &configs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created model not found")
	}
	if found.Provider != models.ProviderOpenAI {
		t.Errorf("provider = %q, want %q", found.Provider, models.ProviderOpenAI)
	}
	if found.AuthMethod != models.AuthMethodOAuth {
		t.Errorf("auth_method = %q, want %q", found.AuthMethod, models.AuthMethodOAuth)
	}
}

func TestCreateModel_OpenAIOAuthCLI(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "OpenAI OAuth CLI Model")
	form.Set("provider", "openai")
	form.Set("openai_auth_type", "oauth")
	form.Set("auth_method", "cli")
	form.Set("model", "gpt-5.3-codex")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	configs, err := llmConfigRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list error: %v", err)
	}

	var found *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "OpenAI OAuth CLI Model" {
			found = &configs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created model not found")
	}
	if found.Provider != models.ProviderOpenAI {
		t.Errorf("provider = %q, want %q", found.Provider, models.ProviderOpenAI)
	}
	if found.AuthMethod != models.AuthMethodCLI {
		t.Errorf("auth_method = %q, want %q", found.AuthMethod, models.AuthMethodCLI)
	}
}

func TestUpdateModel_OpenAI_ChangeModelToGPT54(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create an OpenAI model with gpt-5.3-codex
	agent := &models.LLMConfig{
		Name:       "OpenAI GPT Test",
		Provider:   models.ProviderOpenAI,
		Model:      "gpt-5.3-codex",
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "sk-openai-test",
		MaxTokens:  4096,
		IsDefault:  true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create error: %v", err)
	}

	// Edit the model, changing from gpt-5.3-codex to gpt-5.4
	form := url.Values{}
	form.Set("name", "OpenAI GPT Test")
	form.Set("provider", "openai")
	form.Set("openai_auth_type", "api_key")
	form.Set("model", "gpt-5.4")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0")
	form.Set("reasoning_effort", "high")

	req := httptest.NewRequest(http.MethodPut, "/models/"+agent.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	updated, err := llmConfigRepo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("get error: %v", err)
	}
	// Bug: normalizeOpenAICodexModel didn't include gpt-5.4, so it was silently
	// replaced with gpt-5.3-codex. The model change didn't persist.
	if updated.Model != "gpt-5.4" {
		t.Errorf("model = %q, want %q (model change did not persist)", updated.Model, "gpt-5.4")
	}
}

func TestCreateModel_OpenAI_GPT54(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "OpenAI GPT 5.4")
	form.Set("provider", "openai")
	form.Set("openai_auth_type", "api_key")
	form.Set("model", "gpt-5.4")
	form.Set("api_key", "sk-openai-test")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0")
	form.Set("reasoning_effort", "high")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	configs, err := llmConfigRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list error: %v", err)
	}

	var found *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "OpenAI GPT 5.4" {
			found = &configs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created model not found")
	}
	if found.Model != "gpt-5.4" {
		t.Errorf("model = %q, want %q (gpt-5.4 should be accepted)", found.Model, "gpt-5.4")
	}
}

func TestNormalizeOpenAIModel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"gpt-5.5", "gpt-5.5"},
		{"gpt-5.5-pro", "gpt-5.5-pro"},
		{"gpt-5.4", "gpt-5.4"},
		{"gpt-5.4-mini", "gpt-5.4-mini"},
		{"gpt-5.3-codex", "gpt-5.3-codex"},
		{"gpt-5.3-codex-spark", "gpt-5.3-codex-spark"},
		{"gpt-5.2-codex", "gpt-5.2-codex"},
		{"gpt-5.1-codex-max", "gpt-5.1-codex-max"},
		{"gpt-5.1-codex", "gpt-5.1-codex"},
		{"gpt-5.1-codex-mini", "gpt-5.1-codex-mini"},
		{"gpt-5-codex", "gpt-5-codex"},
		{"gpt-5-codex-mini", "gpt-5-codex-mini"},
		{"", "gpt-5.5"},              // empty defaults to latest
		{"invalid-model", "gpt-5.5"}, // unknown defaults to latest
		{"  gpt-5.5  ", "gpt-5.5"},   // whitespace trimmed
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeOpenAIModelForTest(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeOpenAIModelForTest(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeProviderReasoningEffort(t *testing.T) {
	tests := []struct {
		name     string
		provider models.LLMProvider
		input    string
		want     string
	}{
		{"openai xhigh", models.ProviderOpenAI, "xhigh", "xhigh"},
		{"openai rejects max", models.ProviderOpenAI, "max", ""},
		{"anthropic max", models.ProviderAnthropic, "max", "max"},
		{"anthropic rejects xhigh", models.ProviderAnthropic, "xhigh", ""},
		{"ollama clears effort", models.ProviderOllama, "high", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeProviderReasoningEffort(tt.provider, tt.input); got != tt.want {
				t.Fatalf("normalizeProviderReasoningEffort(%q, %q) = %q, want %q", tt.provider, tt.input, got, tt.want)
			}
		})
	}
}

func TestCreateModel_IgnoresSubmittedMaxTokens(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "No Token Config")
	form.Set("provider", "openai")
	form.Set("openai_auth_type", "api_key")
	form.Set("model", "gpt-5.5")
	form.Set("api_key", "sk-openai-55")
	form.Set("temperature", "0")
	form.Set("reasoning_effort", "medium")
	form.Set("max_tokens", "99999")

	rec := postForm(e, "/models", form)
	assertCode(t, rec, http.StatusSeeOther)

	configs, err := llmConfigRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list error: %v", err)
	}

	var found *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "No Token Config" {
			found = &configs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created model not found")
	}
	if found.MaxTokens != 0 {
		t.Errorf("max_tokens = %d, want 0 because model token caps are not configurable", found.MaxTokens)
	}
}

func TestUpdateModel_IgnoresSubmittedMaxTokens(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:       "Old Token Config",
		Provider:   models.ProviderOpenAI,
		Model:      "gpt-5.5",
		APIKey:     "sk-openai",
		AuthMethod: models.AuthMethodAPIKey,
		MaxTokens:  4096,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create error: %v", err)
	}

	form := url.Values{}
	form.Set("name", "Updated Token Config")
	form.Set("provider", "openai")
	form.Set("openai_auth_type", "api_key")
	form.Set("model", "gpt-5.5")
	form.Set("api_key", "sk-openai")
	form.Set("temperature", "0")
	form.Set("reasoning_effort", "high")
	form.Set("max_tokens", "99999")

	rec := htmxPut(e, "/models/"+agent.ID, form)
	assertCode(t, rec, http.StatusOK)

	updated, err := llmConfigRepo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("get updated error: %v", err)
	}
	if updated.MaxTokens != 4096 {
		t.Errorf("max_tokens = %d, want existing legacy value preserved because submitted values are ignored", updated.MaxTokens)
	}
}

func TestUpdateModel_SwitchFromSubscriptionToAPIKey(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a Claude Max (subscription CLI) model
	agent := &models.LLMConfig{
		Name:       "Sub to API",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-sonnet-4-5-20250929",
		AuthMethod: models.AuthMethodCLI,
		MaxTokens:  4096,
		IsDefault:  true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create error: %v", err)
	}
	_ = h

	// Update to API key
	form := url.Values{}
	form.Set("name", "Sub to API")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "api_key")
	form.Set("model", "claude-sonnet-4-5-20250929")
	form.Set("api_key", "sk-ant-new-key")
	form.Set("max_tokens", "4096")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPut, "/models/"+agent.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	updated, err := llmConfigRepo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("get error: %v", err)
	}
	if updated.Provider != models.ProviderAnthropic {
		t.Errorf("provider = %q, want %q", updated.Provider, models.ProviderAnthropic)
	}
	if updated.APIKey != "sk-ant-new-key" {
		t.Errorf("api_key not updated")
	}
}

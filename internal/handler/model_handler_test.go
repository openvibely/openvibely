package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	llmcustomauth "github.com/openvibely/openvibely/internal/llm/customauth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
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
			name:           "anthropic subscription legacy cli normalizes to oauth",
			provider:       "anthropic",
			anthropicAuth:  "subscription",
			openaiAuth:     "",
			authMethod:     "cli",
			wantProvider:   models.ProviderAnthropic,
			wantAuthMethod: models.AuthMethodOAuth,
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
			name:           "anthropic subscription defaults to oauth when auth_method absent",
			provider:       "anthropic",
			anthropicAuth:  "subscription",
			openaiAuth:     "",
			authMethod:     "",
			wantProvider:   models.ProviderAnthropic,
			wantAuthMethod: models.AuthMethodOAuth,
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
			name:           "openai subscription legacy cli normalizes to oauth",
			provider:       "openai",
			anthropicAuth:  "",
			openaiAuth:     "subscription",
			authMethod:     "cli",
			wantProvider:   models.ProviderOpenAI,
			wantAuthMethod: models.AuthMethodOAuth,
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
			name:           "openai subscription defaults to oauth when auth_method absent",
			provider:       "openai",
			anthropicAuth:  "",
			openaiAuth:     "subscription",
			authMethod:     "",
			wantProvider:   models.ProviderOpenAI,
			wantAuthMethod: models.AuthMethodOAuth,
		},
		{
			name:           "openai defaults to api key",
			provider:       "openai",
			anthropicAuth:  "",
			openaiAuth:     "",
			authMethod:     "",
			wantProvider:   models.ProviderOpenAI,
			wantAuthMethod: models.AuthMethodAPIKey,
		},
		{
			name:           "openai compatible api key",
			provider:       "openai_compatible",
			anthropicAuth:  "",
			openaiAuth:     "",
			authMethod:     "",
			wantProvider:   models.ProviderOpenAICompatible,
			wantAuthMethod: models.AuthMethodAPIKey,
		},
		{
			name:           "openrouter provider preset maps to openai compatible api key",
			provider:       "openai_compatible_openrouter",
			anthropicAuth:  "",
			openaiAuth:     "",
			authMethod:     "",
			wantProvider:   models.ProviderOpenAICompatible,
			wantAuthMethod: models.AuthMethodAPIKey,
		},
		{
			name:           "new provider preset maps to openai compatible api key",
			provider:       "openai_compatible_groq",
			anthropicAuth:  "",
			openaiAuth:     "",
			authMethod:     "",
			wantProvider:   models.ProviderOpenAICompatible,
			wantAuthMethod: models.AuthMethodAPIKey,
		},
		{
			name:           "excluded provider preset is not normalized to openai compatible",
			provider:       "openai_compatible_xai",
			anthropicAuth:  "",
			openaiAuth:     "",
			authMethod:     "",
			wantProvider:   models.LLMProvider("openai_compatible_xai"),
			wantAuthMethod: models.AuthMethodAPIKey,
		}, {
			name:           "ollama",
			provider:       "ollama",
			anthropicAuth:  "",
			openaiAuth:     "",
			authMethod:     "",
			wantProvider:   models.ProviderOllama,
			wantAuthMethod: models.AuthMethodAPIKey,
		}}

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

func TestListOpenAICompatibleAvailableModelsUsesBaseModelsEndpoint(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	_, e, _ := setupTestHandler(t)
	var gotPath string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"local-model"}]}`))
	}))
	defer srv.Close()

	req := httptest.NewRequest(http.MethodGet, "/models/openai-compatible/available?allow_private=1&base_url="+url.QueryEscape(srv.URL+"/v1"), nil)
	req.Header.Set("X-OpenAI-Compatible-API-Key", "sk-test")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/models" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("auth = %q", gotAuth)
	}
	var out openAICompatibleModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(out.Models) != 1 || out.Models[0].ID != "local-model" || out.ResolvedID != "local-model" {
		t.Fatalf("unexpected response: %+v", out)
	}
}

func TestListOpenAICompatibleAvailableModelsUsesSavedCustomHeaders(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	_, e, repo := setupTestHandler(t)
	var gotAPIKey, gotRequiredHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("X-API-Key")
		gotRequiredHeader = r.Header.Get("X-Required-Header")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"configured-model"}]}`))
	}))
	defer srv.Close()

	agent := &models.LLMConfig{
		Name:                  "Configured discovery",
		Provider:              models.ProviderOpenAICompatible,
		AuthMethod:            models.AuthMethodAPIKey,
		Model:                 "configured-model",
		APIKey:                "saved-key",
		BaseURL:               srv.URL + "/v1",
		PresetSlug:            "vllm",
		AuthHeaderName:        "X-API-Key",
		AuthHeaderValuePrefix: "Token ",
		ExtraHeadersJSON:      `{"X-Required-Header":"required"}`,
		CustomAuthConfigJSON: llmcustomauth.MarshalConfig(llmcustomauth.Config{
			ModelsArrayPath: "models",
			ModelIDField:    "name",
		}),
	}
	if err := repo.Create(context.Background(), agent); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/models/openai-compatible/available?config_id="+url.QueryEscape(agent.ID), nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotAPIKey != "Token saved-key" || gotRequiredHeader != "required" {
		t.Fatalf("custom discovery headers = X-API-Key %q, X-Required-Header %q", gotAPIKey, gotRequiredHeader)
	}
	if !strings.Contains(rec.Body.String(), `"id":"configured-model"`) {
		t.Fatalf("unexpected discovered models: %s", rec.Body.String())
	}
}

func TestListOpenAICompatibleAvailableModelsUsesLiveEditsForSavedAPIKeyConfig(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	_, e, repo := setupTestHandler(t)
	var gotLiveKey, gotStoredKey, gotRequiredHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLiveKey = r.Header.Get("X-Live-Key")
		gotStoredKey = r.Header.Get("X-Stored-Key")
		gotRequiredHeader = r.Header.Get("X-Required-Header")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"inventory":[{"slug":"live-model"}]}`))
	}))
	defer srv.Close()

	agent := &models.LLMConfig{
		Name:                  "Edited discovery",
		Provider:              models.ProviderOpenAICompatible,
		AuthMethod:            models.AuthMethodAPIKey,
		Model:                 "stored-model",
		APIKey:                "stored-key",
		BaseURL:               srv.URL,
		PresetSlug:            "custom",
		ModelsURL:             srv.URL + "/models",
		AuthHeaderName:        "X-Stored-Key",
		AuthHeaderValuePrefix: "Token ",
		ExtraHeadersJSON:      `{"X-Required-Header":"stored"}`,
		CustomAuthConfigJSON: llmcustomauth.MarshalConfig(llmcustomauth.Config{
			ModelsArrayPath:       "data",
			ModelIDField:          "id",
			AllowPrivateEndpoints: true,
		}),
	}
	if err := repo.Create(context.Background(), agent); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/models/openai-compatible/available?config_id="+url.QueryEscape(agent.ID), nil)
	req.Header.Set(openAICompatibleAPIKeyHeader, "live-key")
	req.Header.Set(openAICompatibleAuthHeaderNameHeader, "X-Live-Key")
	req.Header.Set(openAICompatibleAuthHeaderPrefixHeader, "")
	req.Header.Set(openAICompatibleExtraHeadersHeader, `{"X-Required-Header":"live"}`)
	req.Header.Set(openAICompatibleModelsArrayPathHeader, "inventory")
	req.Header.Set(openAICompatibleModelIDFieldHeader, "slug")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotLiveKey != "live-key" || gotStoredKey != "" || gotRequiredHeader != "live" {
		t.Fatalf("live discovery headers = X-Live-Key %q, X-Stored-Key %q, X-Required-Header %q", gotLiveKey, gotStoredKey, gotRequiredHeader)
	}
	if !strings.Contains(rec.Body.String(), `"id":"live-model"`) {
		t.Fatalf("unexpected discovered models: %s", rec.Body.String())
	}

	gotRequiredHeader = "not-requested"
	req = httptest.NewRequest(http.MethodGet, "/models/openai-compatible/available?config_id="+url.QueryEscape(agent.ID), nil)
	req.Header.Set(openAICompatibleAuthHeaderNameHeader, "X-Live-Key")
	req.Header.Set(openAICompatibleExtraHeadersHeader, `{}`)
	req.Header.Set(openAICompatibleModelsArrayPathHeader, "inventory")
	req.Header.Set(openAICompatibleModelIDFieldHeader, "slug")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected clearing live headers to succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotRequiredHeader != "" {
		t.Fatalf("expected live discovery to clear saved required header, got %q", gotRequiredHeader)
	}
}

func TestListOpenAICompatibleAvailableModelsUsesUnsavedCustomRequestSettings(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	_, e, _ := setupTestHandler(t)
	var gotAPIKey, gotRequiredHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("X-API-Key")
		gotRequiredHeader = r.Header.Get("X-Required-Header")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"live-model"}]}`))
	}))
	defer srv.Close()

	req := httptest.NewRequest(http.MethodGet, "/models/openai-compatible/available?allow_private=1&base_url="+url.QueryEscape(srv.URL), nil)
	req.Header.Set(openAICompatibleAPIKeyHeader, "live-key")
	req.Header.Set(openAICompatibleAuthHeaderNameHeader, "X-API-Key")
	req.Header.Set(openAICompatibleAuthHeaderPrefixHeader, "")
	req.Header.Set(openAICompatibleExtraHeadersHeader, `{"X-Required-Header":"required"}`)
	req.Header.Set(openAICompatibleModelsArrayPathHeader, "models")
	req.Header.Set(openAICompatibleModelIDFieldHeader, "name")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotAPIKey != "live-key" || gotRequiredHeader != "required" {
		t.Fatalf("live discovery headers = X-API-Key %q, X-Required-Header %q", gotAPIKey, gotRequiredHeader)
	}
	if !strings.Contains(rec.Body.String(), `"id":"live-model"`) {
		t.Fatalf("unexpected discovered models: %s", rec.Body.String())
	}
}

func TestCustomOAuthModelDiscoveryUsesSharedExtraHeaders(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	var gotRequiredHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRequiredHeader = r.Header.Get("X-Required-Header")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"oauth-model"}]}`))
	}))
	defer server.Close()

	h, _, repo := setupTestHandler(t)
	agent := &models.LLMConfig{
		Name:             "OAuth discovery headers",
		Provider:         models.ProviderOpenAICompatible,
		AuthMethod:       models.AuthMethodOAuth,
		Model:            "oauth-model",
		BaseURL:          server.URL,
		ModelsURL:        server.URL + "/models",
		OAuthAccessToken: "access-token",
		ExtraHeadersJSON: `{"X-Required-Header":"required"}`,
		CustomAuthConfigJSON: llmcustomauth.MarshalConfig(llmcustomauth.Config{
			Enabled:               true,
			AllowPrivateEndpoints: true,
		}),
	}
	if err := repo.Create(context.Background(), agent); err != nil {
		t.Fatal(err)
	}

	found, err := h.fetchCustomOpenAICompatibleModels(
		context.Background(),
		llmcustomauth.NewHTTPClient(time.Second, true),
		agent.ModelsURL,
		*agent,
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotRequiredHeader != "required" {
		t.Fatalf("X-Required-Header = %q", gotRequiredHeader)
	}
	if len(found) != 1 || found[0].ID != "oauth-model" {
		t.Fatalf("unexpected discovered models: %+v", found)
	}
}

func TestListOpenAICompatibleAvailableModelsRejectsUnsavedEndpointChanges(t *testing.T) {
	_, e, repo := setupTestHandler(t)
	agent := &models.LLMConfig{
		Name:       "Configured discovery",
		Provider:   models.ProviderOpenAICompatible,
		AuthMethod: models.AuthMethodAPIKey,
		Model:      "configured-model",
		BaseURL:    "https://saved.example.test/v1",
		ModelsURL:  "https://saved.example.test/models",
	}
	if err := repo.Create(context.Background(), agent); err != nil {
		t.Fatal(err)
	}

	query := url.Values{
		"config_id":  {agent.ID},
		"base_url":   {"https://edited.example.test/v1"},
		"models_url": {"https://edited.example.test/models"},
	}
	req := httptest.NewRequest(http.MethodGet, "/models/openai-compatible/available?"+query.Encode(), nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "save endpoint changes") {
		t.Fatalf("unexpected response: %s", rec.Body.String())
	}
}

func TestCustomOAuthModelDiscoveryRejectsStaleConfigurationBeforeRequest(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	requested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requested = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model"}]}`))
	}))
	defer server.Close()

	h, _, repo := setupTestHandler(t)
	cfg := llmcustomauth.Config{Enabled: true, AllowPrivateEndpoints: true}
	agent := &models.LLMConfig{
		Name:                 "Revision guarded discovery",
		Provider:             models.ProviderOpenAICompatible,
		AuthMethod:           models.AuthMethodOAuth,
		Model:                "model",
		BaseURL:              server.URL,
		ModelsURL:            server.URL + "/models",
		OAuthAccessToken:     "access-token",
		CustomAuthConfigJSON: llmcustomauth.MarshalConfig(cfg),
	}
	if err := repo.Create(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	snapshot := *agent
	current, err := repo.GetByID(context.Background(), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Name = "Edited while discovery was queued"
	if err := repo.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}

	_, err = h.fetchCustomOpenAICompatibleModels(
		context.Background(),
		llmcustomauth.NewHTTPClient(time.Second, true),
		snapshot.ModelsURL,
		snapshot,
	)
	if err == nil || !strings.Contains(err.Error(), "configuration changed") {
		t.Fatalf("discovery error = %v", err)
	}
	if requested {
		t.Fatal("stale discovery made an outbound request")
	}
}

func TestListOpenAICompatibleAvailableModelsFallsBackToV1Models(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	_, e, _ := setupTestHandler(t)
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"fallback-model"},{"id":"other-model"}]}`))
	}))
	defer srv.Close()

	req := httptest.NewRequest(http.MethodGet, "/models/openai-compatible/available?allow_private=1&base_url="+url.QueryEscape(srv.URL), nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(paths) != 2 || paths[0] != "/models" || paths[1] != "/v1/models" {
		t.Fatalf("paths = %#v", paths)
	}
	var out openAICompatibleModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(out.Models) != 2 || out.ResolvedID != "" {
		t.Fatalf("unexpected response: %+v", out)
	}
}

func TestCreateModel_Mixture(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agg := &models.LLMConfig{Name: "Aggregator", Provider: models.ProviderTest, Model: "agg"}
	ref := &models.LLMConfig{Name: "Reference", Provider: models.ProviderTest, Model: "ref"}
	if err := llmConfigRepo.Create(ctx, agg); err != nil {
		t.Fatalf("create aggregator: %v", err)
	}
	if err := llmConfigRepo.Create(ctx, ref); err != nil {
		t.Fatalf("create reference: %v", err)
	}

	form := url.Values{}
	form.Set("name", "Research Mixture")
	form.Set("provider", "mixture")
	form.Set("model", "research-heavy")
	form.Set("mixture_enabled", "on")
	form.Set("mixture_aggregator_id", agg.ID)
	form.Add("mixture_reference_ids", ref.ID)
	form.Set("mixture_reference_timeout_seconds", "45")
	form.Set("mixture_max_reference_workers", "4")
	form.Set("mixture_reference_temperature", "0.7")
	form.Set("mixture_aggregator_temperature", "0.2")
	form.Set("temperature", "0.9")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}
	configs, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var created *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "Research Mixture" {
			created = &configs[i]
			break
		}
	}
	if created == nil {
		t.Fatal("created mixture not found")
	}
	if created.Provider != models.ProviderMixture || created.AuthMethod != models.AuthMethodAPIKey || created.APIKey != "" {
		t.Fatalf("unexpected mixture provider/auth: %+v", created)
	}
	var cfg struct {
		Enabled         bool `json:"enabled"`
		ReferenceModels []struct {
			AgentConfigID string `json:"agent_config_id"`
			Provider      string `json:"provider"`
			Model         string `json:"model"`
			Label         string `json:"label"`
		} `json:"reference_models"`
		Aggregator struct {
			AgentConfigID string `json:"agent_config_id"`
			Provider      string `json:"provider"`
			Model         string `json:"model"`
			Label         string `json:"label"`
		} `json:"aggregator"`
		ReferenceTimeoutSeconds int     `json:"reference_timeout_seconds"`
		MaxReferenceWorkers     int     `json:"max_reference_workers"`
		ReferenceTemperature    float64 `json:"reference_temperature"`
		AggregatorTemperature   float64 `json:"aggregator_temperature"`
	}
	if err := json.Unmarshal([]byte(created.MixtureConfigJSON), &cfg); err != nil {
		t.Fatalf("unmarshal mixture config: %v", err)
	}
	if created.Model != "research-heavy" {
		t.Fatalf("expected submitted mixture model id to persist, got %q", created.Model)
	}
	if created.Temperature != 0 {
		t.Fatalf("expected unused top-level mixture temperature to be zero, got %v", created.Temperature)
	}
	if cfg.ReferenceTemperature != 0.7 || cfg.AggregatorTemperature != 0.2 {
		t.Fatalf("expected mixture temperatures 0.7/0.2, got %v/%v", cfg.ReferenceTemperature, cfg.AggregatorTemperature)
	}
	if !cfg.Enabled || cfg.Aggregator.AgentConfigID != agg.ID || cfg.Aggregator.Label != "Aggregator" || len(cfg.ReferenceModels) != 1 || cfg.ReferenceModels[0].AgentConfigID != ref.ID || cfg.ReferenceTimeoutSeconds != 45 || cfg.MaxReferenceWorkers != 4 {
		t.Fatalf("unexpected mixture config: %s", created.MixtureConfigJSON)
	}
}

func TestCreateModel_NonMixturePreservesTemperature(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "Temperature Model")
	form.Set("provider", "openai")
	form.Set("openai_auth_type", "api_key")
	form.Set("model", "gpt-5.5")
	form.Set("api_key", "sk-test")
	form.Set("temperature", "0.35")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	configs, err := llmConfigRepo.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for i := range configs {
		if configs[i].Name == "Temperature Model" {
			if configs[i].Temperature != 0.35 {
				t.Fatalf("expected non-mixture temperature 0.35, got %v", configs[i].Temperature)
			}
			return
		}
	}
	t.Fatal("created non-mixture model not found")
}

func TestCreateModel_RejectsBlankNameWithoutInsert(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	before, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("list before create: %v", err)
	}

	form := modelValidationForm(" \t\n ")
	rec := postForm(e, "/models", form)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Model name is required") {
		t.Fatalf("expected controlled blank-name 400, got %d: %s", rec.Code, rec.Body.String())
	}
	after, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("list after create: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("blank create mutated rows: before=%d after=%d", len(before), len(after))
	}
}

func TestCreateModel_RejectsBlankRunnableModelSlugWithoutInsert(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	tests := []struct {
		name       string
		provider   string
		modelValue *string
		customize  func(url.Values)
	}{
		{name: "anthropic missing", provider: "anthropic", modelValue: nil},
		{name: "anthropic empty", provider: "anthropic", modelValue: ptrString("")},
		{name: "anthropic whitespace", provider: "anthropic", modelValue: ptrString(" \t\n ")},
		{name: "openai missing", provider: "openai", modelValue: nil, customize: func(form url.Values) { form.Set("openai_auth_type", "api_key") }},
		{name: "openai empty", provider: "openai", modelValue: ptrString(""), customize: func(form url.Values) { form.Set("openai_auth_type", "api_key") }},
		{name: "openai whitespace", provider: "openai", modelValue: ptrString(" \t\n "), customize: func(form url.Values) { form.Set("openai_auth_type", "api_key") }},
		{name: "openai compatible missing", provider: "openai_compatible", modelValue: nil, customize: configureOpenAICompatibleValidationForm},
		{name: "openai compatible empty", provider: "openai_compatible", modelValue: ptrString(""), customize: configureOpenAICompatibleValidationForm},
		{name: "openai compatible whitespace", provider: "openai_compatible", modelValue: ptrString(" \t\n "), customize: configureOpenAICompatibleValidationForm},
		{name: "ollama missing", provider: "ollama", modelValue: nil},
		{name: "ollama empty", provider: "ollama", modelValue: ptrString("")},
		{name: "ollama whitespace", provider: "ollama", modelValue: ptrString(" \t\n ")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, err := llmConfigRepo.List(ctx)
			if err != nil {
				t.Fatalf("list before create: %v", err)
			}

			form := modelValidationForm("Blank Slug " + tt.name)
			form.Set("provider", tt.provider)
			form.Del("model")
			if tt.modelValue != nil {
				form.Set("model", *tt.modelValue)
			}
			if tt.customize != nil {
				tt.customize(form)
			}
			rec := postForm(e, "/models", form)

			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Model identifier is required") {
				t.Fatalf("expected controlled blank-model 400, got %d: %s", rec.Code, rec.Body.String())
			}
			after, err := llmConfigRepo.List(ctx)
			if err != nil {
				t.Fatalf("list after create: %v", err)
			}
			if len(after) != len(before) {
				t.Fatalf("blank model create mutated rows: before=%d after=%d", len(before), len(after))
			}
		})
	}
}

func TestCreateModel_HTMXRejectsDuplicateNormalizedNameWithoutInsert(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	existing := &models.LLMConfig{Name: "Production", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey, Model: "claude-sonnet-4-5-20250929"}
	if err := llmConfigRepo.Create(ctx, existing); err != nil {
		t.Fatalf("create existing model: %v", err)
	}
	before, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("list before duplicate create: %v", err)
	}

	form := modelValidationForm(" production ")
	rec := htmxPost(e, "/models", form)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "already exists") {
		t.Fatalf("expected controlled duplicate-name 400, got %d: %s", rec.Code, rec.Body.String())
	}
	after, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("list after duplicate create: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("duplicate create mutated rows: before=%d after=%d", len(before), len(after))
	}
}

func TestUpdateModel_PostRejectsBlankNameWithoutMutation(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("expected seeded default model, got %v", err)
	}
	original := *agent

	form := modelValidationForm("   ")
	rec := postForm(e, "/models/"+agent.ID, form)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Model name is required") {
		t.Fatalf("expected controlled blank-name 400, got %d: %s", rec.Code, rec.Body.String())
	}
	updated, err := llmConfigRepo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("get updated model: %v", err)
	}
	if updated.Name != original.Name || updated.Model != original.Model || updated.Provider != original.Provider {
		t.Fatalf("blank update mutated model: before=%+v after=%+v", original, updated)
	}
}

func TestUpdateModel_RejectsBlankRunnableModelSlugWithoutMutation(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	tests := []struct {
		name      string
		agent     *models.LLMConfig
		customize func(url.Values)
	}{
		{name: "anthropic", agent: &models.LLMConfig{Name: "Runnable Anthropic", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey, Model: "claude-sonnet-4-5-20250929"}},
		{name: "openai", agent: &models.LLMConfig{Name: "Runnable OpenAI", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-5.5"}, customize: func(form url.Values) { form.Set("openai_auth_type", "api_key") }},
		{name: "openai compatible", agent: &models.LLMConfig{Name: "Runnable Compatible", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey, Model: "vendor/model", BaseURL: "https://api.example.test/v1/", Transport: "chat_completions", PresetSlug: "custom"}, customize: configureOpenAICompatibleValidationForm},
		{name: "ollama", agent: &models.LLMConfig{Name: "Runnable Ollama", Provider: models.ProviderOllama, AuthMethod: models.AuthMethodAPIKey, Model: "llama3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := *tt.agent
			if err := llmConfigRepo.Create(ctx, &agent); err != nil {
				t.Fatalf("create model: %v", err)
			}
			original := agent

			form := modelValidationForm(agent.Name)
			form.Set("provider", string(agent.Provider))
			form.Set("model", " \t\n ")
			if tt.customize != nil {
				tt.customize(form)
			}
			rec := htmxPut(e, "/models/"+agent.ID, form)

			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Model identifier is required") {
				t.Fatalf("expected controlled blank-model 400, got %d: %s", rec.Code, rec.Body.String())
			}
			updated, err := llmConfigRepo.GetByID(ctx, agent.ID)
			if err != nil {
				t.Fatalf("get updated model: %v", err)
			}
			if updated.Model != original.Model || updated.Provider != original.Provider || updated.Name != original.Name {
				t.Fatalf("blank model update mutated model: before=%+v after=%+v", original, updated)
			}
		})
	}
}

func TestUpdateModel_HTMXRejectsDuplicateNormalizedNameWithoutMutation(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	alpha := &models.LLMConfig{Name: "Alpha", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey, Model: "claude-sonnet-4-5-20250929"}
	beta := &models.LLMConfig{Name: "Beta", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-5.5"}
	for _, cfg := range []*models.LLMConfig{alpha, beta} {
		if err := llmConfigRepo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s model: %v", cfg.Name, err)
		}
	}
	original := *beta

	form := modelValidationForm(" alpha ")
	form.Set("provider", "openai")
	form.Set("openai_auth_type", "api_key")
	form.Set("model", beta.Model)
	rec := htmxPut(e, "/models/"+beta.ID, form)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "already exists") {
		t.Fatalf("expected controlled duplicate-name 400, got %d: %s", rec.Code, rec.Body.String())
	}
	updated, err := llmConfigRepo.GetByID(ctx, beta.ID)
	if err != nil {
		t.Fatalf("get updated model: %v", err)
	}
	if updated.Name != original.Name || updated.Model != original.Model || updated.Provider != original.Provider {
		t.Fatalf("duplicate update mutated model: before=%+v after=%+v", original, updated)
	}
}

func TestUpdateModel_AllowsOwnNormalizedNameAndPersistsTrimmedName(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := &models.LLMConfig{Name: "Stable Name", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey, Model: "claude-sonnet-4-5-20250929"}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create model: %v", err)
	}

	form := modelValidationForm("  Stable Name  ")
	rec := postForm(e, "/models/"+agent.ID, form)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected own normalized name update to succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	updated, err := llmConfigRepo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("get updated model: %v", err)
	}
	if updated.Name != "Stable Name" {
		t.Fatalf("expected trimmed model name to persist, got %q", updated.Name)
	}
}

func modelValidationForm(name string) url.Values {
	form := url.Values{}
	form.Set("name", name)
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "api_key")
	form.Set("model", "claude-sonnet-4-5-20250929")
	form.Set("temperature", "0")
	return form
}

func configureOpenAICompatibleValidationForm(form url.Values) {
	form.Set("base_url", "https://api.example.test/v1/")
	form.Set("transport", "chat_completions")
	form.Set("preset_slug", "custom")
}

func ptrString(value string) *string {
	return &value
}

func TestCreateModel_MixtureDefaultsBlankModel(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agg := &models.LLMConfig{Name: "Aggregator", Provider: models.ProviderTest, Model: "agg"}
	ref := &models.LLMConfig{Name: "Reference", Provider: models.ProviderTest, Model: "ref"}
	for _, cfg := range []*models.LLMConfig{agg, ref} {
		if err := llmConfigRepo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}

	form := url.Values{}
	form.Set("name", "Browser Mixture")
	form.Set("provider", "mixture")
	form.Set("model", "")
	form.Set("mixture_enabled", "on")
	form.Set("mixture_aggregator_id", agg.ID)
	form.Add("mixture_reference_ids", ref.ID)

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	configs, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for i := range configs {
		if configs[i].Name == "Browser Mixture" {
			if configs[i].Model != "default" {
				t.Fatalf("expected blank mixture model to default to virtual id, got %q", configs[i].Model)
			}
			return
		}
	}
	t.Fatal("created mixture not found")
}

func TestCreateModel_MixtureAllowsAggregatorAsReference(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agg := &models.LLMConfig{Name: "Aggregator", Provider: models.ProviderTest, Model: "agg"}
	if err := llmConfigRepo.Create(ctx, agg); err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	form := url.Values{}
	form.Set("name", "Self Review Mixture")
	form.Set("provider", "mixture")
	form.Set("model", "self-review")
	form.Set("mixture_enabled", "on")
	form.Set("mixture_aggregator_id", agg.ID)
	form.Add("mixture_reference_ids", agg.ID)

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	configs, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for i := range configs {
		if configs[i].Name != "Self Review Mixture" {
			continue
		}
		var cfg struct {
			Aggregator struct {
				AgentConfigID string `json:"agent_config_id"`
			} `json:"aggregator"`
			ReferenceModels []struct {
				AgentConfigID string `json:"agent_config_id"`
			} `json:"reference_models"`
		}
		if err := json.Unmarshal([]byte(configs[i].MixtureConfigJSON), &cfg); err != nil {
			t.Fatalf("unmarshal mixture config: %v", err)
		}
		if cfg.Aggregator.AgentConfigID != agg.ID || len(cfg.ReferenceModels) != 1 || cfg.ReferenceModels[0].AgentConfigID != agg.ID {
			t.Fatalf("expected aggregator to also be saved as reference, got %s", configs[i].MixtureConfigJSON)
		}
		return
	}
	t.Fatal("created mixture not found")
}

func TestCreateModel_MixtureCommaSeparatedReferenceOrder(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agg := &models.LLMConfig{Name: "Aggregator", Provider: models.ProviderTest, Model: "agg"}
	refA := &models.LLMConfig{Name: "Reference A", Provider: models.ProviderTest, Model: "ref-a"}
	refB := &models.LLMConfig{Name: "Reference B", Provider: models.ProviderTest, Model: "ref-b"}
	for _, cfg := range []*models.LLMConfig{agg, refA, refB} {
		if err := llmConfigRepo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}

	form := url.Values{}
	form.Set("name", "Ordered Mixture")
	form.Set("provider", "mixture")
	form.Set("model", "ordered")
	form.Set("mixture_enabled", "on")
	form.Set("mixture_aggregator_id", agg.ID)
	form.Set("mixture_reference_ids", refB.ID+","+refA.ID)

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	configs, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var created *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "Ordered Mixture" {
			created = &configs[i]
			break
		}
	}
	if created == nil {
		t.Fatal("created mixture not found")
	}
	var cfg struct {
		ReferenceModels []struct {
			AgentConfigID string `json:"agent_config_id"`
		} `json:"reference_models"`
	}
	if err := json.Unmarshal([]byte(created.MixtureConfigJSON), &cfg); err != nil {
		t.Fatalf("unmarshal mixture config: %v", err)
	}
	if len(cfg.ReferenceModels) != 2 || cfg.ReferenceModels[0].AgentConfigID != refB.ID || cfg.ReferenceModels[1].AgentConfigID != refA.ID {
		t.Fatalf("expected comma-separated fallback reference order [%s %s], got %+v in %s", refB.ID, refA.ID, cfg.ReferenceModels, created.MixtureConfigJSON)
	}
}

func TestUpdateModel_Mixture(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agg := &models.LLMConfig{Name: "Aggregator", Provider: models.ProviderTest, Model: "agg"}
	ref := &models.LLMConfig{Name: "Reference", Provider: models.ProviderTest, Model: "ref"}
	mixture := &models.LLMConfig{Name: "Old Mixture", Provider: models.ProviderMixture, Model: "default", Temperature: 0.8, MixtureConfigJSON: `{"enabled":false,"aggregator":{"agent_config_id":"placeholder"}}`}
	for _, cfg := range []*models.LLMConfig{agg, ref, mixture} {
		if err := llmConfigRepo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}
	form := url.Values{}
	form.Set("name", "Edited Mixture")
	form.Set("provider", "mixture")
	form.Set("model", "edited")
	form.Set("mixture_enabled", "on")
	form.Set("mixture_aggregator_id", agg.ID)
	form.Add("mixture_reference_ids", ref.ID)
	req := httptest.NewRequest(http.MethodPut, "/models/"+mixture.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}
	updated, err := llmConfigRepo.GetByID(ctx, mixture.ID)
	if err != nil {
		t.Fatalf("get updated: %v", err)
	}
	if updated.Name != "Edited Mixture" || !strings.Contains(updated.MixtureConfigJSON, ref.ID) {
		t.Fatalf("mixture not updated: %+v", updated)
	}
	if updated.Temperature != 0 {
		t.Fatalf("expected stale top-level mixture temperature to be cleared, got %v", updated.Temperature)
	}
}

func TestCreateModel_MixtureRejectsMissingAggregator(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	form := url.Values{}
	form.Set("name", "Bad Mixture")
	form.Set("provider", "mixture")
	form.Set("mixture_enabled", "on")
	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "aggregator") {
		t.Fatalf("expected missing aggregator 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateModel_MixtureRejectsRecursiveAndDuplicateSlots(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agg := &models.LLMConfig{Name: "Aggregator", Provider: models.ProviderTest, Model: "agg"}
	ref := &models.LLMConfig{Name: "Reference", Provider: models.ProviderTest, Model: "ref"}
	recursive := &models.LLMConfig{Name: "Recursive", Provider: models.ProviderMixture, Model: "recursive", MixtureConfigJSON: `{"enabled":false,"aggregator":{"agent_config_id":"placeholder"}}`}
	for _, cfg := range []*models.LLMConfig{agg, ref, recursive} {
		if err := llmConfigRepo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}
	cases := []struct {
		name string
		refs []string
		want string
	}{
		{name: "recursive", refs: []string{recursive.ID}, want: "mixture model"},
		{name: "duplicate", refs: []string{ref.ID, ref.ID}, want: "duplicate reference"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{}
			form.Set("name", "Bad Mixture "+tc.name)
			form.Set("provider", "mixture")
			form.Set("mixture_enabled", "on")
			form.Set("mixture_aggregator_id", agg.ID)
			for _, id := range tc.refs {
				form.Add("mixture_reference_ids", id)
			}
			req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("expected %q 400, got %d: %s", tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestCreateModel_MixtureRejectsNonCallableSlots(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	callable := &models.LLMConfig{Name: "Callable API", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-5"}
	cliOpenAI := &models.LLMConfig{Name: "Codex CLI", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodCLI, Model: "gpt-5-codex"}
	cliAnthropic := &models.LLMConfig{Name: "Claude CLI", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodCLI, Model: "claude-sonnet"}
	for _, cfg := range []*models.LLMConfig{callable, cliOpenAI, cliAnthropic} {
		if err := llmConfigRepo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}

	cases := []struct {
		name         string
		aggregatorID string
		refs         []string
		want         string
	}{
		{name: "cli aggregator", aggregatorID: cliOpenAI.ID, refs: []string{callable.ID}, want: "Codex CLI"},
		{name: "cli reference", aggregatorID: callable.ID, refs: []string{cliAnthropic.ID}, want: "Claude CLI"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{}
			form.Set("name", "Bad Mixture "+tc.name)
			form.Set("provider", "mixture")
			form.Set("mixture_enabled", "on")
			form.Set("mixture_aggregator_id", tc.aggregatorID)
			for _, id := range tc.refs {
				form.Add("mixture_reference_ids", id)
			}
			req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("expected %q 400, got %d: %s", tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestDeleteModel_UsedByMixtureBlockedWithNames(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agg := &models.LLMConfig{Name: "Aggregator", Provider: models.ProviderTest, Model: "agg"}
	ref := &models.LLMConfig{Name: "Reference", Provider: models.ProviderTest, Model: "ref"}
	for _, cfg := range []*models.LLMConfig{agg, ref} {
		if err := llmConfigRepo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}
	mixture := &models.LLMConfig{Name: "Research Mixture", Provider: models.ProviderMixture, Model: "default", MixtureConfigJSON: `{"enabled":true,"reference_models":[{"agent_config_id":"` + ref.ID + `"}],"aggregator":{"agent_config_id":"` + agg.ID + `"}}`}
	if err := llmConfigRepo.Create(ctx, mixture); err != nil {
		t.Fatalf("create mixture: %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/models/"+ref.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Research Mixture") || !strings.Contains(rec.Body.String(), "Remove it from those mixtures") {
		t.Fatalf("expected mixture delete block, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateModel_OpenAICompatible(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "OpenRouter Nemotron")
	form.Set("provider", "openai_compatible_openrouter")
	form.Set("model", "nvidia/nemotron-3-ultra-550b-a55b:free")
	form.Set("api_key", "sk-or-test")
	form.Set("base_url", "https://openrouter.ai/api/v1/")
	form.Set("transport", "chat_completions")
	form.Set("preset_slug", "openrouter")
	form.Set("default_max_tokens", "16000")
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
		if configs[i].Name == "OpenRouter Nemotron" {
			found = &configs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created model not found")
	}
	if found.Provider != models.ProviderOpenAICompatible || found.AuthMethod != models.AuthMethodAPIKey {
		t.Fatalf("provider/auth = %s/%s", found.Provider, found.AuthMethod)
	}
	if found.Model != "nvidia/nemotron-3-ultra-550b-a55b:free" {
		t.Fatalf("model = %q", found.Model)
	}
	if found.BaseURL != "https://openrouter.ai/api/v1/" || found.Transport != "chat_completions" || found.PresetSlug != "openrouter" || found.DefaultMaxTokens != 16000 {
		t.Fatalf("compatible fields not saved: %+v", found)
	}
}

func TestCreateModel_CustomOpenAICompatibleOAuthPersistsAdvancedAuthentication(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "Signed Custom Model")
	form.Set("provider", "openai_compatible")
	form.Set("custom_auth_method", "oauth")
	form.Set("model", "premium")
	form.Set("base_url", "https://api.example.test/inference/v1")
	form.Set("models_url", "https://api.example.test/inference/v1/model/info")
	form.Set("transport", "chat_completions")
	form.Set("preset_slug", "custom")
	form.Set("oauth_authorize_url", "https://login.example.test/login")
	form.Set("oauth_token_url", "https://api.example.test/auth/token")
	form.Set("oauth_client_id", "client-id")
	form.Set("oauth_client_secret", "client-secret")
	form.Set("oauth_scopes", "openid profile")
	form.Set("custom_refresh_url", "https://api.example.test/auth/refresh")
	form.Set("custom_profile_url", "https://api.example.test/admin/profile")
	form.Set("custom_user_agent", "custom-client/1.0")
	form.Set("custom_signing_secret", "secret")
	form.Set("custom_authorization_mode", "auto")
	form.Set("custom_access_token_header", "X-Auth-Token")
	form.Set("custom_access_token_prefix", "Token ")
	form.Set("custom_profile_instance_path", "instances.0.instance_id")
	form.Set("custom_profile_team_path", "instances.0.teams.0.id")
	form.Set("custom_models_array_path", "data")
	form.Set("custom_model_id_field", "model_name")
	form.Set("custom_access_token_field", "token")
	form.Set("custom_refresh_token_field", "refresh_token")
	form.Set("custom_token_request_format", "json")
	form.Set("custom_callback_parameter", "redirect_uri")
	form.Set("custom_local_callback_host", "127.0.0.1")
	form.Set("custom_local_callback_path", "/provider-callback")
	form.Set("custom_standard_token_fields", "on")
	form.Set("custom_oauth_pkce", "on")
	form.Set("custom_static_headers_json", `{"X-Required-Header":"required-value"}`)
	form.Set("custom_authorization_parameters_json", `{"audience":"custom-api"}`)

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}

	configs, err := llmConfigRepo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "Signed Custom Model" {
			found = &configs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created model not found")
	}
	if found.AuthMethod != models.AuthMethodOAuth || found.ModelsURL != "https://api.example.test/inference/v1/model/info" {
		t.Fatalf("unexpected saved config: %#v", found)
	}
	cfg, err := llmcustomauth.ParseConfig(found.CustomAuthConfigJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.UserAgent != "custom-client/1.0" || cfg.SigningSecret != "secret" || cfg.ModelIDField != "model_name" ||
		cfg.AccessTokenHeader != "X-Auth-Token" || cfg.AccessTokenPrefix != "Token " {
		t.Fatalf("unexpected custom auth config: %#v", cfg)
	}
	if !cfg.PKCE || !cfg.StandardTokenFields || cfg.CallbackParameter != "redirect_uri" ||
		cfg.LocalCallbackHost != "127.0.0.1" || cfg.LocalCallbackPath != "/provider-callback" ||
		cfg.StaticHeaders["X-Required-Header"] != "required-value" || cfg.AuthorizationParameters["audience"] != "custom-api" {
		t.Fatalf("generic OAuth fields not saved: %#v", cfg)
	}
	if found.OAuthClientID != "client-id" || found.OAuthClientSecret != "client-secret" || found.OAuthScopes != "openid profile" {
		t.Fatalf("OAuth client fields not saved: %#v", found)
	}

	editForm := url.Values{}
	editForm.Set("base_url", found.BaseURL)
	editForm.Set("models_url", found.ModelsURL)
	editForm.Set("transport", found.Transport)
	editForm.Set("preset_slug", found.PresetSlug)
	editForm.Set("custom_user_agent", cfg.UserAgent)
	editReq := httptest.NewRequest(http.MethodPost, "/models/"+found.ID, strings.NewReader(editForm.Encode()))
	editReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	editCtx := e.NewContext(editReq, httptest.NewRecorder())
	edited := *found
	if err := applyOpenAICompatibleForm(editCtx, &edited); err != nil {
		t.Fatal(err)
	}
	editedCfg, err := llmcustomauth.ParseConfig(edited.CustomAuthConfigJSON)
	if err != nil {
		t.Fatal(err)
	}
	if editedCfg.StaticHeaders["X-Required-Header"] != "required-value" {
		t.Fatalf("blank edit erased saved static headers: %#v", editedCfg.StaticHeaders)
	}
	clearForm := url.Values{}
	clearForm.Set("base_url", found.BaseURL)
	clearForm.Set("models_url", found.ModelsURL)
	clearForm.Set("transport", found.Transport)
	clearForm.Set("preset_slug", found.PresetSlug)
	clearForm.Set("custom_clear_signing_secret", "on")
	clearReq := httptest.NewRequest(http.MethodPost, "/models/"+found.ID, strings.NewReader(clearForm.Encode()))
	clearReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	clearCtx := e.NewContext(clearReq, httptest.NewRecorder())
	if err := applyOpenAICompatibleForm(clearCtx, &edited); err != nil {
		t.Fatal(err)
	}
	clearedCfg, err := llmcustomauth.ParseConfig(edited.CustomAuthConfigJSON)
	if err != nil {
		t.Fatal(err)
	}
	if clearedCfg.SigningSecret != "" {
		t.Fatal("explicit clear retained the signing secret")
	}
}

func TestCreateModel_CustomOAuthRejectsInvalidHeaderConfiguration(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	form := url.Values{
		"name":                       {"Invalid custom header"},
		"provider":                   {"openai_compatible"},
		"custom_auth_method":         {"oauth"},
		"model":                      {"premium"},
		"base_url":                   {"https://api.example.test/v1"},
		"preset_slug":                {"custom"},
		"transport":                  {"chat_completions"},
		"oauth_authorize_url":        {"https://login.example.test/authorize"},
		"oauth_token_url":            {"https://login.example.test/token"},
		"custom_access_token_header": {"Bad Header"},
	}
	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "valid HTTP header name") {
		t.Fatalf("unexpected response: %s", rec.Body.String())
	}
}

func TestCreateModel_CustomCompatibleRejectsInvalidRequestExtras(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value string
	}{
		{name: "auth header name", field: "auth_header_name", value: "Bad Header"},
		{name: "auth header value", field: "auth_header_value_prefix", value: "Bearer\r\nX-Injected: true"},
		{name: "additional headers JSON", field: "extra_headers_json", value: `{"X-Required-Header":`},
		{name: "additional header value", field: "extra_headers_json", value: `{"X-Required-Header":"required\u000aX-Injected: true"}`},
		{name: "additional body JSON", field: "extra_body_json", value: `[{"metadata":"not an object"}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, e, _ := setupTestHandler(t)
			form := url.Values{
				"name":        {"Invalid compatible request extras"},
				"provider":    {"openai_compatible"},
				"model":       {"model"},
				"api_key":     {"secret"},
				"base_url":    {"https://api.example.test/v1"},
				"preset_slug": {"custom"},
				"transport":   {"chat_completions"},
				"temperature": {"0"},
			}
			form.Set(tt.field, tt.value)
			req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestApplyOpenAICompatibleFormPreservesAndClearsRequestExtras(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	agent := models.LLMConfig{
		Provider:         models.ProviderOpenAICompatible,
		AuthMethod:       models.AuthMethodAPIKey,
		APIKey:           "secret",
		ExtraHeadersJSON: `{"X-Required-Header":"required"}`,
		ExtraBodyJSON:    `{"provider_option":true}`,
	}
	form := url.Values{
		"base_url":                 {"https://api.example.test/v1"},
		"preset_slug":              {"custom"},
		"transport":                {"chat_completions"},
		"auth_header_name":         {"X-API-Key"},
		"auth_header_value_prefix": {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := applyOpenAICompatibleForm(e.NewContext(req, httptest.NewRecorder()), &agent); err != nil {
		t.Fatal(err)
	}
	if agent.ExtraHeadersJSON != `{"X-Required-Header":"required"}` || agent.ExtraBodyJSON != `{"provider_option":true}` {
		t.Fatalf("blank edit erased saved request extras: headers=%q body=%q", agent.ExtraHeadersJSON, agent.ExtraBodyJSON)
	}
	if got := agent.GetAuthHeaderValuePrefix(); got != "" {
		t.Fatalf("raw API-key prefix = %q, want empty", got)
	}

	form.Set("clear_extra_headers", "on")
	form.Set("clear_extra_body", "on")
	req = httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := applyOpenAICompatibleForm(e.NewContext(req, httptest.NewRecorder()), &agent); err != nil {
		t.Fatal(err)
	}
	if agent.ExtraHeadersJSON != "" || agent.ExtraBodyJSON != "" {
		t.Fatalf("explicit clear retained request extras: headers=%q body=%q", agent.ExtraHeadersJSON, agent.ExtraBodyJSON)
	}

	agent.ExtraHeadersJSON = `{"X-Required-Header":"required"}`
	agent.ExtraBodyJSON = `{"provider_option":true}`
	form.Set("preset_slug", "openrouter")
	form.Set("extra_headers_json", `{"X-Required-Header":"required"}`)
	form.Set("extra_body_json", `{"provider_option":true}`)
	req = httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := applyOpenAICompatibleForm(e.NewContext(req, httptest.NewRecorder()), &agent); err != nil {
		t.Fatal(err)
	}
	if agent.AuthHeaderName != "" || agent.AuthHeaderValuePrefix != "" ||
		agent.ExtraHeadersJSON != "" || agent.ExtraBodyJSON != "" {
		t.Fatalf("preset switch retained custom request settings: %#v", agent)
	}
}

func TestUpdateCustomOAuthSecurityEndpointInvalidatesStoredCredentials(t *testing.T) {
	_, e, repo := setupTestHandler(t)
	ctx := context.Background()
	cfg := llmcustomauth.Config{
		Enabled:              true,
		RefreshURL:           "https://api.example.test/auth/refresh",
		ProfileURL:           "https://api.example.test/profile",
		AccessTokenField:     "token",
		RefreshTokenField:    "refresh_token",
		AuthorizationMode:    "auto",
		ModelsArrayPath:      "data",
		ModelIDField:         "model_name",
		CallbackParameter:    "callback_uri",
		TokenRequestFormat:   "json",
		RefreshRequestFormat: "json",
	}
	agent := &models.LLMConfig{
		Name:                 "Connected custom model",
		Provider:             models.ProviderOpenAICompatible,
		AuthMethod:           models.AuthMethodOAuth,
		Model:                "premium",
		BaseURL:              "https://api.example.test/inference/v1",
		ModelsURL:            "https://api.example.test/inference/v1/model/info",
		OAuthAuthorizeURL:    "https://login.example.test/login",
		OAuthTokenURL:        "https://api.example.test/auth/token",
		OAuthAccessToken:     "connected-access",
		OAuthRefreshToken:    "connected-refresh",
		OAuthExpiresAt:       time.Now().Add(time.Hour).UnixMilli(),
		CustomAuthConfigJSON: llmcustomauth.MarshalConfig(cfg),
		CustomAuthStateJSON:  `{"instance_id":"instance-1"}`,
	}
	if err := repo.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"name":                          {agent.Name},
		"provider":                      {"openai_compatible"},
		"custom_auth_method":            {"oauth"},
		"model":                         {agent.Model},
		"base_url":                      {agent.BaseURL},
		"models_url":                    {"https://new-api.example.test/models"},
		"preset_slug":                   {"custom"},
		"transport":                     {"chat_completions"},
		"oauth_authorize_url":           {agent.OAuthAuthorizeURL},
		"oauth_token_url":               {agent.OAuthTokenURL},
		"custom_refresh_url":            {cfg.RefreshURL},
		"custom_profile_url":            {cfg.ProfileURL},
		"custom_access_token_field":     {"token"},
		"custom_refresh_token_field":    {"refresh_token"},
		"custom_token_request_format":   {"json"},
		"custom_refresh_request_format": {"json"},
		"custom_authorization_mode":     {"auto"},
		"custom_models_array_path":      {"data"},
		"custom_model_id_field":         {"model_name"},
		"custom_callback_parameter":     {"callback_uri"},
	}
	req := httptest.NewRequest(http.MethodPut, "/models/"+agent.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}
	updated, err := repo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ModelsURL != "https://new-api.example.test/models" {
		t.Fatalf("models URL not updated: %q", updated.ModelsURL)
	}
	if updated.OAuthAccessToken != "" || updated.OAuthRefreshToken != "" || updated.CustomAuthStateJSON != "" {
		t.Fatalf("security endpoint change retained OAuth credentials: %#v", updated)
	}
}

func TestCreateModel_OpenAICompatibleNewPresetPersistsExactFields(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "Groq Llama")
	form.Set("provider", "openai_compatible_groq")
	form.Set("model", " llama-3.3-70b-versatile ")
	form.Set("api_key", "gsk-test")
	form.Set("base_url", "https://api.groq.com/openai/v1/")
	form.Set("transport", "chat_completions")
	form.Set("preset_slug", "groq")
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
	for i := range configs {
		if configs[i].Name == "Groq Llama" {
			if configs[i].Provider != models.ProviderOpenAICompatible || configs[i].AuthMethod != models.AuthMethodAPIKey {
				t.Fatalf("provider/auth = %s/%s", configs[i].Provider, configs[i].AuthMethod)
			}
			if configs[i].Model != "llama-3.3-70b-versatile" {
				t.Fatalf("model = %q", configs[i].Model)
			}
			if configs[i].BaseURL != "https://api.groq.com/openai/v1/" || configs[i].Transport != "chat_completions" || configs[i].PresetSlug != "groq" {
				t.Fatalf("compatible fields not saved: %+v", configs[i])
			}
			return
		}
	}
	t.Fatal("created model not found")
}

func TestCreateModel_OpenAICompatibleRejectsInvalidBaseURL(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "Bad Compatible")
	form.Set("provider", "openai_compatible")
	form.Set("model", "model")
	form.Set("api_key", "sk-test")
	form.Set("base_url", "ftp://example.com/v1")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateModel_OpenAICompatibleRejectsPublicHTTPBaseURL(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "Public HTTP Compatible")
	form.Set("provider", "openai_compatible")
	form.Set("model", "model")
	form.Set("api_key", "sk-test")
	form.Set("base_url", "http://example.com/v1")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateModel_OpenAICompatibleRequiresOptInForPrivateModelsURL(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	_, e, _ := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "Private Models API")
	form.Set("provider", "openai_compatible")
	form.Set("model", "model")
	form.Set("base_url", "https://api.example.test/v1")
	form.Set("models_url", "http://127.0.0.1:8000/models")
	form.Set("preset_slug", "custom")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without per-model private endpoint opt-in, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateModel_OpenAICompatibleAllowsLocalHTTPBaseURL(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "Local Compatible")
	form.Set("provider", "openai_compatible")
	form.Set("model", "local-model")
	form.Set("base_url", "http://127.0.0.1:8000/v1")
	form.Set("preset_slug", "vllm")
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
	for i := range configs {
		if configs[i].Name == "Local Compatible" {
			if configs[i].BaseURL != "http://127.0.0.1:8000/v1" {
				t.Fatalf("base_url = %q", configs[i].BaseURL)
			}
			return
		}
	}
	t.Fatal("created model not found")
}

func TestCreateModel_OpenAICompatibleRejectsLocalURLWithoutServerPolicy(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "")
	_, e, _ := setupTestHandler(t)
	form := url.Values{
		"name":        {"Blocked Local Compatible"},
		"provider":    {"openai_compatible"},
		"model":       {"local-model"},
		"base_url":    {"http://127.0.0.1:8000/v1"},
		"preset_slug": {"vllm"},
	}
	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
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

func TestModelMutations_HTMXReturnRefreshedList(t *testing.T) {
	db, statementCounter := testutil.NewStatementCountingTestDB(t)
	_, e, llmConfigRepo := setupTestHandlerForDB(t, db)
	ctx := context.Background()
	largePayload := strings.Repeat("large-edit-only-json", 4096)
	largeCustom := &models.LLMConfig{
		Name: "Large Custom Provider", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey,
		Model: "custom-model", APIKey: "secret-api-key", BaseURL: "https://example.com/v1/",
		Transport: "chat_completions", PresetSlug: "custom", ExtraHeadersJSON: `{"X-Secret":"value"}`,
		ExtraBodyJSON: largePayload, CustomAuthConfigJSON: `{"signing_secret":"secret"}`,
		CustomAuthStateJSON: `{"access":"secret"}`, MixtureConfigJSON: `{"large":"` + largePayload + `"}`,
	}
	if err := llmConfigRepo.Create(ctx, largeCustom); err != nil {
		t.Fatalf("create large custom provider: %v", err)
	}

	createForm := url.Values{
		"name":                {"Mutation Created Model"},
		"provider":            {"anthropic"},
		"anthropic_auth_type": {"api_key"},
		"model":               {"claude-sonnet-4-5-20250929"},
		"temperature":         {"0"},
		"model_max_workers":   {"2"},
	}
	statementCounter.Reset()
	statementCounter.SetEnabled(true)
	createReq := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(createForm.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createReq.Header.Set("HX-Request", "true")
	createRec := httptest.NewRecorder()
	e.ServeHTTP(createRec, createReq)
	statementCounter.SetEnabled(false)
	assertRefreshedModelsResponse(t, createRec, "Mutation Created Model", "Large Custom Provider", "0 / 2 active")
	assertCompactModelsRefreshQuery(t, statementCounter.Statements())
	assertNotContains(t, createRec, largePayload)
	assertNotContains(t, createRec, "secret-api-key")

	configs, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("list models after create: %v", err)
	}
	var created *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "Mutation Created Model" {
			created = &configs[i]
			break
		}
	}
	if created == nil {
		t.Fatal("created model not found")
	}

	updateForm := url.Values{
		"name":                {"Mutation Updated Model"},
		"provider":            {"anthropic"},
		"anthropic_auth_type": {"api_key"},
		"model":               {"claude-sonnet-4-5-20250929"},
		"temperature":         {"0"},
		"model_max_workers":   {"3"},
	}
	statementCounter.Reset()
	statementCounter.SetEnabled(true)
	updateRec := htmxPut(e, "/models/"+created.ID, updateForm)
	statementCounter.SetEnabled(false)
	assertRefreshedModelsResponse(t, updateRec, "Mutation Updated Model", "Large Custom Provider", "0 / 3 active")
	assertCompactModelsRefreshQuery(t, statementCounter.Statements())
	assertNotContains(t, updateRec, largePayload)

	statementCounter.Reset()
	statementCounter.SetEnabled(true)
	defaultRec := htmxPost(e, "/models/"+created.ID+"/set-default", nil)
	statementCounter.SetEnabled(false)
	assertRefreshedModelsResponse(t, defaultRec, "Mutation Updated Model", "Large Custom Provider", "data-model-is-default=\"true\"")
	assertCompactModelsRefreshQuery(t, statementCounter.Statements())
	assertNotContains(t, defaultRec, largePayload)

	statementCounter.Reset()
	statementCounter.SetEnabled(true)
	deleteRec := htmxDelete(e, "/models/"+created.ID)
	statementCounter.SetEnabled(false)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete HTMX response code = %d, want %d: %s", deleteRec.Code, http.StatusOK, deleteRec.Body.String())
	}
	if body := deleteRec.Body.String(); !strings.Contains(body, "models-container") || strings.Contains(body, "Mutation Updated Model") {
		t.Errorf("delete HTMX response did not contain the refreshed model list: %s", body)
	}
	assertCompactModelsRefreshQuery(t, statementCounter.Statements())
	assertNotContains(t, deleteRec, largePayload)

	details := htmxGet(e, "/models/"+largeCustom.ID+"/edit-details")
	assertCode(t, details, http.StatusOK)
	var payload modelEditDetails
	if err := json.Unmarshal(details.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ExtraBodyJSON != largePayload || payload.ExtraHeadersJSON != largeCustom.ExtraHeadersJSON {
		t.Fatalf("edit details lost full custom provider fields: %#v", payload)
	}
}

func assertRefreshedModelsResponse(t *testing.T, rec *httptest.ResponseRecorder, expected ...string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("HTMX response code = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "models-container") {
		t.Fatalf("HTMX response missing models-container: %s", body)
	}
	for _, value := range expected {
		if !strings.Contains(body, value) {
			t.Errorf("HTMX response missing %q: %s", value, body)
		}
	}
}

func assertCompactModelsRefreshQuery(t *testing.T, statements []string) {
	t.Helper()
	var refreshStatements []string
	for _, statement := range statements {
		normalized := strings.Join(strings.Fields(statement), " ")
		if strings.Contains(normalized, "FROM agent_configs WHERE 1=1 ORDER BY is_default DESC, name ASC") {
			refreshStatements = append(refreshStatements, normalized)
		}
	}
	if len(refreshStatements) != 1 {
		t.Fatalf("expected exactly one model-card refresh query, got %d in statements: %q", len(refreshStatements), statements)
	}
	refresh := refreshStatements[0]
	for _, forbidden := range []string{"oauth_refresh_token", "oauth_client_secret", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json"} {
		if strings.Contains(refresh, forbidden) {
			t.Fatalf("model refresh query selected edit-only column %q: %s", forbidden, refresh)
		}
	}
	for _, required := range []string{"CASE WHEN api_key != '' THEN 1 ELSE 0 END", "json_array_length"} {
		if !strings.Contains(refresh, required) {
			t.Fatalf("model refresh query does not look like compact card projection; missing %q in %s", required, refresh)
		}
	}
}

func TestNormalizeBrowserModelFormCommonWorkerAndCheckboxSettings(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	createForm := url.Values{
		"name":                {"Worker Create"},
		"provider":            {"anthropic"},
		"anthropic_auth_type": {"api_key"},
		"model":               {"claude-sonnet-4-5-20250929"},
		"temperature":         {"0.2"},
		"model_max_workers":   {"99"},
		"worker_timeout":      {"-5"},
		"is_default":          {"on"},
		"auto_start_tasks":    {"on"},
	}
	createReq := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(createForm.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	created := &models.LLMConfig{}
	if err := h.normalizeBrowserModelForm(context.Background(), e.NewContext(createReq, httptest.NewRecorder()), created, modelFormOptions{mode: modelFormCreate}); err != nil {
		t.Fatal(err)
	}
	if created.MaxWorkers != 10 || created.WorkerTimeout != 0 || !created.IsDefault || !created.AutoStartTasks || created.Temperature != 0.2 {
		t.Fatalf("create normalization mismatch: %#v", created)
	}

	updateForm := url.Values{
		"name":                {"Worker Update"},
		"provider":            {"anthropic"},
		"anthropic_auth_type": {"api_key"},
		"model":               {"claude-sonnet-4-5-20250929"},
		"temperature":         {"not-a-number"},
		"model_max_workers":   {"not-a-number"},
		"worker_timeout":      {"25"},
	}
	updateReq := httptest.NewRequest(http.MethodPost, "/models/id", strings.NewReader(updateForm.Encode()))
	updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updated := &models.LLMConfig{Temperature: 0.7, MaxWorkers: 4, WorkerTimeout: 12, IsDefault: true, AutoStartTasks: true}
	if err := h.normalizeBrowserModelForm(context.Background(), e.NewContext(updateReq, httptest.NewRecorder()), updated, modelFormOptions{mode: modelFormUpdate}); err != nil {
		t.Fatal(err)
	}
	if updated.Temperature != 0.7 || updated.MaxWorkers != 4 || updated.WorkerTimeout != 25 || updated.IsDefault || updated.AutoStartTasks {
		t.Fatalf("update normalization mismatch: %#v", updated)
	}
}

func TestCreateModel_SubscriptionLegacyCLINormalizesOAuth(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "My Legacy CLI Model")
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
		if configs[i].Name == "My Legacy CLI Model" {
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

func TestCreateModel_WithExistingModelConfigIDUpdatesAnthropicOAuthInPlace(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:              "Claude Opus 4.8",
		Provider:          models.ProviderAnthropic,
		Model:             "claude-opus-4-8",
		ReasoningEffort:   "low",
		AuthMethod:        models.AuthMethodOAuth,
		Temperature:       0,
		OAuthAccessToken:  "expired-access-token",
		OAuthRefreshToken: "refresh-token",
		OAuthExpiresAt:    time.Now().Add(-time.Hour).UnixMilli(),
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create error: %v", err)
	}

	before, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("list before: %v", err)
	}

	form := url.Values{}
	form.Set("model_config_id", agent.ID)
	form.Set("name", "Claude Opus 4.8")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "oauth")
	form.Set("auth_method", "oauth")
	form.Set("model", "claude-opus-4-8")
	form.Set("reasoning_effort", "high")
	form.Set("temperature", "0")

	// Simulate the duplicated-card failure mode: the reusable edit form submits to
	// the create route while carrying the existing config ID.
	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	after, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("expected edit to update existing model count %d, got %d", len(before), len(after))
	}

	updated, err := llmConfigRepo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("get updated: %v", err)
	}
	if updated == nil {
		t.Fatal("updated model not found")
	}
	if updated.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort = %q, want high", updated.ReasoningEffort)
	}
	if updated.OAuthAccessToken != "expired-access-token" || updated.OAuthRefreshToken != "refresh-token" || updated.OAuthExpiresAt != agent.OAuthExpiresAt {
		t.Fatalf("expected OAuth token state preserved, got access=%q refresh=%q expires=%d", updated.OAuthAccessToken, updated.OAuthRefreshToken, updated.OAuthExpiresAt)
	}
	if updated.AuthMethod != models.AuthMethodOAuth || updated.Provider != models.ProviderAnthropic {
		t.Fatalf("provider/auth = %s/%s, want anthropic/oauth", updated.Provider, updated.AuthMethod)
	}
}

func TestUpdateModel_SwitchAnthropicOAuthToOpenAIOAuthClearsStaleOAuthState(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:              "Claude OAuth",
		Provider:          models.ProviderAnthropic,
		Model:             "claude-opus-4-8",
		AuthMethod:        models.AuthMethodOAuth,
		OAuthAccessToken:  "anthropic-access-token",
		OAuthRefreshToken: "anthropic-refresh-token",
		OAuthExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
		OAuthAccountID:    "anthropic-account",
		OAuthClientID:     "stale-client-id",
		OAuthClientSecret: "stale-client-secret",
		OAuthAuthorizeURL: "https://stale.example/authorize",
		OAuthTokenURL:     "https://stale.example/token",
		OAuthScopes:       "stale-scope",
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create error: %v", err)
	}
	before, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("list before: %v", err)
	}

	form := url.Values{}
	form.Set("name", "OpenAI OAuth")
	form.Set("provider", "openai")
	form.Set("openai_auth_type", "subscription")
	form.Set("auth_method", "oauth")
	form.Set("model", "gpt-5.3-codex")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPut, "/models/"+agent.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	after, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("expected provider switch to update existing model count %d, got %d", len(before), len(after))
	}
	updated, err := llmConfigRepo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("get error: %v", err)
	}
	if updated.Provider != models.ProviderOpenAI || updated.AuthMethod != models.AuthMethodOAuth {
		t.Fatalf("provider/auth = %s/%s, want openai/oauth", updated.Provider, updated.AuthMethod)
	}
	if updated.OAuthAccessToken != "" || updated.OAuthRefreshToken != "" || updated.OAuthExpiresAt != 0 || updated.OAuthAccountID != "" {
		t.Fatalf("expected OAuth tokens/account cleared on provider switch, got access=%q refresh=%q expires=%d account=%q", updated.OAuthAccessToken, updated.OAuthRefreshToken, updated.OAuthExpiresAt, updated.OAuthAccountID)
	}
	if updated.OAuthClientID != "" || updated.OAuthClientSecret != "" || updated.OAuthAuthorizeURL != "" || updated.OAuthTokenURL != "" || updated.OAuthScopes != "" {
		t.Fatalf("expected stale OAuth client fields cleared on provider switch, got client_id=%q authorize=%q token=%q scopes=%q", updated.OAuthClientID, updated.OAuthAuthorizeURL, updated.OAuthTokenURL, updated.OAuthScopes)
	}
}

func TestUpdateModel_SwitchOpenAIOAuthToAnthropicOAuthClearsStaleOAuthState(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:              "OpenAI OAuth",
		Provider:          models.ProviderOpenAI,
		Model:             "gpt-5.3-codex",
		AuthMethod:        models.AuthMethodOAuth,
		OAuthAccessToken:  "openai-access-token",
		OAuthRefreshToken: "openai-refresh-token",
		OAuthExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
		OAuthAccountID:    "openai-account",
		OAuthClientID:     "openai-client-id",
		OAuthClientSecret: "openai-client-secret",
		OAuthAuthorizeURL: "https://openai.example/authorize",
		OAuthTokenURL:     "https://openai.example/token",
		OAuthScopes:       "openid profile",
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create error: %v", err)
	}
	before, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("list before: %v", err)
	}

	form := url.Values{}
	form.Set("name", "Claude OAuth")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "subscription")
	form.Set("auth_method", "oauth")
	form.Set("model", "claude-opus-4-8")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPut, "/models/"+agent.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	after, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("expected provider switch to update existing model count %d, got %d", len(before), len(after))
	}
	updated, err := llmConfigRepo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("get error: %v", err)
	}
	if updated.Provider != models.ProviderAnthropic || updated.AuthMethod != models.AuthMethodOAuth {
		t.Fatalf("provider/auth = %s/%s, want anthropic/oauth", updated.Provider, updated.AuthMethod)
	}
	if updated.OAuthAccessToken != "" || updated.OAuthRefreshToken != "" || updated.OAuthExpiresAt != 0 || updated.OAuthAccountID != "" {
		t.Fatalf("expected OAuth tokens/account cleared on provider switch, got access=%q refresh=%q expires=%d account=%q", updated.OAuthAccessToken, updated.OAuthRefreshToken, updated.OAuthExpiresAt, updated.OAuthAccountID)
	}
	if updated.OAuthClientID != "" || updated.OAuthClientSecret != "" || updated.OAuthAuthorizeURL != "" || updated.OAuthTokenURL != "" || updated.OAuthScopes != "" {
		t.Fatalf("expected OpenAI OAuth client fields cleared on Anthropic switch, got client_id=%q authorize=%q token=%q scopes=%q", updated.OAuthClientID, updated.OAuthAuthorizeURL, updated.OAuthTokenURL, updated.OAuthScopes)
	}
}

func TestUpdateModel_SwitchToOpenAICompatibleBlankAPIKeyClearsStaleCredential(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:       "OpenAI API Key",
		Provider:   models.ProviderOpenAI,
		AuthMethod: models.AuthMethodAPIKey,
		Model:      "gpt-5.4",
		APIKey:     "sk-openai-old",
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create error: %v", err)
	}

	form := url.Values{}
	form.Set("name", "Local Compatible")
	form.Set("provider", "openai_compatible")
	form.Set("model", "local-model")
	form.Set("api_key", "")
	form.Set("base_url", "http://127.0.0.1:8000/v1")
	form.Set("preset_slug", "vllm")
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
	if updated.Provider != models.ProviderOpenAICompatible || updated.AuthMethod != models.AuthMethodAPIKey {
		t.Fatalf("provider/auth = %s/%s", updated.Provider, updated.AuthMethod)
	}
	if updated.APIKey != "" {
		t.Fatalf("expected stale API key cleared on provider switch, got %q", updated.APIKey)
	}
	if updated.BaseURL != "http://127.0.0.1:8000/v1" {
		t.Fatalf("base_url = %q", updated.BaseURL)
	}
}

func TestUpdateModel_SwitchCustomOAuthToAPIKeyClearsOAuthSecrets(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	cfg := llmcustomauth.Config{
		Enabled:           true,
		SigningSecret:     "signing-secret",
		StaticHeaders:     map[string]string{"X-Required-Header": "required"},
		TokenHeaders:      map[string]string{"X-Token-Secret": "token-secret"},
		RefreshHeaders:    map[string]string{"X-Refresh-Secret": "refresh-secret"},
		RefreshParameters: map[string]string{"refresh_secret": "secret"},
	}
	agent := &models.LLMConfig{
		Name:                 "Custom OAuth",
		Provider:             models.ProviderOpenAICompatible,
		AuthMethod:           models.AuthMethodOAuth,
		Model:                "model",
		BaseURL:              "https://api.example.test/v1",
		APIKey:               "",
		OAuthAccessToken:     "access-token",
		OAuthRefreshToken:    "refresh-token",
		OAuthClientID:        "client-id",
		OAuthClientSecret:    "client-secret",
		OAuthAuthorizeURL:    "https://login.example.test/authorize",
		OAuthTokenURL:        "https://login.example.test/token",
		CustomAuthConfigJSON: llmcustomauth.MarshalConfig(cfg),
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"name":               {"Custom API key"},
		"provider":           {"openai_compatible"},
		"custom_auth_method": {"api_key"},
		"model":              {"model"},
		"api_key":            {"new-api-key"},
		"base_url":           {"https://api.example.test/v1"},
		"preset_slug":        {"custom"},
		"transport":          {"chat_completions"},
		"temperature":        {"0"},
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
		t.Fatal(err)
	}
	if updated.AuthMethod != models.AuthMethodAPIKey || updated.APIKey != "new-api-key" {
		t.Fatalf("unexpected updated credentials: auth=%s api_key=%q", updated.AuthMethod, updated.APIKey)
	}
	if updated.OAuthAccessToken != "" || updated.OAuthRefreshToken != "" ||
		updated.OAuthClientID != "" || updated.OAuthClientSecret != "" {
		t.Fatalf("OAuth credentials survived auth-mode switch: %#v", updated)
	}
	updatedCfg, err := llmcustomauth.ParseConfig(updated.CustomAuthConfigJSON)
	if err != nil {
		t.Fatal(err)
	}
	if updatedCfg.SigningSecret != "" || len(updatedCfg.StaticHeaders) != 0 ||
		len(updatedCfg.TokenHeaders) != 0 || len(updatedCfg.RefreshHeaders) != 0 ||
		len(updatedCfg.RefreshParameters) != 0 {
		t.Fatalf("OAuth-only secrets survived auth-mode switch: %#v", updatedCfg)
	}
}

func TestUpdateModel_CustomOAuthEmptyDisplayedValuesClearSavedSecrets(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	cfg := llmcustomauth.Config{
		Enabled:           true,
		SigningSecret:     "signing-secret",
		StaticHeaders:     map[string]string{"X-Required": "static-secret"},
		TokenHeaders:      map[string]string{"X-Token": "token-secret"},
		RefreshHeaders:    map[string]string{"X-Refresh": "refresh-secret"},
		RefreshParameters: map[string]string{"audience": "saved-audience"},
	}
	agent := &models.LLMConfig{
		Name:                 "Custom OAuth",
		Provider:             models.ProviderOpenAICompatible,
		AuthMethod:           models.AuthMethodOAuth,
		Model:                "custom-model",
		BaseURL:              "https://api.example.test/v1",
		PresetSlug:           "custom",
		OAuthClientID:        "client-id",
		OAuthClientSecret:    "client-secret",
		OAuthAuthorizeURL:    "https://login.example.test/authorize",
		OAuthTokenURL:        "https://login.example.test/token",
		CustomAuthConfigJSON: llmcustomauth.MarshalConfig(cfg),
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"name":                                 {"Custom OAuth"},
		"provider":                             {"openai_compatible"},
		"custom_auth_method":                   {"oauth"},
		"model":                                {"custom-model"},
		"base_url":                             {"https://api.example.test/v1"},
		"preset_slug":                          {"custom"},
		"transport":                            {"chat_completions"},
		"oauth_authorize_url":                  {"https://login.example.test/authorize"},
		"oauth_token_url":                      {"https://login.example.test/token"},
		"oauth_client_id":                      {"client-id"},
		"oauth_client_secret":                  {""},
		"custom_signing_secret":                {""},
		"custom_static_headers_json":           {""},
		"custom_token_headers_json":            {""},
		"custom_refresh_headers_json":          {""},
		"custom_refresh_parameters_json":       {""},
		"custom_authorization_parameters_json": {""},
		"temperature":                          {"0"},
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
		t.Fatal(err)
	}
	if updated.OAuthClientSecret != "" {
		t.Fatalf("empty displayed client secret did not clear saved value: %q", updated.OAuthClientSecret)
	}
	updatedCfg, err := llmcustomauth.ParseConfig(updated.CustomAuthConfigJSON)
	if err != nil {
		t.Fatal(err)
	}
	if updatedCfg.SigningSecret != "" || len(updatedCfg.StaticHeaders) != 0 ||
		len(updatedCfg.TokenHeaders) != 0 || len(updatedCfg.RefreshHeaders) != 0 ||
		len(updatedCfg.RefreshParameters) != 0 {
		t.Fatalf("empty displayed custom OAuth values did not clear saved values: %#v", updatedCfg)
	}
}

func TestUpdateModel_SwitchProviderWithoutAPIKeyFieldClearsStaleCredential(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:       "OpenAI API Key",
		Provider:   models.ProviderOpenAI,
		AuthMethod: models.AuthMethodAPIKey,
		Model:      "gpt-5.4",
		APIKey:     "sk-openai-old",
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create error: %v", err)
	}

	form := url.Values{}
	form.Set("name", "Local Compatible")
	form.Set("provider", "openai_compatible")
	form.Set("model", "local-model")
	form.Set("base_url", "http://127.0.0.1:8000/v1")
	form.Set("preset_slug", "vllm")
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
	if updated.APIKey != "" {
		t.Fatalf("expected stale API key cleared when key field omitted on provider switch, got %q", updated.APIKey)
	}
}

func TestUpdateModel_SwitchAwayFromOpenAICompatibleClearsEndpointFields(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:       "Compatible",
		Provider:   models.ProviderOpenAICompatible,
		AuthMethod: models.AuthMethodAPIKey,
		Model:      "provider/model",
		APIKey:     "sk-old",
		BaseURL:    "https://openrouter.ai/api/v1/",
		Transport:  "chat_completions",
		PresetSlug: "openrouter",
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create error: %v", err)
	}

	form := url.Values{}
	form.Set("name", "Compatible")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "api_key")
	form.Set("model", "claude-sonnet-4-5-20250929")
	form.Set("api_key", "sk-ant-new")
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
		t.Fatalf("provider = %q", updated.Provider)
	}
	if updated.BaseURL != "" || updated.Transport != "" || updated.PresetSlug != "" {
		t.Fatalf("expected compatible fields cleared, got %+v", updated)
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

func TestUpdateModel_ChangeAuthMethod_LegacyCLIToOAuth(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a historical subscription model with retired CLI auth method.
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

func TestUpdateModel_ChangeAuthMethod_OAuthStaleCLIFormNormalizesOAuth(t *testing.T) {
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

	// Update: a stale CLI value from a subscription form normalizes back to OAuth.
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
	if updated.AuthMethod != models.AuthMethodOAuth {
		t.Errorf("auth_method = %q, want %q", updated.AuthMethod, models.AuthMethodOAuth)
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
		OAuthAccessToken:  "openai-access-token",
		OAuthRefreshToken: "openai-refresh-token",
		OAuthExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
		OAuthAccountID:    "openai-account",
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
	if updated.OAuthAccessToken != "openai-access-token" || updated.OAuthRefreshToken != "openai-refresh-token" || updated.OAuthExpiresAt != agent.OAuthExpiresAt || updated.OAuthAccountID != "openai-account" {
		t.Fatalf("expected OAuth token state preserved, got access=%q refresh=%q expires=%d account=%q", updated.OAuthAccessToken, updated.OAuthRefreshToken, updated.OAuthExpiresAt, updated.OAuthAccountID)
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

// TestUpdateModel_DuplicateAuthMethodFormFields reproduces the scenario where
// two <select> elements with name="auth_method" exist in the form (one for Anthropic,
// one for OpenAI). When both are enabled, the browser sends both values and Go's
// FormValue returns the first one. The UI prevents this via toggleProviderFields(),
// which disables the inactive provider's select so only the active provider's value
// is submitted. The handler also defaults to OAuth (not CLI) when auth_method is
// absent or unrecognized for OAuth auth types, providing an additional safety net.
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

	// With duplicate form fields, Go's FormValue returns the first value ("cli").
	// The handler normalizes stale subscription CLI values back to OAuth.
	if updated.AuthMethod != models.AuthMethodOAuth {
		t.Errorf("auth_method = %q, want %q", updated.AuthMethod, models.AuthMethodOAuth)
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
			name:           "anthropic oauth with legacy cli connection normalizes to oauth",
			provider:       "anthropic",
			anthropicAuth:  "oauth",
			authMethod:     "cli",
			wantProvider:   models.ProviderAnthropic,
			wantAuthMethod: models.AuthMethodOAuth,
		},
		{
			name:           "anthropic oauth defaults to oauth when auth_method absent",
			provider:       "anthropic",
			anthropicAuth:  "oauth",
			authMethod:     "",
			wantProvider:   models.ProviderAnthropic,
			wantAuthMethod: models.AuthMethodOAuth,
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
			name:           "openai oauth with legacy cli connection normalizes to oauth",
			provider:       "openai",
			openaiAuth:     "oauth",
			authMethod:     "cli",
			wantProvider:   models.ProviderOpenAI,
			wantAuthMethod: models.AuthMethodOAuth,
		},
		{
			name:           "openai oauth defaults to oauth when auth_method absent",
			provider:       "openai",
			openaiAuth:     "oauth",
			authMethod:     "",
			wantProvider:   models.ProviderOpenAI,
			wantAuthMethod: models.AuthMethodOAuth,
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

func TestCreateModel_OAuthLegacyCLINormalizesOAuth(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "My OAuth Legacy CLI Model")
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
		if configs[i].Name == "My OAuth Legacy CLI Model" {
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

// TestCreateModel_AnthropicOAuthEmptyAuthMethod verifies that submitting an Anthropic
// OAuth form without an auth_method field (e.g. if the JS disabled the select and the
// browser omitted it) correctly defaults to OAuth — not CLI.
func TestCreateModel_AnthropicOAuthEmptyAuthMethod(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	// Do not set auth_method — simulates a disabled select being omitted by the browser.
	form := url.Values{}
	form.Set("name", "Anthropic OAuth Empty AuthMethod")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "oauth")
	form.Set("model", "claude-opus-4-6")
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
		if configs[i].Name == "Anthropic OAuth Empty AuthMethod" {
			found = &configs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created model not found")
	}
	if found.AuthMethod != models.AuthMethodOAuth {
		t.Errorf("auth_method = %q, want %q (absent auth_method should default to oauth for oauth auth type)", found.AuthMethod, models.AuthMethodOAuth)
	}
}

// TestCreateModel_OpenAIOAuthEmptyAuthMethod verifies that submitting an OpenAI
// OAuth form without an auth_method field (e.g. if the JS disabled the select and the
// browser omitted it) correctly defaults to OAuth — not CLI.
func TestCreateModel_OpenAIOAuthEmptyAuthMethod(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	// Do not set auth_method — simulates a disabled select being omitted by the browser.
	form := url.Values{}
	form.Set("name", "OpenAI OAuth Empty AuthMethod")
	form.Set("provider", "openai")
	form.Set("openai_auth_type", "oauth")
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
		if configs[i].Name == "OpenAI OAuth Empty AuthMethod" {
			found = &configs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created model not found")
	}
	if found.AuthMethod != models.AuthMethodOAuth {
		t.Errorf("auth_method = %q, want %q (absent auth_method should default to oauth for oauth auth type)", found.AuthMethod, models.AuthMethodOAuth)
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

func TestCreateModel_OpenAIOAuthLegacyCLINormalizesOAuth(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "OpenAI OAuth Legacy CLI Model")
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
		if configs[i].Name == "OpenAI OAuth Legacy CLI Model" {
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

func TestCreateModel_OpenAI_AstraClearsTemperature(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "OpenAI GPT-6 Astra")
	form.Set("provider", "openai")
	form.Set("openai_auth_type", "api_key")
	form.Set("model", "gpt-6-astra")
	form.Set("api_key", "sk-openai-test")
	form.Set("temperature", "0.8")
	form.Set("reasoning_effort", "medium")

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
	for _, config := range configs {
		if config.Name == "OpenAI GPT-6 Astra" {
			if config.Temperature != 0 {
				t.Fatalf("temperature = %v, want 0 for Astra", config.Temperature)
			}
			return
		}
	}
	t.Fatal("created Astra model not found")
}

func TestNormalizeOpenAIModel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"gpt-6-astra", "gpt-6-astra"},
		{"gpt-5.6-sol", "gpt-5.6-sol"},
		{"gpt-5.6-terra", "gpt-5.6-terra"},
		{"gpt-5.6-luna", "gpt-5.6-luna"},
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
		{"", ""},                         // empty stays empty for form validation
		{"invalid-model", "gpt-5.6-sol"}, // unknown defaults to latest
		{"  gpt-5.5  ", "gpt-5.5"},       // whitespace trimmed
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
		model    string
		input    string
		want     string
	}{
		{"openai astra max", models.ProviderOpenAI, "gpt-6-astra", "max", "max"},
		{"openai astra rejects none", models.ProviderOpenAI, "gpt-6-astra", "none", ""},
		{"openai none", models.ProviderOpenAI, "gpt-5.6-sol", "none", "none"},
		{"openai xhigh", models.ProviderOpenAI, "gpt-5.6-sol", "xhigh", "xhigh"},
		{"openai max", models.ProviderOpenAI, "gpt-5.6-sol", "max", "max"},
		{"openai rejects max for older model", models.ProviderOpenAI, "gpt-5.4-mini", "max", ""},
		{"openai rejects ultra", models.ProviderOpenAI, "gpt-5.6-sol", "ultra", ""},
		{"anthropic max", models.ProviderAnthropic, "claude-opus-4-6", "max", "max"},
		{"anthropic rejects xhigh", models.ProviderAnthropic, "claude-opus-4-6", "xhigh", ""},
		{"anthropic opus 5 low", models.ProviderAnthropic, "claude-opus-5", "low", "low"},
		{"anthropic opus 5 xhigh", models.ProviderAnthropic, "claude-opus-5", "xhigh", "xhigh"},
		{"anthropic sonnet 5 xhigh", models.ProviderAnthropic, "claude-sonnet-5", "xhigh", "xhigh"},
		{"anthropic fable 5.1 xhigh", models.ProviderAnthropic, "claude-fable-5-1", "xhigh", "xhigh"},
		{"anthropic fable 5 xhigh", models.ProviderAnthropic, "claude-fable-5", "xhigh", "xhigh"},
		{"anthropic mythos 5 xhigh", models.ProviderAnthropic, "claude-mythos-5", "xhigh", "xhigh"},
		{"anthropic opus 4.8 xhigh", models.ProviderAnthropic, "claude-opus-4-8", "xhigh", "xhigh"},
		{"anthropic opus 4.7 xhigh", models.ProviderAnthropic, "claude-opus-4-7", "xhigh", "xhigh"},
		{"anthropic rejects effort for sonnet 4.5", models.ProviderAnthropic, "claude-sonnet-4-5-20250929", "low", ""},
		{"anthropic rejects max for opus 4.5", models.ProviderAnthropic, "claude-opus-4-5-20251101", "max", ""},
		{"kimi k3 low", models.ProviderOpenAICompatible, "kimi-k3", "low", "low"},
		{"kimi k3 max", models.ProviderOpenAICompatible, " KIMI-K3 ", "max", "max"},
		{"kimi k3 rejects medium", models.ProviderOpenAICompatible, "kimi-k3", "medium", ""},
		{"kimi k2.7 rejects effort", models.ProviderOpenAICompatible, "kimi-k2.7-code", "high", ""},
		{"glm 5.2 minimal", models.ProviderOpenAICompatible, "glm-5.2", "minimal", "minimal"},
		{"glm 5.2 max", models.ProviderOpenAICompatible, " GLM-5.2 ", "max", "max"},
		{"glm 5.2 rejects ultra", models.ProviderOpenAICompatible, "glm-5.2", "ultra", ""},
		{"glm 5.1 rejects effort", models.ProviderOpenAICompatible, "glm-5.1", "high", ""},
		{"ollama clears effort", models.ProviderOllama, "llama", "high", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeProviderReasoningEffort(tt.provider, tt.model, tt.input); got != tt.want {
				t.Fatalf("normalizeProviderReasoningEffort(%q, %q, %q) = %q, want %q", tt.provider, tt.model, tt.input, got, tt.want)
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

	// Create a historical subscription model with retired CLI auth method.
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

// TestCreateModel_PreservesProjectIDInRedirect verifies that when CreateModel is called
// without an HTMX header (native form POST fallback), the redirect back to /models
// includes the project_id query param so the project picker is not reset.
func TestCreateModel_PreservesProjectIDInRedirect(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "Project Context Model")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "api_key")
	form.Set("model", "claude-sonnet-4-5-20250929")
	form.Set("api_key", "sk-ant-proj-key")
	form.Set("temperature", "0")

	// Native POST with project_id encoded in the action URL (as the JS sets it).
	req := httptest.NewRequest(http.MethodPost, "/models?project_id=test-project-123", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// No HX-Request header — simulates native form POST fallback.
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d: %s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if location != "/models?project_id=test-project-123" {
		t.Errorf("redirect Location = %q, want %q", location, "/models?project_id=test-project-123")
	}
}

// TestCreateModel_HTMX_NoNavigationPreservesProjectContext verifies that the HTMX
// submission path returns an in-place 200 response (no redirect), which means the
// browser URL (including ?project_id=) is never changed.
func TestCreateModel_HTMX_NoNavigationPreservesProjectContext(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "HTMX Project Context Model")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "api_key")
	form.Set("model", "claude-sonnet-4-5-20250929")
	form.Set("api_key", "sk-ant-htmx-proj-key")
	form.Set("temperature", "0")

	// HTMX POST — no redirect should happen; response is swapped in-place.
	req := httptest.NewRequest(http.MethodPost, "/models?project_id=my-project", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for HTMX request (no redirect), got %d: %s", rec.Code, rec.Body.String())
	}
	// No Location header — browser URL unchanged, project picker preserved.
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("expected no Location header on HTMX response, got %q", loc)
	}
	// Response should contain the updated models list for in-place swap.
	body := rec.Body.String()
	if !strings.Contains(body, "models-container") {
		t.Errorf("HTMX response should contain models-container div for in-place swap")
	}
}

// TestUpdateModel_PreservesProjectIDInRedirect verifies that the UpdateModel non-HTMX
// redirect also carries the project_id forward so editing a model doesn't reset the picker.
func TestUpdateModel_PreservesProjectIDInRedirect(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)

	// Create a model to update.
	agent := &models.LLMConfig{
		Name:      "Update Redirect Model",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		APIKey:    "sk-ant-update-key",
		IsDefault: false,
	}
	if err := llmConfigRepo.Create(context.Background(), agent); err != nil {
		t.Fatalf("create: %v", err)
	}

	form := url.Values{}
	form.Set("name", "Update Redirect Model Renamed")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "api_key")
	form.Set("model", "claude-sonnet-4-5-20250929")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPost, "/models/"+agent.ID+"?project_id=proj-xyz", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// No HX-Request header — simulates native form POST fallback.
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d: %s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if location != "/models?project_id=proj-xyz" {
		t.Errorf("redirect Location = %q, want %q", location, "/models?project_id=proj-xyz")
	}
}

func TestModelMutations_DefaultAndDeleteRedirectToPlainModels(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	defaultCandidate := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Default Redirect Model"
		a.IsDefault = false
	})
	setDefaultRec := postForm(e, "/models/"+defaultCandidate.ID+"/set-default?project_id=preserved-by-create-update-only", nil)
	if setDefaultRec.Code != http.StatusSeeOther {
		t.Fatalf("set default response code = %d, want %d: %s", setDefaultRec.Code, http.StatusSeeOther, setDefaultRec.Body.String())
	}
	if location := setDefaultRec.Header().Get("Location"); location != "/models" {
		t.Errorf("set default redirect = %q, want /models", location)
	}

	deleteCandidate := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Delete Redirect Model"
		a.IsDefault = false
	})
	deleteReq := httptest.NewRequest(http.MethodDelete, "/models/"+deleteCandidate.ID+"?project_id=preserved-by-create-update-only", nil)
	deleteRec := httptest.NewRecorder()
	e.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusSeeOther {
		t.Fatalf("delete response code = %d, want %d: %s", deleteRec.Code, http.StatusSeeOther, deleteRec.Body.String())
	}
	if location := deleteRec.Header().Get("Location"); location != "/models" {
		t.Errorf("delete redirect = %q, want /models", location)
	}

	if deleted, err := llmConfigRepo.GetByID(ctx, deleteCandidate.ID); err != nil || deleted != nil {
		t.Fatalf("deleted model = %#v, err = %v; want nil, nil", deleted, err)
	}
}

// TestCreateModel_RedirectWithoutProjectID verifies that when no project_id is in the
// URL, the redirect goes to plain /models (no dangling query param).
func TestCreateModel_RedirectWithoutProjectID(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	form := url.Values{}
	form.Set("name", "No Project Model")
	form.Set("provider", "anthropic")
	form.Set("anthropic_auth_type", "api_key")
	form.Set("model", "claude-sonnet-4-5-20250929")
	form.Set("api_key", "sk-ant-noproj")
	form.Set("temperature", "0")

	req := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d: %s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if location != "/models" {
		t.Errorf("redirect Location = %q, want plain /models", location)
	}
}

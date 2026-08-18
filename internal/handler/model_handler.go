package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	llmcustomauth "github.com/openvibely/openvibely/internal/llm/customauth"
	llmmixture "github.com/openvibely/openvibely/internal/llm/mixture"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	"github.com/openvibely/openvibely/web/templates/pages"
)

const (
	openAICompatibleAPIKeyHeader           = "X-OpenAI-Compatible-API-Key"
	openAICompatibleAuthHeaderNameHeader   = "X-OpenAI-Compatible-Auth-Header-Name"
	openAICompatibleAuthHeaderPrefixHeader = "X-OpenAI-Compatible-Auth-Header-Prefix"
	openAICompatibleExtraHeadersHeader     = "X-OpenAI-Compatible-Extra-Headers"
	openAICompatibleModelsArrayPathHeader  = "X-OpenAI-Compatible-Models-Array-Path"
	openAICompatibleModelIDFieldHeader     = "X-OpenAI-Compatible-Model-ID-Field"
)

func (h *Handler) ListModels(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	isHTMX := isHTMX(c)
	// applog.Debugf("[handler] ListModels requested htmx=%v", isHTMX)
	agents, err := h.llmConfigRepo.ListCards(c.Request().Context())
	if err != nil {
		applog.Infof("[handler] ListModels error: %v", err)
		return err
	}
	// applog.Debugf("[handler] ListModels found %d agents", len(agents))

	// Build per-model worker utilization
	modelWorkerStats := make(map[string]int)
	for _, agent := range agents {
		modelWorkerStats[agent.ID] = h.workerSvc.ModelRunning(agent.ID)
	}

	// For HTMX requests, return just the agents content
	if isHTMX {
		return render(c, http.StatusOK, pages.ModelsContent(agents, modelWorkerStats, h.desktopMode))
	}

	currentProjectID, _ := h.getCurrentProjectID(c)
	projects, _ := h.projectSvc.ListSelectorOptions(c.Request().Context())

	return render(c, http.StatusOK, pages.Models(projects, currentProjectID, agents, modelWorkerStats, h.desktopMode))
}

type modelEditDetails struct {
	ID                    string             `json:"id"`
	Name                  string             `json:"name"`
	Provider              models.LLMProvider `json:"provider"`
	Model                 string             `json:"model"`
	ReasoningEffort       string             `json:"reasoning_effort"`
	Temperature           float64            `json:"temperature"`
	IsDefault             bool               `json:"is_default"`
	APIKey                string             `json:"api_key"`
	AuthMethod            models.AuthMethod  `json:"auth_method"`
	MaxWorkers            int                `json:"max_workers"`
	WorkerTimeout         int                `json:"worker_timeout"`
	OAuthClientID         string             `json:"oauth_client_id"`
	OAuthClientSecret     string             `json:"oauth_client_secret"`
	OAuthAuthorizeURL     string             `json:"oauth_authorize_url"`
	OAuthTokenURL         string             `json:"oauth_token_url"`
	OAuthScopes           string             `json:"oauth_scopes"`
	OllamaBaseURL         string             `json:"ollama_base_url"`
	BaseURL               string             `json:"base_url"`
	Transport             string             `json:"transport"`
	PresetSlug            string             `json:"preset_slug"`
	ModelsURL             string             `json:"models_url"`
	AuthHeaderName        string             `json:"auth_header_name"`
	AuthHeaderValuePrefix string             `json:"auth_header_value_prefix"`
	ExtraHeadersJSON      string             `json:"extra_headers_json"`
	ExtraBodyJSON         string             `json:"extra_body_json"`
	CustomAuthConfigJSON  string             `json:"custom_auth_config_json"`
	MixtureConfigJSON     string             `json:"mixture_config_json"`
	AutoStartTasks        bool               `json:"auto_start_tasks"`
}

func (h *Handler) GetModelEditDetails(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	config, err := h.llmConfigRepo.GetByID(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	if config == nil {
		return echo.NewHTTPError(http.StatusNotFound, "model configuration not found")
	}
	details := modelEditDetails{
		ID: config.ID, Name: config.Name, Provider: config.Provider, Model: config.Model,
		ReasoningEffort: config.ReasoningEffort, Temperature: config.Temperature,
		IsDefault: config.IsDefault, APIKey: config.APIKey, AuthMethod: config.AuthMethod,
		MaxWorkers: config.MaxWorkers, WorkerTimeout: config.WorkerTimeout,
		OAuthClientID: config.OAuthClientID, OAuthAuthorizeURL: config.OAuthAuthorizeURL,
		OAuthTokenURL: config.OAuthTokenURL, OAuthScopes: config.OAuthScopes,
		OllamaBaseURL: config.OllamaBaseURL, BaseURL: config.BaseURL,
		Transport: config.Transport, PresetSlug: config.PresetSlug, ModelsURL: config.ModelsURL,
		AuthHeaderName: config.AuthHeaderName, AuthHeaderValuePrefix: config.AuthHeaderValuePrefix,
		AutoStartTasks: config.AutoStartTasks,
	}
	if config.Provider == models.ProviderOpenAICompatible &&
		(strings.TrimSpace(config.PresetSlug) == "" || strings.EqualFold(config.PresetSlug, "custom")) {
		details.ExtraHeadersJSON = config.ExtraHeadersJSON
		details.ExtraBodyJSON = config.ExtraBodyJSON
		if config.AuthMethod == models.AuthMethodOAuth {
			details.OAuthClientSecret = config.OAuthClientSecret
			details.CustomAuthConfigJSON = config.CustomAuthConfigJSON
		}
	}
	if config.Provider == models.ProviderMixture {
		details.MixtureConfigJSON = config.MixtureConfigJSON
	}
	return c.JSON(http.StatusOK, details)
}

// resolveProviderAndAuth maps UI form values to DB provider and auth_method.
// The UI shows "Anthropic" and "OpenAI" as single providers with auth type sub-selection,
// while the DB stores provider and auth_method separately.
func resolveProviderAndAuth(provider, anthropicAuthType, openaiAuthType, authMethod string) (models.LLMProvider, models.AuthMethod) {
	if provider == string(models.ProviderMixture) {
		return models.ProviderMixture, models.AuthMethodAPIKey
	}
	if provider == string(models.ProviderOpenAICompatible) || isKnownOpenAICompatibleUIProvider(provider) {
		return models.ProviderOpenAICompatible, models.AuthMethodAPIKey
	}
	// Accept both "subscription" (legacy) and "oauth" (current) form values.
	// CLI auth is no longer a supported model transport, so legacy or absent
	// auth_method values normalize to OAuth for subscription-backed providers.
	if provider == "anthropic" && (anthropicAuthType == "subscription" || anthropicAuthType == "oauth") {
		am := models.AuthMethod(authMethod)
		if am != models.AuthMethodOAuth {
			am = models.AuthMethodOAuth
		}
		return models.ProviderAnthropic, am
	}
	if provider == "anthropic" {
		return models.ProviderAnthropic, models.AuthMethodAPIKey
	}
	if provider == "openai" && openaiAuthType == "api_key" {
		return models.ProviderOpenAI, models.AuthMethodAPIKey
	}
	// Accept both "subscription" (legacy) and "oauth" (current) form values.
	// CLI auth is no longer a supported model transport, so legacy or absent
	// auth_method values normalize to OAuth for subscription-backed providers.
	if provider == "openai" && (openaiAuthType == "subscription" || openaiAuthType == "oauth") {
		am := models.AuthMethod(authMethod)
		if am != models.AuthMethodOAuth {
			am = models.AuthMethodOAuth
		}
		return models.ProviderOpenAI, am
	}
	if provider == "openai" {
		return models.ProviderOpenAI, models.AuthMethodAPIKey
	}
	return models.LLMProvider(provider), models.AuthMethodAPIKey
}

func isKnownOpenAICompatibleUIProvider(provider string) bool {
	const prefix = "openai_compatible_"
	if !strings.HasPrefix(provider, prefix) {
		return false
	}
	switch strings.TrimPrefix(provider, prefix) {
	case "openrouter", "nvidia_nim", "vllm", "lm_studio", "sglang", "litellm", "deepinfra", "fireworks", "groq", "mistral", "cerebras", "together", "huggingface_router", "deepseek", "moonshot", "dashscope", "dashscope_intl", "alibaba_coding_plan", "zai_glm", "novita", "venice", "qianfan", "kilo_code", "arcee", "stepfun", "stepfun_step_plan", "gmi_cloud", "chutes", "tokenhub", "tokenhub_intl", "xiaomi_mimo", "inferrs", "ds4", "custom":
		return true
	default:
		return false
	}
}

func normalizeOpenAICompatibleTransport(transport string) string {
	transport = strings.TrimSpace(transport)
	if transport == "" {
		return "chat_completions"
	}
	return transport
}

func validateOpenAICompatibleBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("base URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("base URL must use http or https")
	}
	if u.Host == "" {
		return "", fmt.Errorf("base URL must include a host")
	}
	if u.Scheme == "http" && !isLocalOrPrivateHost(u.Hostname()) {
		return "", fmt.Errorf("plain http base URLs are only allowed for localhost or private development hosts")
	}
	return raw, nil
}

func isLocalOrPrivateHost(host string) bool {
	host = strings.TrimSpace(strings.TrimSuffix(host, "."))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}

type openAICompatibleModelInfo struct {
	ID string `json:"id"`
}

type openAICompatibleModelsResponse struct {
	Models     []openAICompatibleModelInfo `json:"models"`
	TriedURLs  []string                    `json:"tried_urls"`
	ResolvedID string                      `json:"resolved_id,omitempty"`
}

var errCustomOAuthUnauthorized = errors.New("custom OAuth request unauthorized")

func openAICompatibleModelsURLs(baseURL, modelsURL string) ([]string, error) {
	var urls []string
	if strings.TrimSpace(modelsURL) != "" {
		u, err := validateOpenAICompatibleBaseURL(modelsURL)
		if err != nil {
			return nil, err
		}
		urls = append(urls, u)
		return urls, nil
	}
	base, err := validateOpenAICompatibleBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/models"
	urls = append(urls, u.String())

	basePath := strings.TrimRight(u.EscapedPath(), "/")
	if !strings.HasSuffix(basePath, "/v1/models") {
		v1, err := url.Parse(base)
		if err != nil {
			return nil, err
		}
		v1.Path = strings.TrimRight(v1.Path, "/") + "/v1/models"
		if v1.String() != urls[0] {
			urls = append(urls, v1.String())
		}
	}
	return urls, nil
}

func applyOpenAICompatibleForm(c echo.Context, agent *models.LLMConfig) error {
	baseURL, err := validateOpenAICompatibleBaseURL(c.FormValue("base_url"))
	if err != nil {
		return err
	}
	agent.BaseURL = baseURL
	agent.Transport = normalizeOpenAICompatibleTransport(c.FormValue("transport"))
	agent.PresetSlug = strings.TrimSpace(c.FormValue("preset_slug"))
	if agent.PresetSlug == "" {
		agent.PresetSlug = "custom"
	}
	agent.ModelsURL = strings.TrimSpace(c.FormValue("models_url"))
	agent.AuthHeaderName = strings.TrimSpace(c.FormValue("auth_header_name"))
	agent.AuthHeaderValuePrefix = c.FormValue("auth_header_value_prefix")
	if raw, present := formValueIfPresent(c, "extra_headers_json"); present {
		agent.ExtraHeadersJSON = strings.TrimSpace(raw)
	} else if c.FormValue("clear_extra_headers") == "on" {
		agent.ExtraHeadersJSON = ""
	}
	if raw, present := formValueIfPresent(c, "extra_body_json"); present {
		agent.ExtraBodyJSON = strings.TrimSpace(raw)
	} else if c.FormValue("clear_extra_body") == "on" {
		agent.ExtraBodyJSON = ""
	}
	if !strings.EqualFold(agent.PresetSlug, "custom") {
		agent.AuthHeaderName = ""
		agent.AuthHeaderValuePrefix = ""
		agent.ExtraHeadersJSON = ""
		agent.ExtraBodyJSON = ""
	}
	if err := validateOpenAICompatibleRequestExtras(agent); err != nil {
		return err
	}
	cfg, err := llmcustomauth.ParseConfig(agent.CustomAuthConfigJSON)
	if err != nil {
		return err
	}
	cfg.Enabled = agent.AuthMethod == models.AuthMethodOAuth
	cfg.RefreshURL = strings.TrimSpace(c.FormValue("custom_refresh_url"))
	cfg.PKCE = c.FormValue("custom_oauth_pkce") == "on"
	cfg.TokenRequestFormat = strings.TrimSpace(c.FormValue("custom_token_request_format"))
	cfg.AccessTokenField = strings.TrimSpace(c.FormValue("custom_access_token_field"))
	cfg.RefreshTokenField = strings.TrimSpace(c.FormValue("custom_refresh_token_field"))
	cfg.ExpiresInField = strings.TrimSpace(c.FormValue("custom_expires_in_field"))
	cfg.AuthorizationMode = strings.TrimSpace(c.FormValue("custom_authorization_mode"))
	cfg.AccessTokenHeader = strings.TrimSpace(c.FormValue("custom_access_token_header"))
	cfg.AccessTokenPrefix = c.FormValue("custom_access_token_prefix")
	cfg.UserAgent = strings.TrimSpace(c.FormValue("custom_user_agent"))
	cfg.ProfileURL = strings.TrimSpace(c.FormValue("custom_profile_url"))
	cfg.ProfileInstancePath = strings.TrimSpace(c.FormValue("custom_profile_instance_path"))
	cfg.ProfileTeamPath = strings.TrimSpace(c.FormValue("custom_profile_team_path"))
	cfg.InstanceHeader = strings.TrimSpace(c.FormValue("custom_instance_header"))
	cfg.TeamHeader = strings.TrimSpace(c.FormValue("custom_team_header"))
	if secret, present := formValueIfPresent(c, "custom_signing_secret"); present {
		cfg.SigningSecret = secret
	} else if c.FormValue("custom_clear_signing_secret") == "on" {
		cfg.SigningSecret = ""
	}
	cfg.TimestampHeader = strings.TrimSpace(c.FormValue("custom_timestamp_header"))
	cfg.SignatureHeader = strings.TrimSpace(c.FormValue("custom_signature_header"))
	cfg.ModelsArrayPath = strings.TrimSpace(c.FormValue("custom_models_array_path"))
	cfg.ModelIDField = strings.TrimSpace(c.FormValue("custom_model_id_field"))
	cfg.StandardTokenFields = c.FormValue("custom_standard_token_fields") == "on"
	cfg.CallbackParameter = strings.TrimSpace(c.FormValue("custom_callback_parameter"))
	cfg.LocalCallbackHost = strings.TrimSpace(c.FormValue("custom_local_callback_host"))
	cfg.LocalCallbackPath = strings.TrimSpace(c.FormValue("custom_local_callback_path"))
	privateEndpointsRequested := c.FormValue("custom_allow_private_endpoints") == "on" ||
		openAICompatiblePresetUsesPrivateEndpoints(agent.PresetSlug)
	if privateEndpointsRequested && !llmcustomauth.PrivateEndpointPolicyEnabled() {
		return fmt.Errorf("private/local model endpoints are disabled by server policy")
	}
	cfg.AllowPrivateEndpoints = privateEndpointsRequested
	if _, err := llmcustomauth.ValidateEndpoint(baseURL, cfg.AllowPrivateEndpoints); err != nil {
		return fmt.Errorf("base URL: %w", err)
	}
	cfg.RefreshRequestFormat = strings.TrimSpace(c.FormValue("custom_refresh_request_format"))
	cfg.RefreshIncludeGrantType = c.FormValue("custom_refresh_include_grant_type") == "on"
	cfg.RefreshIncludeClient = c.FormValue("custom_refresh_include_client") == "on"
	if staticHeadersRaw, present := formValueIfPresent(c, "custom_static_headers_json"); present {
		if staticHeadersRaw = strings.TrimSpace(staticHeadersRaw); staticHeadersRaw == "" {
			cfg.StaticHeaders = nil
		} else if err := decodeStringMap(staticHeadersRaw, &cfg.StaticHeaders); err != nil {
			return fmt.Errorf("additional headers must be a JSON object of string values: %w", err)
		}
	} else if c.FormValue("custom_clear_static_headers") == "on" {
		cfg.StaticHeaders = nil
	}
	if authorizationParametersRaw, present := formValueIfPresent(c, "custom_authorization_parameters_json"); present {
		if authorizationParametersRaw = strings.TrimSpace(authorizationParametersRaw); authorizationParametersRaw == "" {
			cfg.AuthorizationParameters = nil
		} else if err := decodeStringMap(authorizationParametersRaw, &cfg.AuthorizationParameters); err != nil {
			return fmt.Errorf("authorization parameters must be a JSON object of string values: %w", err)
		}
	} else if c.FormValue("custom_clear_authorization_parameters") == "on" {
		cfg.AuthorizationParameters = nil
	}
	if err := applyOptionalSecretHeaderMap(c, "custom_token_headers_json", "custom_clear_token_headers", &cfg.TokenHeaders, "token endpoint headers"); err != nil {
		return err
	}
	if refreshParametersRaw, present := formValueIfPresent(c, "custom_refresh_parameters_json"); present {
		if refreshParametersRaw = strings.TrimSpace(refreshParametersRaw); refreshParametersRaw == "" {
			cfg.RefreshParameters = nil
		} else if err := decodeStringMap(refreshParametersRaw, &cfg.RefreshParameters); err != nil {
			return fmt.Errorf("refresh parameters must be a JSON object of string values: %w", err)
		}
	} else if c.FormValue("custom_clear_refresh_parameters") == "on" {
		cfg.RefreshParameters = nil
	}
	if err := applyOptionalSecretHeaderMap(c, "custom_refresh_headers_json", "custom_clear_refresh_headers", &cfg.RefreshHeaders, "refresh endpoint headers"); err != nil {
		return err
	}
	if agent.AuthMethod == models.AuthMethodOAuth {
		if err := validateCustomOAuthEndpoints(agent, cfg); err != nil {
			return err
		}
		if _, _, err := normalizeCustomOAuthLocalCallback(cfg); err != nil {
			return err
		}
	}
	agent.CustomAuthConfigJSON = llmcustomauth.MarshalConfig(cfg)
	if agent.AuthMethod != models.AuthMethodOAuth && strings.TrimSpace(agent.ModelsURL) != "" {
		if _, err := llmcustomauth.ValidateEndpoint(agent.ModelsURL, cfg.AllowPrivateEndpoints); err != nil {
			return fmt.Errorf("models URL: %w", err)
		}
	}
	if maxTokens, err := strconv.Atoi(c.FormValue("default_max_tokens")); err == nil && maxTokens > 0 {
		agent.DefaultMaxTokens = maxTokens
	} else {
		agent.DefaultMaxTokens = 0
	}
	return nil
}

func validateOpenAICompatibleRequestExtras(agent *models.LLMConfig) error {
	headers := map[string]string{}
	if agent.ExtraHeadersJSON != "" {
		if err := json.Unmarshal([]byte(agent.ExtraHeadersJSON), &headers); err != nil {
			return fmt.Errorf("additional request headers must be a JSON object of string values: %w", err)
		}
		if headers == nil {
			return fmt.Errorf("additional request headers must be a JSON object of string values")
		}
	}
	if agent.ExtraBodyJSON != "" {
		var body map[string]any
		if err := json.Unmarshal([]byte(agent.ExtraBodyJSON), &body); err != nil {
			return fmt.Errorf("additional request body must be a JSON object: %w", err)
		}
		if body == nil {
			return fmt.Errorf("additional request body must be a JSON object")
		}
	}
	cfg := llmcustomauth.Config{
		AccessTokenHeader: agent.GetAuthHeaderName(),
		AccessTokenPrefix: agent.GetAuthHeaderValuePrefix(),
		StaticHeaders:     headers,
	}
	if err := llmcustomauth.ValidateHeaders(cfg); err != nil {
		return err
	}
	return llmcustomauth.ValidateRequestHeaderValues(cfg, llmcustomauth.State{}, agent.APIKey)
}

func openAICompatiblePresetUsesPrivateEndpoints(preset string) bool {
	switch strings.ToLower(strings.TrimSpace(preset)) {
	case "vllm", "lm_studio", "sglang", "litellm", "inferrs", "ds4":
		return true
	default:
		return false
	}
}

func applyOptionalSecretHeaderMap(c echo.Context, valueField, clearField string, target *map[string]string, label string) error {
	raw, present := formValueIfPresent(c, valueField)
	if present {
		if raw = strings.TrimSpace(raw); raw == "" {
			*target = nil
		} else if err := decodeStringMap(raw, target); err != nil {
			return fmt.Errorf("%s must be a JSON object of string values: %w", label, err)
		}
	} else if c.FormValue(clearField) == "on" {
		*target = nil
	}
	return nil
}

func decodeStringMap(raw string, target *map[string]string) error {
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return err
	}
	*target = values
	return nil
}

func validateCustomOAuthEndpoints(agent *models.LLMConfig, cfg llmcustomauth.Config) error {
	if err := llmcustomauth.ValidateAuthorizationParameters(cfg); err != nil {
		return err
	}
	if err := llmcustomauth.ValidateHeaders(cfg); err != nil {
		return err
	}
	endpoints := map[string]string{
		"base URL":      agent.BaseURL,
		"authorize URL": agent.OAuthAuthorizeURL,
		"token URL":     agent.OAuthTokenURL,
		"refresh URL":   cfg.RefreshURL,
		"profile URL":   cfg.ProfileURL,
		"models URL":    agent.ModelsURL,
	}
	for label, endpoint := range endpoints {
		if strings.TrimSpace(endpoint) == "" {
			continue
		}
		if _, err := llmcustomauth.ValidateEndpoint(endpoint, cfg.AllowPrivateEndpoints); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
	}
	return nil
}

func clearOpenAICompatibleFields(agent *models.LLMConfig) {
	agent.BaseURL = ""
	agent.Transport = ""
	agent.PresetSlug = ""
	agent.ModelsURL = ""
	agent.AuthHeaderName = ""
	agent.AuthHeaderValuePrefix = ""
	agent.ExtraHeadersJSON = ""
	agent.ExtraBodyJSON = ""
	agent.DefaultMaxTokens = 0
	agent.TokenExchangeFormat = ""
	agent.TokenRefreshFormat = ""
	agent.CustomAuthConfigJSON = ""
	agent.CustomAuthStateJSON = ""
}

func clearOAuthState(agent *models.LLMConfig) {
	agent.OAuthAccessToken = ""
	agent.OAuthRefreshToken = ""
	agent.OAuthExpiresAt = 0
	agent.OAuthAccountID = ""
	agent.OAuthClientID = ""
	agent.OAuthClientSecret = ""
	agent.OAuthAuthorizeURL = ""
	agent.OAuthTokenURL = ""
	agent.OAuthScopes = ""
	agent.CustomAuthStateJSON = ""
}

func clearOAuthCredentials(agent *models.LLMConfig) {
	agent.OAuthAccessToken = ""
	agent.OAuthRefreshToken = ""
	agent.OAuthExpiresAt = 0
	agent.OAuthAccountID = ""
	agent.CustomAuthStateJSON = ""
}

func oauthSecurityConfigChanged(before, after models.LLMConfig) bool {
	return before.BaseURL != after.BaseURL ||
		before.ModelsURL != after.ModelsURL ||
		before.OAuthClientID != after.OAuthClientID ||
		before.OAuthClientSecret != after.OAuthClientSecret ||
		before.OAuthAuthorizeURL != after.OAuthAuthorizeURL ||
		before.OAuthTokenURL != after.OAuthTokenURL ||
		before.OAuthScopes != after.OAuthScopes ||
		before.CustomAuthConfigJSON != after.CustomAuthConfigJSON
}

func parseMixtureConfigForm(c echo.Context) (llmmixture.Config, error) {
	raw := strings.TrimSpace(c.FormValue("mixture_config_json"))
	if raw != "" {
		return llmmixture.ParseConfig(raw)
	}
	cfg := llmmixture.DefaultConfig()
	cfg.Enabled = c.FormValue("mixture_enabled") != "" && c.FormValue("mixture_enabled") != "false" && c.FormValue("mixture_enabled") != "0"
	cfg.Aggregator = llmmixture.ModelSlot{AgentConfigID: strings.TrimSpace(c.FormValue("mixture_aggregator_id"))}
	if cfg.Enabled == false && c.FormValue("mixture_enabled") == "" {
		cfg.Enabled = true
	}
	for _, rawID := range c.Request().Form["mixture_reference_ids"] {
		for _, id := range strings.Split(rawID, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				cfg.ReferenceModels = append(cfg.ReferenceModels, llmmixture.ModelSlot{AgentConfigID: id})
			}
		}
	}
	if v, err := strconv.ParseFloat(c.FormValue("mixture_reference_temperature"), 64); err == nil {
		cfg.ReferenceTemperature = v
	}
	if v, err := strconv.ParseFloat(c.FormValue("mixture_aggregator_temperature"), 64); err == nil {
		cfg.AggregatorTemperature = v
	}
	if v, err := strconv.Atoi(c.FormValue("mixture_reference_timeout_seconds")); err == nil {
		cfg.ReferenceTimeoutSeconds = v
	}
	if v, err := strconv.Atoi(c.FormValue("mixture_max_reference_workers")); err == nil {
		cfg.MaxReferenceWorkers = v
	}
	return llmmixture.NormalizeConfig(cfg)
}

func (h *Handler) applyAndValidateMixtureForm(ctx context.Context, c echo.Context, agent *models.LLMConfig) error {
	cfg, err := parseMixtureConfigForm(c)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Aggregator.AgentConfigID) == "" {
		return fmt.Errorf("mixture aggregator is required")
	}
	if cfg.Enabled && len(cfg.ReferenceModels) == 0 {
		return fmt.Errorf("at least one reference model is required when mixture is enabled")
	}
	slots := make([]llmmixture.ModelSlot, 0, len(cfg.ReferenceModels)+1)
	slots = append(slots, cfg.Aggregator)
	slots = append(slots, cfg.ReferenceModels...)
	ids := make(map[string]struct{}, len(slots))
	for _, slot := range slots {
		id := strings.TrimSpace(slot.AgentConfigID)
		if id == "" {
			return fmt.Errorf("mixture model slot is missing a model")
		}
		ids[id] = struct{}{}
	}
	configs, err := h.llmConfigRepo.GetByIDs(ctx, keysOfStringSet(ids))
	if err != nil {
		return err
	}
	populateSlot := func(slot *llmmixture.ModelSlot, role string) error {
		id := strings.TrimSpace(slot.AgentConfigID)
		cfg, ok := configs[id]
		if !ok || cfg == nil {
			return fmt.Errorf("%s model config not found", role)
		}
		if cfg.Provider == models.ProviderMixture {
			return fmt.Errorf("%s cannot use a mixture model", role)
		}
		if !isCallableMixtureSlot(*cfg) {
			return fmt.Errorf("%s model %q is not callable as a mixture slot", role, cfg.Name)
		}
		slot.Provider = string(cfg.Provider)
		slot.Model = cfg.Model
		if strings.TrimSpace(slot.Label) == "" {
			slot.Label = cfg.Name
		}
		return nil
	}
	if err := populateSlot(&cfg.Aggregator, "aggregator"); err != nil {
		return err
	}
	seenRefs := map[string]struct{}{}
	for i := range cfg.ReferenceModels {
		if err := populateSlot(&cfg.ReferenceModels[i], "reference"); err != nil {
			return err
		}
		id := strings.TrimSpace(cfg.ReferenceModels[i].AgentConfigID)
		if _, ok := seenRefs[id]; ok {
			return fmt.Errorf("duplicate reference model %q", cfg.ReferenceModels[i].Label)
		}
		seenRefs[id] = struct{}{}
	}
	normalized, err := llmmixture.NormalizeConfig(cfg)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	agent.MixtureConfigJSON = string(encoded)
	agent.Temperature = 0
	if strings.TrimSpace(agent.Model) == "" {
		agent.Model = "default"
	}
	agent.AuthMethod = models.AuthMethodAPIKey
	agent.APIKey = ""
	return nil
}

func isCallableMixtureSlot(cfg models.LLMConfig) bool {
	return cfg.IsCallableMixtureSlot()
}

func keysOfStringSet(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

type modelFormMode int

const (
	modelFormCreate modelFormMode = iota
	modelFormUpdate
)

type modelFormOptions struct {
	mode                         modelFormMode
	beforeProviderSpecificFields func()
}

func isModelValidationError(err error) bool {
	return errors.Is(err, repository.ErrLLMConfigNameRequired) ||
		errors.Is(err, repository.ErrLLMConfigNameDuplicate) ||
		errors.Is(err, repository.ErrLLMConfigModelRequired)
}

func validateBrowserRunnableModelSlug(agent *models.LLMConfig) error {
	switch agent.Provider {
	case models.ProviderAnthropic, models.ProviderOpenAI, models.ProviderOpenAICompatible, models.ProviderOllama:
		if strings.TrimSpace(agent.Model) == "" {
			return repository.ErrLLMConfigModelRequired
		}
	}
	return nil
}

func (h *Handler) normalizeBrowserModelForm(ctx context.Context, c echo.Context, agent *models.LLMConfig, opts modelFormOptions) error {
	provider, authMethod := resolveProviderAndAuth(
		c.FormValue("provider"),
		c.FormValue("anthropic_auth_type"),
		c.FormValue("openai_auth_type"),
		c.FormValue("auth_method"),
	)
	if provider == models.ProviderOpenAICompatible && c.FormValue("custom_auth_method") == string(models.AuthMethodOAuth) {
		authMethod = models.AuthMethodOAuth
	}

	normalizedName, err := h.llmConfigRepo.ValidateNameAvailable(ctx, c.FormValue("name"), agent.ID)
	if err != nil {
		return err
	}
	agent.Name = normalizedName
	agent.Provider = provider
	agent.AuthMethod = authMethod

	agent.Model = c.FormValue("model")
	if agent.Provider == models.ProviderOpenAI {
		agent.Model = normalizeOpenAIModel(agent.Model)
	}
	agent.ReasoningEffort = normalizeProviderReasoningEffort(agent.Provider, agent.Model, c.FormValue("reasoning_effort"))
	if agent.Provider == models.ProviderOpenAICompatible {
		agent.Model = strings.TrimSpace(agent.Model)
	}
	if temp, err := strconv.ParseFloat(c.FormValue("temperature"), 64); err == nil {
		agent.Temperature = temp
	}
	agent.IsDefault = c.FormValue("is_default") == "on"
	agent.AutoStartTasks = c.FormValue("auto_start_tasks") == "on"
	if mw, err := strconv.Atoi(c.FormValue("model_max_workers")); err == nil {
		if mw < 0 {
			mw = 0
		}
		if mw > 10 {
			mw = 10
		}
		agent.MaxWorkers = mw
	}
	if wt, err := strconv.Atoi(c.FormValue("worker_timeout")); err == nil {
		if wt < 0 {
			wt = 0
		}
		agent.WorkerTimeout = wt
	}

	if opts.beforeProviderSpecificFields != nil {
		opts.beforeProviderSpecificFields()
	} else if opts.mode == modelFormCreate {
		agent.APIKey = c.FormValue("api_key")
	}

	applyModelOAuthForm(c, agent, opts.mode)

	if agent.Provider == models.ProviderOllama {
		agent.OllamaBaseURL = strings.TrimSpace(c.FormValue("ollama_base_url"))
		if customModel := strings.TrimSpace(c.FormValue("ollama_custom_model")); customModel != "" {
			agent.Model = customModel
		}
	} else {
		agent.OllamaBaseURL = ""
	}
	if agent.Provider == models.ProviderOpenAICompatible {
		if err := applyOpenAICompatibleForm(c, agent); err != nil {
			return err
		}
	} else {
		clearOpenAICompatibleFields(agent)
	}
	if agent.Provider == models.ProviderMixture {
		agent.OllamaBaseURL = ""
		if err := h.applyAndValidateMixtureForm(ctx, c, agent); err != nil {
			return err
		}
	} else {
		agent.MixtureConfigJSON = ""
	}
	if opts.mode == modelFormCreate && agent.Provider == "" {
		agent.Provider = models.ProviderAnthropic
	}
	if err := validateBrowserRunnableModelSlug(agent); err != nil {
		return err
	}
	return nil
}

func applyModelOAuthForm(c echo.Context, agent *models.LLMConfig, mode modelFormMode) {
	if (agent.Provider != models.ProviderOpenAI && agent.Provider != models.ProviderOpenAICompatible) || agent.AuthMethod != models.AuthMethodOAuth {
		return
	}
	if mode == modelFormCreate {
		agent.OAuthClientID = c.FormValue("oauth_client_id")
		agent.OAuthClientSecret = c.FormValue("oauth_client_secret")
		agent.OAuthAuthorizeURL = c.FormValue("oauth_authorize_url")
		agent.OAuthTokenURL = c.FormValue("oauth_token_url")
		agent.OAuthScopes = c.FormValue("oauth_scopes")
		return
	}
	if v, ok := formValueIfPresent(c, "oauth_client_id"); ok {
		agent.OAuthClientID = v
	}
	if v, ok := formValueIfPresent(c, "oauth_client_secret"); ok {
		agent.OAuthClientSecret = v
	} else if c.FormValue("clear_oauth_client_secret") == "on" {
		agent.OAuthClientSecret = ""
	}
	if v, ok := formValueIfPresent(c, "oauth_authorize_url"); ok {
		agent.OAuthAuthorizeURL = v
	}
	if v, ok := formValueIfPresent(c, "oauth_token_url"); ok {
		agent.OAuthTokenURL = v
	}
	if v, ok := formValueIfPresent(c, "oauth_scopes"); ok {
		agent.OAuthScopes = v
	}
}

func (h *Handler) mixturesUsingModel(ctx context.Context, modelID string) ([]string, error) {
	agents, err := h.llmConfigRepo.ListMixtureDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, agent := range agents {
		if strings.TrimSpace(agent.MixtureConfigJSON) == "" {
			continue
		}
		cfg, err := llmmixture.ParseConfig(agent.MixtureConfigJSON)
		if err != nil {
			continue
		}
		if cfg.Aggregator.AgentConfigID == modelID {
			names = append(names, agent.Name)
			continue
		}
		for _, ref := range cfg.ReferenceModels {
			if ref.AgentConfigID == modelID {
				names = append(names, agent.Name)
				break
			}
		}
	}
	return names, nil
}

func (h *Handler) CreateModel(c echo.Context) error {
	if id := strings.TrimSpace(c.FormValue("model_config_id")); id != "" {
		applog.Infof("[handler] CreateModel received existing model_config_id=%s; updating instead", id)
		return h.updateModelByID(c, id)
	}

	a := &models.LLMConfig{}
	if err := h.normalizeBrowserModelForm(c.Request().Context(), c, a, modelFormOptions{mode: modelFormCreate}); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	applog.Infof("[handler] CreateModel name=%q provider=%s model=%s auth_method=%s temp=%.1f default=%v",
		a.Name, a.Provider, a.Model, a.AuthMethod, a.Temperature, a.IsDefault)

	if err := h.llmConfigRepo.Create(c.Request().Context(), a); err != nil {
		applog.Infof("[handler] CreateModel error: %v", err)
		if isModelValidationError(err) {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return err
	}
	applog.Infof("[handler] CreateModel success id=%s", a.ID)

	// Return updated agents list for HTMX
	if isHTMX(c) {
		return h.renderRefreshedModels(c)
	}
	redirectURL := "/models"
	if projectID := c.QueryParam("project_id"); projectID != "" {
		redirectURL += "?project_id=" + url.QueryEscape(projectID)
	}
	return c.Redirect(http.StatusSeeOther, redirectURL)
}

func (h *Handler) UpdateModel(c echo.Context) error {
	return h.updateModelByID(c, c.Param("id"))
}

func (h *Handler) updateModelByID(c echo.Context, id string) error {
	applog.Infof("[handler] UpdateModel id=%s", id)

	agent, err := h.llmConfigRepo.GetByID(c.Request().Context(), id)
	if err != nil {
		applog.Infof("[handler] UpdateModel fetch error: %v", err)
		return err
	}
	if agent == nil {
		applog.Infof("[handler] UpdateModel not found id=%s", id)
		return echo.NewHTTPError(http.StatusNotFound, "agent not found")
	}
	previous := *agent

	if err := h.normalizeBrowserModelForm(c.Request().Context(), c, agent, modelFormOptions{
		mode: modelFormUpdate,
		beforeProviderSpecificFields: func() {
			providerOrAuthChanged := previous.Provider != agent.Provider || previous.AuthMethod != agent.AuthMethod
			if agent.Provider == models.ProviderOpenAICompatible && agent.AuthMethod == models.AuthMethodOAuth {
				agent.APIKey = ""
			} else if apiKey, ok := formValueIfPresent(c, "api_key"); ok && apiKey != "" {
				agent.APIKey = apiKey
			} else if providerOrAuthChanged {
				agent.APIKey = ""
			}

			// Provider/auth changes require reauthorization. Preserve OAuth state only for
			// same-provider OAuth edits such as model settings updates.
			if providerOrAuthChanged {
				if previous.Provider == models.ProviderOpenAICompatible && previous.AuthMethod == models.AuthMethodOAuth {
					agent.CustomAuthConfigJSON = ""
				}
				clearOAuthState(agent)
			}
		},
	}); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if previous.Provider == agent.Provider && previous.AuthMethod == models.AuthMethodOAuth &&
		agent.AuthMethod == models.AuthMethodOAuth && oauthSecurityConfigChanged(previous, *agent) {
		clearOAuthCredentials(agent)
	}

	applog.Infof("[handler] UpdateModel id=%s name=%q model=%s auth_method=%s max_workers=%d", id, agent.Name, agent.Model, agent.AuthMethod, agent.MaxWorkers)
	if err := h.llmConfigRepo.Update(c.Request().Context(), agent); err != nil {
		applog.Infof("[handler] UpdateModel error: %v", err)
		if isModelValidationError(err) {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return err
	}
	applog.Infof("[handler] UpdateModel success id=%s", id)

	// Return updated agents list for HTMX
	if isHTMX(c) {
		return h.renderRefreshedModels(c)
	}
	redirectURL := "/models"
	if projectID := c.QueryParam("project_id"); projectID != "" {
		redirectURL += "?project_id=" + url.QueryEscape(projectID)
	}
	return c.Redirect(http.StatusSeeOther, redirectURL)
}

func (h *Handler) SetDefaultModel(c echo.Context) error {
	id := c.Param("id")
	applog.Infof("[handler] SetDefaultModel id=%s", id)

	agent, err := h.llmConfigRepo.GetByID(c.Request().Context(), id)
	if err != nil {
		applog.Infof("[handler] SetDefaultModel fetch error: %v", err)
		return err
	}
	if agent == nil {
		applog.Infof("[handler] SetDefaultModel not found id=%s", id)
		return echo.NewHTTPError(http.StatusNotFound, "agent not found")
	}

	agent.IsDefault = true
	if err := h.llmConfigRepo.Update(c.Request().Context(), agent); err != nil {
		applog.Infof("[handler] SetDefaultModel update error: %v", err)
		return err
	}
	applog.Infof("[handler] SetDefaultModel success id=%s", id)

	// Return updated agents list for HTMX
	if isHTMX(c) {
		return h.renderRefreshedModels(c)
	}
	return c.Redirect(http.StatusSeeOther, "/models")
}

func (h *Handler) DeleteModel(c echo.Context) error {
	id := c.Param("id")
	applog.Infof("[handler] DeleteModel id=%s", id)
	ctx := c.Request().Context()

	// Fetch agent to check if it exists and if it's the default
	agent, err := h.llmConfigRepo.GetByID(ctx, id)
	if err != nil {
		applog.Infof("[handler] DeleteModel fetch error: %v", err)
		return err
	}
	if agent == nil {
		applog.Infof("[handler] DeleteModel not found id=%s", id)
		return echo.NewHTTPError(http.StatusNotFound, "agent not found")
	}

	if mixtures, err := h.mixturesUsingModel(ctx, id); err != nil {
		return err
	} else if len(mixtures) > 0 {
		msg := fmt.Sprintf("This model is used by %d mixtures: %s. Remove it from those mixtures before deleting.", len(mixtures), strings.Join(mixtures, ", "))
		return echo.NewHTTPError(http.StatusBadRequest, msg)
	}

	if agent.IsDefault {
		// If a new default is provided, validate and apply it before delete.
		// If not provided, repo delete logic will auto-promote another model when available.
		newDefaultID := c.QueryParam("new_default_id")
		if newDefaultID == "" {
			newDefaultID = c.FormValue("new_default_id")
		}
		if newDefaultID != "" {
			// Verify the new default exists and is not the model being deleted.
			newDefault, err := h.llmConfigRepo.GetByID(ctx, newDefaultID)
			if err != nil {
				applog.Infof("[handler] DeleteModel new default fetch error: %v", err)
				return err
			}
			if newDefault == nil || newDefaultID == id {
				applog.Infof("[handler] DeleteModel rejected: invalid new default id=%s", newDefaultID)
				return echo.NewHTTPError(http.StatusBadRequest, "Invalid new default model selection.")
			}
			if err := h.llmConfigRepo.TransferDefaultAndDelete(ctx, id, newDefaultID); err != nil {
				applog.Infof("[handler] DeleteModel transfer+delete error: %v", err)
				return err
			}
			applog.Infof("[handler] DeleteModel success: transferred default to %s, deleted %s", newDefaultID, id)
		} else {
			if err := h.llmConfigRepo.Delete(ctx, id); err != nil {
				applog.Infof("[handler] DeleteModel default delete error: %v", err)
				return err
			}
			applog.Infof("[handler] DeleteModel success: deleted default model id=%s (auto-reassigned when needed)", id)
		}
	} else {
		if err := h.llmConfigRepo.Delete(ctx, id); err != nil {
			applog.Infof("[handler] DeleteModel error: %v", err)
			return err
		}
		applog.Infof("[handler] DeleteModel success id=%s", id)
	}

	// Return updated agents list for HTMX
	if isHTMX(c) {
		return h.renderRefreshedModels(c)
	}
	return c.Redirect(http.StatusSeeOther, "/models")
}

func (h *Handler) renderRefreshedModels(c echo.Context) error {
	agents, err := h.llmConfigRepo.ListCards(c.Request().Context())
	if err != nil {
		return err
	}
	return render(c, http.StatusOK, pages.ModelsContent(agents, h.buildModelWorkerStats(agents), h.desktopMode))
}

// buildModelWorkerStats returns a map of agent config ID -> running worker count.
func (h *Handler) buildModelWorkerStats(agents []models.LLMConfig) map[string]int {
	stats := make(map[string]int)
	for _, agent := range agents {
		stats[agent.ID] = h.workerSvc.ModelRunning(agent.ID)
	}
	return stats
}

func normalizeProviderReasoningEffort(provider models.LLMProvider, model, value string) string {
	switch provider {
	case models.ProviderOpenAI:
		return normalizeOpenAIReasoningEffort(model, value)
	case models.ProviderAnthropic:
		return anthropicclient.NormalizeEffort(model, value)
	case models.ProviderOpenAICompatible:
		if effort := normalizeKimiReasoningEffort(model, value); effort != "" {
			return effort
		}
		return normalizeGLMReasoningEffort(model, value)
	default:
		return ""
	}
}

func normalizeKimiReasoningEffort(model, value string) string {
	if !strings.EqualFold(strings.TrimSpace(model), "kimi-k3") {
		return ""
	}
	switch effort := strings.ToLower(strings.TrimSpace(value)); effort {
	case "low", "high", "max":
		return effort
	default:
		return ""
	}
}

func normalizeGLMReasoningEffort(model, value string) string {
	if !strings.EqualFold(strings.TrimSpace(model), "glm-5.2") {
		return ""
	}
	switch effort := strings.ToLower(strings.TrimSpace(value)); effort {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return effort
	default:
		return ""
	}
}

func normalizeOpenAIReasoningEffort(model, value string) string {
	effort := llmprompt.NormalizeReasoningEffortValue(value)
	if llmprompt.StringInSlice(effort, llmprompt.CodexSupportedReasoningEfforts(model)) {
		return effort
	}
	return ""
}

func formValueIfPresent(c echo.Context, key string) (string, bool) {
	formValues, err := c.FormParams()
	if err != nil {
		return "", false
	}
	values, ok := formValues[key]
	if !ok || len(values) == 0 {
		return "", false
	}
	return values[0], true
}

// ListOpenAICompatibleAvailableModels best-effort probes an OpenAI-compatible /models endpoint.
func (h *Handler) ListOpenAICompatibleAvailableModels(c echo.Context) error {
	var configured *models.LLMConfig
	if id := strings.TrimSpace(c.QueryParam("config_id")); id != "" {
		var err error
		configured, err = h.llmConfigRepo.GetByID(c.Request().Context(), id)
		if err != nil {
			return err
		}
		if configured == nil || configured.Provider != models.ProviderOpenAICompatible {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "custom model configuration not found"})
		}
		if requested := strings.TrimSpace(c.QueryParam("base_url")); requested != "" && requested != strings.TrimSpace(configured.BaseURL) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "save endpoint changes and reconnect before discovering models"})
		}
		if _, supplied := c.QueryParams()["models_url"]; supplied &&
			strings.TrimSpace(c.QueryParam("models_url")) != strings.TrimSpace(configured.ModelsURL) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "save endpoint changes and reconnect before discovering models"})
		}
	}
	baseURL, modelsURL := c.QueryParam("base_url"), c.QueryParam("models_url")
	if configured != nil {
		baseURL, modelsURL = configured.BaseURL, configured.ModelsURL
	}
	urls, err := openAICompatibleModelsURLs(baseURL, modelsURL)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	apiKey := strings.TrimSpace(c.Request().Header.Get(openAICompatibleAPIKeyHeader))
	requestPrivate := c.QueryParam("allow_private") == "1" || strings.EqualFold(c.QueryParam("allow_private"), "true")
	if configured != nil {
		cfg, parseErr := llmcustomauth.ParseConfig(configured.CustomAuthConfigJSON)
		if parseErr != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": parseErr.Error()})
		}
		requestPrivate = cfg.AllowPrivateEndpoints || openAICompatiblePresetUsesPrivateEndpoints(configured.PresetSlug)
		if configured.AuthMethod == models.AuthMethodAPIKey &&
			strings.TrimSpace(c.Request().Header.Get(openAICompatibleAuthHeaderNameHeader)) != "" {
			live := *configured
			live.AuthHeaderName = strings.TrimSpace(c.Request().Header.Get(openAICompatibleAuthHeaderNameHeader))
			live.AuthHeaderValuePrefix = c.Request().Header.Get(openAICompatibleAuthHeaderPrefixHeader)
			if raw := strings.TrimSpace(c.Request().Header.Get(openAICompatibleExtraHeadersHeader)); raw != "" {
				live.ExtraHeadersJSON = raw
			}
			cfg.ModelsArrayPath = strings.TrimSpace(c.Request().Header.Get(openAICompatibleModelsArrayPathHeader))
			cfg.ModelIDField = strings.TrimSpace(c.Request().Header.Get(openAICompatibleModelIDFieldHeader))
			live.CustomAuthConfigJSON = llmcustomauth.MarshalConfig(cfg)
			if err := validateOpenAICompatibleRequestExtras(&live); err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			}
			configured = &live
		}
	} else if strings.TrimSpace(c.Request().Header.Get(openAICompatibleAuthHeaderNameHeader)) != "" {
		cfg := llmcustomauth.Config{
			ModelsArrayPath:       strings.TrimSpace(c.Request().Header.Get(openAICompatibleModelsArrayPathHeader)),
			ModelIDField:          strings.TrimSpace(c.Request().Header.Get(openAICompatibleModelIDFieldHeader)),
			AllowPrivateEndpoints: requestPrivate,
		}
		configured = &models.LLMConfig{
			Provider:              models.ProviderOpenAICompatible,
			AuthMethod:            models.AuthMethodAPIKey,
			APIKey:                apiKey,
			AuthHeaderName:        strings.TrimSpace(c.Request().Header.Get(openAICompatibleAuthHeaderNameHeader)),
			AuthHeaderValuePrefix: c.Request().Header.Get(openAICompatibleAuthHeaderPrefixHeader),
			ExtraHeadersJSON:      strings.TrimSpace(c.Request().Header.Get(openAICompatibleExtraHeadersHeader)),
			CustomAuthConfigJSON:  llmcustomauth.MarshalConfig(cfg),
		}
		if err := validateOpenAICompatibleRequestExtras(configured); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
	}
	client := llmcustomauth.NewHTTPClient(10*time.Second, requestPrivate)
	tried := make([]string, 0, len(urls))
	var lastErr error
	for _, modelsURL := range urls {
		tried = append(tried, modelsURL)
		var modelsFound []openAICompatibleModelInfo
		if configured != nil && configured.AuthMethod == models.AuthMethodOAuth {
			modelsFound, err = h.fetchCustomOpenAICompatibleModels(c.Request().Context(), client, modelsURL, *configured)
			if errors.Is(err, errCustomOAuthUnauthorized) {
				if refreshErr := h.refreshCustomCompatibleOAuth(c.Request().Context(), configured, client, configured.OAuthAccessToken); refreshErr == nil {
					modelsFound, err = h.fetchCustomOpenAICompatibleModels(c.Request().Context(), client, modelsURL, *configured)
				} else {
					err = refreshErr
				}
			}
		} else {
			discoveryKey := apiKey
			if configured != nil && discoveryKey == "" {
				discoveryKey = configured.APIKey
			}
			modelsFound, err = fetchOpenAICompatibleModels(c.Request().Context(), client, modelsURL, discoveryKey, configured)
		}
		if err != nil {
			lastErr = err
			continue
		}
		response := openAICompatibleModelsResponse{Models: modelsFound, TriedURLs: tried}
		if len(modelsFound) == 1 {
			response.ResolvedID = modelsFound[0].ID
		}
		return c.JSON(http.StatusOK, response)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no model discovery URLs were available")
	}
	applog.Infof("[handler] ListOpenAICompatibleAvailableModels error: %v", lastErr)
	return c.JSON(http.StatusBadGateway, map[string]any{"error": lastErr.Error(), "tried_urls": tried})
}

func (h *Handler) refreshCustomCompatibleOAuth(ctx context.Context, agent *models.LLMConfig, client *http.Client, tokenUsed string) error {
	if agent == nil {
		return fmt.Errorf("custom OAuth token cannot be refreshed")
	}
	tokens, err := llmcustomauth.CoordinatedRefreshDistributed(
		ctx,
		string(agent.Provider)+":"+agent.ID,
		h.llmConfigRepo,
		agent.ID,
		func() (llmcustomauth.TokenSet, bool, error) {
			current, loadErr := h.llmConfigRepo.GetByID(ctx, agent.ID)
			if loadErr != nil {
				return llmcustomauth.TokenSet{}, false, loadErr
			}
			if current == nil {
				return llmcustomauth.TokenSet{}, false, fmt.Errorf("custom model configuration not found")
			}
			if tokenUsed != "" && current.OAuthAccessToken != "" && current.OAuthAccessToken != tokenUsed {
				return llmcustomauth.TokenSet{
					AccessToken: current.OAuthAccessToken, RefreshToken: current.OAuthRefreshToken, ExpiresAt: current.OAuthExpiresAt,
				}, true, nil
			}
			return llmcustomauth.TokenSet{}, false, nil
		},
		func() (llmcustomauth.TokenSet, error) {
			current, loadErr := h.llmConfigRepo.GetByID(ctx, agent.ID)
			if loadErr != nil {
				return llmcustomauth.TokenSet{}, loadErr
			}
			if current == nil {
				return llmcustomauth.TokenSet{}, fmt.Errorf("custom model configuration not found")
			}
			if strings.TrimSpace(current.OAuthRefreshToken) == "" {
				return llmcustomauth.TokenSet{}, fmt.Errorf("custom OAuth token cannot be refreshed")
			}
			if tokenUsed != "" && current.OAuthAccessToken != "" && current.OAuthAccessToken != tokenUsed {
				return llmcustomauth.TokenSet{
					AccessToken: current.OAuthAccessToken, RefreshToken: current.OAuthRefreshToken, ExpiresAt: current.OAuthExpiresAt,
				}, nil
			}
			cfg, parseErr := llmcustomauth.ParseConfig(current.CustomAuthConfigJSON)
			if parseErr != nil {
				return llmcustomauth.TokenSet{}, parseErr
			}
			safeClient := llmcustomauth.NewHTTPClient(client.Timeout, cfg.AllowPrivateEndpoints)
			refreshed, refreshErr := llmcustomauth.Refresh(ctx, safeClient, cfg, current.OAuthRefreshToken, llmcustomauth.RefreshOptions{
				ClientID: current.OAuthClientID, ClientSecret: current.OAuthClientSecret,
			})
			if refreshErr != nil {
				return llmcustomauth.TokenSet{}, refreshErr
			}
			updated, persistErr := h.llmConfigRepo.UpdateCustomOAuthTokensIfRevision(
				ctx, current.ID, current.OAuthConfigRevision,
				refreshed.AccessToken, refreshed.RefreshToken, refreshed.ExpiresAt,
			)
			if persistErr != nil {
				return llmcustomauth.TokenSet{}, persistErr
			}
			if !updated {
				return llmcustomauth.TokenSet{}, fmt.Errorf("custom OAuth configuration changed during token refresh")
			}
			return refreshed, nil
		},
	)
	if err != nil {
		return err
	}
	agent.OAuthAccessToken = tokens.AccessToken
	agent.OAuthRefreshToken = tokens.RefreshToken
	agent.OAuthExpiresAt = tokens.ExpiresAt
	return nil
}

func (h *Handler) fetchCustomOpenAICompatibleModels(ctx context.Context, client *http.Client, modelsURL string, agent models.LLMConfig) ([]openAICompatibleModelInfo, error) {
	current, err := h.currentCustomOAuthConfig(ctx, agent)
	if err != nil {
		return nil, err
	}
	agent = *current
	cfg, err := llmcustomauth.ParseConfig(agent.CustomAuthConfigJSON)
	if err != nil {
		return nil, err
	}
	if _, err := llmcustomauth.ValidateEndpoint(modelsURL, cfg.AllowPrivateEndpoints); err != nil {
		return nil, err
	}
	client = llmcustomauth.NewHTTPClient(client.Timeout, cfg.AllowPrivateEndpoints)
	state, err := llmcustomauth.ParseState(agent.CustomAuthStateJSON)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	if err := applyOpenAICompatibleExtraHeaders(req, agent.ExtraHeadersJSON); err != nil {
		return nil, err
	}
	if err := llmcustomauth.PrepareRequest(req, nil, cfg, state, agent.OAuthAccessToken); err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, errCustomOAuthUnauthorized
		}
		return nil, fmt.Errorf("%s returned %d %s", modelsURL, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	var payload any
	if err := llmcustomauth.DecodeMetadataJSON(resp.Body, &payload, "models response"); err != nil {
		return nil, err
	}
	ids := llmcustomauth.ExtractModelIDs(payload, cfg)
	out := make([]openAICompatibleModelInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, openAICompatibleModelInfo{ID: id})
	}
	return out, nil
}

func (h *Handler) currentCustomOAuthConfig(ctx context.Context, snapshot models.LLMConfig) (*models.LLMConfig, error) {
	current, err := h.llmConfigRepo.GetByID(ctx, snapshot.ID)
	if err != nil {
		return nil, err
	}
	if current == nil || current.Provider != models.ProviderOpenAICompatible || current.AuthMethod != models.AuthMethodOAuth {
		return nil, fmt.Errorf("custom OAuth model configuration is no longer available")
	}
	if current.OAuthConfigRevision != snapshot.OAuthConfigRevision {
		return nil, fmt.Errorf("custom OAuth configuration changed before model discovery")
	}
	return current, nil
}

func fetchOpenAICompatibleModels(ctx context.Context, client *http.Client, modelsURL, apiKey string, configured *models.LLMConfig) ([]openAICompatibleModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(apiKey) != "" {
		headerName, prefix := "Authorization", "Bearer "
		if configured != nil {
			headerName = configured.GetAuthHeaderName()
			prefix = configured.GetAuthHeaderValuePrefix()
		}
		req.Header.Set(headerName, prefix+strings.TrimSpace(apiKey))
	}
	if configured != nil {
		if err := applyOpenAICompatibleExtraHeaders(req, configured.ExtraHeadersJSON); err != nil {
			return nil, err
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("%s returned %d %s", modelsURL, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	if configured == nil {
		return decodeOpenAICompatibleModels(resp.Body)
	}
	cfg, err := llmcustomauth.ParseConfig(configured.CustomAuthConfigJSON)
	if err != nil {
		return nil, err
	}
	var payload any
	if err := llmcustomauth.DecodeMetadataJSON(resp.Body, &payload, "models response"); err != nil {
		return nil, err
	}
	ids := llmcustomauth.ExtractModelIDs(payload, cfg)
	out := make([]openAICompatibleModelInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, openAICompatibleModelInfo{ID: id})
	}
	return out, nil
}

func applyOpenAICompatibleExtraHeaders(req *http.Request, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return fmt.Errorf("decode model discovery headers: %w", err)
	}
	cfg := llmcustomauth.Config{StaticHeaders: headers}
	if err := llmcustomauth.ValidateHeaders(cfg); err != nil {
		return err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	return nil
}

func decodeOpenAICompatibleModels(body io.Reader) ([]openAICompatibleModelInfo, error) {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := llmcustomauth.DecodeMetadataJSON(body, &payload, "models response"); err != nil {
		return nil, err
	}
	models := make([]openAICompatibleModelInfo, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		models = append(models, openAICompatibleModelInfo{ID: id})
	}
	return models, nil
}

func (h *Handler) ListOllamaAvailableModels(c echo.Context) error {
	baseURL := c.QueryParam("base_url")
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}

	models, err := service.ListOllamaModels(c.Request().Context(), baseURL)
	if err != nil {
		applog.Infof("[handler] ListOllamaAvailableModels error: %v", err)
		return c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, models)
}

func normalizeOpenAIModel(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	switch trimmed {
	case "gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-5.5",
		"gpt-5.5-pro",
		"gpt-5.4",
		"gpt-5.4-mini",
		"gpt-5.3-codex",
		"gpt-5.3-codex-spark",
		"gpt-5.2-codex",
		"gpt-5.1-codex-max",
		"gpt-5.1-codex",
		"gpt-5.1-codex-mini",
		"gpt-5-codex",
		"gpt-5-codex-mini":
		return trimmed
	default:
		return "gpt-5.6-sol"
	}
}

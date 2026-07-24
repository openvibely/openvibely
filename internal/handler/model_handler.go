package handler

import (
	"context"
	"encoding/json"
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
	llmmixture "github.com/openvibely/openvibely/internal/llm/mixture"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) ListModels(c echo.Context) error {
	isHTMX := isHTMX(c)
	// applog.Debugf("[handler] ListModels requested htmx=%v", isHTMX)
	agents, err := h.llmConfigRepo.List(c.Request().Context())
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
		return render(c, http.StatusOK, pages.ModelsContent(agents, modelWorkerStats))
	}

	currentProjectID, _ := h.getCurrentProjectID(c)
	projects, _ := h.projectSvc.List(c.Request().Context())

	return render(c, http.StatusOK, pages.Models(projects, currentProjectID, agents, modelWorkerStats))
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
	// Default to OAuth (not CLI) when auth_method is absent or unrecognized — the UI
	// no longer exposes CLI as an option for these providers.
	if provider == "anthropic" && (anthropicAuthType == "subscription" || anthropicAuthType == "oauth") {
		am := models.AuthMethod(authMethod)
		if am != models.AuthMethodCLI && am != models.AuthMethodOAuth {
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
	// Default to OAuth (not CLI) when auth_method is absent or unrecognized — the UI
	// no longer exposes CLI as an option for these providers.
	if provider == "openai" && (openaiAuthType == "subscription" || openaiAuthType == "oauth") {
		am := models.AuthMethod(authMethod)
		if am != models.AuthMethodCLI && am != models.AuthMethodOAuth {
			am = models.AuthMethodOAuth
		}
		return models.ProviderOpenAI, am
	}
	if provider == "openai" {
		// Fallback for backwards compatibility
		return models.ProviderOpenAI, models.AuthMethodCLI
	}
	return models.LLMProvider(provider), models.AuthMethodCLI
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
	agent.ExtraHeadersJSON = strings.TrimSpace(c.FormValue("extra_headers_json"))
	agent.ExtraBodyJSON = strings.TrimSpace(c.FormValue("extra_body_json"))
	if maxTokens, err := strconv.Atoi(c.FormValue("default_max_tokens")); err == nil && maxTokens > 0 {
		agent.DefaultMaxTokens = maxTokens
	} else {
		agent.DefaultMaxTokens = 0
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
}

func parseMixtureConfigForm(c echo.Context) (llmmixture.Config, error) {
	raw := strings.TrimSpace(c.FormValue("mixture_config_json"))
	if raw != "" {
		return llmmixture.ParseConfig(raw)
	}
	cfg := llmmixture.Config{
		Enabled:    c.FormValue("mixture_enabled") != "" && c.FormValue("mixture_enabled") != "false" && c.FormValue("mixture_enabled") != "0",
		Aggregator: llmmixture.ModelSlot{AgentConfigID: strings.TrimSpace(c.FormValue("mixture_aggregator_id"))},
	}
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

func (h *Handler) mixturesUsingModel(ctx context.Context, modelID string) ([]string, error) {
	agents, err := h.llmConfigRepo.List(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, agent := range agents {
		if agent.Provider != models.ProviderMixture || strings.TrimSpace(agent.MixtureConfigJSON) == "" {
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

	temp, _ := strconv.ParseFloat(c.FormValue("temperature"), 64)
	isDefault := c.FormValue("is_default") == "on"
	reasoningEffort := c.FormValue("reasoning_effort")

	provider, authMethod := resolveProviderAndAuth(
		c.FormValue("provider"),
		c.FormValue("anthropic_auth_type"),
		c.FormValue("openai_auth_type"),
		c.FormValue("auth_method"),
	)

	modelMaxWorkers, _ := strconv.Atoi(c.FormValue("model_max_workers"))
	if modelMaxWorkers < 0 {
		modelMaxWorkers = 0
	}
	if modelMaxWorkers > 10 {
		modelMaxWorkers = 10
	}
	workerTimeout, _ := strconv.Atoi(c.FormValue("worker_timeout"))
	if workerTimeout < 0 {
		workerTimeout = 0
	}

	model := c.FormValue("model")
	if provider == models.ProviderOpenAI {
		model = normalizeOpenAIModel(model)
	}
	a := &models.LLMConfig{
		Name:            c.FormValue("name"),
		Provider:        provider,
		Model:           model,
		ReasoningEffort: normalizeProviderReasoningEffort(provider, model, reasoningEffort),
		APIKey:          c.FormValue("api_key"),
		Temperature:     temp,
		IsDefault:       isDefault,
		AuthMethod:      authMethod,
		MaxWorkers:      modelMaxWorkers,
		WorkerTimeout:   workerTimeout,
		AutoStartTasks:  c.FormValue("auto_start_tasks") == "on",
	}
	// Store OpenAI OAuth config fields
	if a.Provider == models.ProviderOpenAI && a.AuthMethod == models.AuthMethodOAuth {
		a.OAuthClientID = c.FormValue("oauth_client_id")
		a.OAuthClientSecret = c.FormValue("oauth_client_secret")
		a.OAuthAuthorizeURL = c.FormValue("oauth_authorize_url")
		a.OAuthTokenURL = c.FormValue("oauth_token_url")
		a.OAuthScopes = c.FormValue("oauth_scopes")
	}
	// Store Ollama-specific fields
	if a.Provider == models.ProviderOllama {
		a.OllamaBaseURL = strings.TrimSpace(c.FormValue("ollama_base_url"))
		// Allow custom model names for Ollama
		if customModel := strings.TrimSpace(c.FormValue("ollama_custom_model")); customModel != "" {
			a.Model = customModel
		}
	}
	if a.Provider == models.ProviderOpenAICompatible {
		a.Model = strings.TrimSpace(a.Model)
		if err := applyOpenAICompatibleForm(c, a); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	}
	if a.Provider == models.ProviderMixture {
		if err := h.applyAndValidateMixtureForm(c.Request().Context(), c, a); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	} else {
		a.MixtureConfigJSON = ""
	}
	if a.Provider == "" {
		a.Provider = models.ProviderAnthropic
	}
	applog.Infof("[handler] CreateModel name=%q provider=%s model=%s auth_method=%s temp=%.1f default=%v",
		a.Name, a.Provider, a.Model, a.AuthMethod, a.Temperature, a.IsDefault)

	if err := h.llmConfigRepo.Create(c.Request().Context(), a); err != nil {
		applog.Infof("[handler] CreateModel error: %v", err)
		return err
	}
	applog.Infof("[handler] CreateModel success id=%s", a.ID)

	// Return updated agents list for HTMX
	if isHTMX(c) {
		agents, err := h.llmConfigRepo.List(c.Request().Context())
		if err != nil {
			return err
		}
		return render(c, http.StatusOK, pages.ModelsContent(agents, h.buildModelWorkerStats(agents)))
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

	agent.Name = c.FormValue("name")

	provider, authMethod := resolveProviderAndAuth(
		c.FormValue("provider"),
		c.FormValue("anthropic_auth_type"),
		c.FormValue("openai_auth_type"),
		c.FormValue("auth_method"),
	)
	previousProvider := agent.Provider
	previousAuthMethod := agent.AuthMethod
	agent.Provider = provider
	agent.AuthMethod = authMethod

	agent.Model = c.FormValue("model")
	if agent.Provider == models.ProviderOpenAI {
		agent.Model = normalizeOpenAIModel(agent.Model)
	}
	agent.ReasoningEffort = normalizeProviderReasoningEffort(provider, agent.Model, c.FormValue("reasoning_effort"))
	if agent.Provider == models.ProviderOpenAICompatible {
		agent.Model = strings.TrimSpace(agent.Model)
	}
	if apiKey, ok := formValueIfPresent(c, "api_key"); ok && apiKey != "" {
		agent.APIKey = apiKey
	} else if previousProvider != agent.Provider || previousAuthMethod != agent.AuthMethod {
		agent.APIKey = ""
	}
	if temp, err := strconv.ParseFloat(c.FormValue("temperature"), 64); err == nil {
		agent.Temperature = temp
	}
	agent.IsDefault = c.FormValue("is_default") == "on"
	agent.AutoStartTasks = c.FormValue("auto_start_tasks") == "on"
	// Provider/auth changes require reauthorization. Preserve OAuth state only for
	// same-provider OAuth edits such as model settings updates.
	if previousProvider != agent.Provider || previousAuthMethod != agent.AuthMethod {
		clearOAuthState(agent)
	}
	// Store OpenAI OAuth config fields
	if agent.Provider == models.ProviderOpenAI && agent.AuthMethod == models.AuthMethodOAuth {
		if v, ok := formValueIfPresent(c, "oauth_client_id"); ok {
			agent.OAuthClientID = v
		}
		if v, ok := formValueIfPresent(c, "oauth_client_secret"); ok {
			agent.OAuthClientSecret = v
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
	// Store Ollama-specific fields
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
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	} else {
		clearOpenAICompatibleFields(agent)
	}
	if agent.Provider == models.ProviderMixture {
		agent.OllamaBaseURL = ""
		if err := h.applyAndValidateMixtureForm(c.Request().Context(), c, agent); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	} else {
		agent.MixtureConfigJSON = ""
	}
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

	applog.Infof("[handler] UpdateModel id=%s name=%q model=%s auth_method=%s max_workers=%d", id, agent.Name, agent.Model, agent.AuthMethod, agent.MaxWorkers)
	if err := h.llmConfigRepo.Update(c.Request().Context(), agent); err != nil {
		applog.Infof("[handler] UpdateModel error: %v", err)
		return err
	}
	applog.Infof("[handler] UpdateModel success id=%s", id)

	// Return updated agents list for HTMX
	if isHTMX(c) {
		agents, err := h.llmConfigRepo.List(c.Request().Context())
		if err != nil {
			return err
		}
		return render(c, http.StatusOK, pages.ModelsContent(agents, h.buildModelWorkerStats(agents)))
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
		agents, err := h.llmConfigRepo.List(c.Request().Context())
		if err != nil {
			return err
		}
		return render(c, http.StatusOK, pages.ModelsContent(agents, h.buildModelWorkerStats(agents)))
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
		agents, err := h.llmConfigRepo.List(c.Request().Context())
		if err != nil {
			return err
		}
		return render(c, http.StatusOK, pages.ModelsContent(agents, h.buildModelWorkerStats(agents)))
	}
	return c.Redirect(http.StatusSeeOther, "/models")
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
	urls, err := openAICompatibleModelsURLs(c.QueryParam("base_url"), c.QueryParam("models_url"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	apiKey := strings.TrimSpace(c.Request().Header.Get("X-OpenAI-Compatible-API-Key"))
	client := &http.Client{Timeout: 10 * time.Second}
	tried := make([]string, 0, len(urls))
	var lastErr error
	for _, modelsURL := range urls {
		tried = append(tried, modelsURL)
		models, err := fetchOpenAICompatibleModels(c.Request().Context(), client, modelsURL, apiKey)
		if err != nil {
			lastErr = err
			continue
		}
		response := openAICompatibleModelsResponse{Models: models, TriedURLs: tried}
		if len(models) == 1 {
			response.ResolvedID = models[0].ID
		}
		return c.JSON(http.StatusOK, response)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no model discovery URLs were available")
	}
	applog.Infof("[handler] ListOpenAICompatibleAvailableModels error: %v", lastErr)
	return c.JSON(http.StatusBadGateway, map[string]any{"error": lastErr.Error(), "tried_urls": tried})
}

func fetchOpenAICompatibleModels(ctx context.Context, client *http.Client, modelsURL, apiKey string) ([]openAICompatibleModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
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
	return decodeOpenAICompatibleModels(resp.Body)
}

func decodeOpenAICompatibleModels(body io.Reader) ([]openAICompatibleModelInfo, error) {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode models response: %w", err)
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
	switch strings.TrimSpace(value) {
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
		return strings.TrimSpace(value)
	default:
		return "gpt-5.6-sol"
	}
}

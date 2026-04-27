package handler

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) ListModels(c echo.Context) error {
	isHTMX := isHTMX(c)
	log.Printf("[handler] ListModels requested htmx=%v", isHTMX)
	agents, err := h.llmConfigRepo.List(c.Request().Context())
	if err != nil {
		log.Printf("[handler] ListModels error: %v", err)
		return err
	}
	log.Printf("[handler] ListModels found %d agents", len(agents))

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
	// Accept both "subscription" (legacy) and "oauth" (current) form values
	if provider == "anthropic" && (anthropicAuthType == "subscription" || anthropicAuthType == "oauth") {
		am := models.AuthMethod(authMethod)
		if am != models.AuthMethodCLI && am != models.AuthMethodOAuth {
			am = models.AuthMethodCLI
		}
		return models.ProviderAnthropic, am
	}
	if provider == "anthropic" {
		return models.ProviderAnthropic, models.AuthMethodAPIKey
	}
	if provider == "openai" && openaiAuthType == "api_key" {
		return models.ProviderOpenAI, models.AuthMethodAPIKey
	}
	// Accept both "subscription" (legacy) and "oauth" (current) form values
	if provider == "openai" && (openaiAuthType == "subscription" || openaiAuthType == "oauth") {
		am := models.AuthMethod(authMethod)
		if am != models.AuthMethodCLI && am != models.AuthMethodOAuth {
			am = models.AuthMethodCLI
		}
		return models.ProviderOpenAI, am
	}
	if provider == "openai" {
		// Fallback for backwards compatibility
		return models.ProviderOpenAI, models.AuthMethodCLI
	}
	return models.LLMProvider(provider), models.AuthMethodCLI
}

func (h *Handler) CreateModel(c echo.Context) error {
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

	a := &models.LLMConfig{
		Name:            c.FormValue("name"),
		Provider:        provider,
		Model:           c.FormValue("model"),
		ReasoningEffort: normalizeProviderReasoningEffort(provider, reasoningEffort),
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
	if a.Provider == "" {
		a.Provider = models.ProviderAnthropic
	}
	if a.Provider == models.ProviderOpenAI {
		a.Model = normalizeOpenAIModel(a.Model)
	}
	log.Printf("[handler] CreateModel name=%q provider=%s model=%s auth_method=%s temp=%.1f default=%v",
		a.Name, a.Provider, a.Model, a.AuthMethod, a.Temperature, a.IsDefault)

	if err := h.llmConfigRepo.Create(c.Request().Context(), a); err != nil {
		log.Printf("[handler] CreateModel error: %v", err)
		return err
	}
	log.Printf("[handler] CreateModel success id=%s", a.ID)

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

func (h *Handler) UpdateModel(c echo.Context) error {
	id := c.Param("id")
	log.Printf("[handler] UpdateModel id=%s", id)

	agent, err := h.llmConfigRepo.GetByID(c.Request().Context(), id)
	if err != nil {
		log.Printf("[handler] UpdateModel fetch error: %v", err)
		return err
	}
	if agent == nil {
		log.Printf("[handler] UpdateModel not found id=%s", id)
		return echo.NewHTTPError(http.StatusNotFound, "agent not found")
	}

	agent.Name = c.FormValue("name")

	provider, authMethod := resolveProviderAndAuth(
		c.FormValue("provider"),
		c.FormValue("anthropic_auth_type"),
		c.FormValue("openai_auth_type"),
		c.FormValue("auth_method"),
	)
	agent.Provider = provider
	agent.AuthMethod = authMethod

	agent.Model = c.FormValue("model")
	agent.ReasoningEffort = normalizeProviderReasoningEffort(provider, c.FormValue("reasoning_effort"))
	if agent.Provider == models.ProviderOpenAI {
		agent.Model = normalizeOpenAIModel(agent.Model)
	}
	if apiKey := c.FormValue("api_key"); apiKey != "" {
		agent.APIKey = apiKey
	}
	if temp, err := strconv.ParseFloat(c.FormValue("temperature"), 64); err == nil {
		agent.Temperature = temp
	}
	agent.IsDefault = c.FormValue("is_default") == "on"
	agent.AutoStartTasks = c.FormValue("auto_start_tasks") == "on"
	// If switching away from OAuth, clear tokens
	if agent.AuthMethod != models.AuthMethodOAuth {
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

	log.Printf("[handler] UpdateModel id=%s name=%q model=%s auth_method=%s max_workers=%d", id, agent.Name, agent.Model, agent.AuthMethod, agent.MaxWorkers)
	if err := h.llmConfigRepo.Update(c.Request().Context(), agent); err != nil {
		log.Printf("[handler] UpdateModel error: %v", err)
		return err
	}
	log.Printf("[handler] UpdateModel success id=%s", id)

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

func (h *Handler) SetDefaultModel(c echo.Context) error {
	id := c.Param("id")
	log.Printf("[handler] SetDefaultModel id=%s", id)

	agent, err := h.llmConfigRepo.GetByID(c.Request().Context(), id)
	if err != nil {
		log.Printf("[handler] SetDefaultModel fetch error: %v", err)
		return err
	}
	if agent == nil {
		log.Printf("[handler] SetDefaultModel not found id=%s", id)
		return echo.NewHTTPError(http.StatusNotFound, "agent not found")
	}

	agent.IsDefault = true
	if err := h.llmConfigRepo.Update(c.Request().Context(), agent); err != nil {
		log.Printf("[handler] SetDefaultModel update error: %v", err)
		return err
	}
	log.Printf("[handler] SetDefaultModel success id=%s", id)

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
	log.Printf("[handler] DeleteModel id=%s", id)
	ctx := c.Request().Context()

	// Fetch agent to check if it exists and if it's the default
	agent, err := h.llmConfigRepo.GetByID(ctx, id)
	if err != nil {
		log.Printf("[handler] DeleteModel fetch error: %v", err)
		return err
	}
	if agent == nil {
		log.Printf("[handler] DeleteModel not found id=%s", id)
		return echo.NewHTTPError(http.StatusNotFound, "agent not found")
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
				log.Printf("[handler] DeleteModel new default fetch error: %v", err)
				return err
			}
			if newDefault == nil || newDefaultID == id {
				log.Printf("[handler] DeleteModel rejected: invalid new default id=%s", newDefaultID)
				return echo.NewHTTPError(http.StatusBadRequest, "Invalid new default model selection.")
			}
			if err := h.llmConfigRepo.TransferDefaultAndDelete(ctx, id, newDefaultID); err != nil {
				log.Printf("[handler] DeleteModel transfer+delete error: %v", err)
				return err
			}
			log.Printf("[handler] DeleteModel success: transferred default to %s, deleted %s", newDefaultID, id)
		} else {
			if err := h.llmConfigRepo.Delete(ctx, id); err != nil {
				log.Printf("[handler] DeleteModel default delete error: %v", err)
				return err
			}
			log.Printf("[handler] DeleteModel success: deleted default model id=%s (auto-reassigned when needed)", id)
		}
	} else {
		if err := h.llmConfigRepo.Delete(ctx, id); err != nil {
			log.Printf("[handler] DeleteModel error: %v", err)
			return err
		}
		log.Printf("[handler] DeleteModel success id=%s", id)
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

func normalizeProviderReasoningEffort(provider models.LLMProvider, value string) string {
	switch provider {
	case models.ProviderOpenAI:
		return normalizeOpenAIReasoningEffort(value)
	case models.ProviderAnthropic:
		return normalizeAnthropicEffort(value)
	default:
		return ""
	}
}

func normalizeOpenAIReasoningEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeAnthropicEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low", "medium", "high", "max":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
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

// ListOllamaAvailableModels queries an Ollama instance for installed models.
func (h *Handler) ListOllamaAvailableModels(c echo.Context) error {
	baseURL := c.QueryParam("base_url")
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}

	models, err := service.ListOllamaModels(c.Request().Context(), baseURL)
	if err != nil {
		log.Printf("[handler] ListOllamaAvailableModels error: %v", err)
		return c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, models)
}

func normalizeOpenAIModel(value string) string {
	switch strings.TrimSpace(value) {
	case "gpt-5.5",
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
		return "gpt-5.5"
	}
}

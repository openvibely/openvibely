package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

const maxWebhookBodySize = 1 << 20 // 1 MB

// --- Inbound webhook endpoint ---

func (h *Handler) HandleWebhookInbound(c echo.Context) error {
	pathToken := c.Param("pathToken")
	if pathToken == "" {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}

	if h.webhookRepo == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	if h.taskRepo == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}

	endpoint, err := h.webhookRepo.GetByPathToken(c.Request().Context(), pathToken)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
	if endpoint == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
	}
	if !endpoint.Enabled {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "endpoint disabled"})
	}

	// Read body with size limit
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, maxWebhookBodySize+1))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "failed to read body"})
	}
	if len(body) > maxWebhookBodySize {
		return c.JSON(http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
	}

	// Authenticate with secret if configured
	if endpoint.Secret != "" {
		if !verifyWebhookAuth(c.Request(), endpoint.Secret, body) {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		}
	}

	// Parse JSON body
	var payload map[string]interface{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		}
	}
	if payload == nil {
		payload = make(map[string]interface{})
	}

	// Normalize some generic fields
	eventType := extractStringField(payload, "event_type", "type", "action", "event")
	summary := extractStringField(payload, "summary", "description", "message", "text", "title")

	// Build task title
	title := buildWebhookTaskTitle(endpoint, eventType, summary)

	// Build task prompt with embedded payload
	prompt := buildWebhookTaskPrompt(endpoint, eventType, summary, string(body))

	// Get assigned agents
	agentIDs := []string{}
	agents, err := h.webhookRepo.GetEndpointAgents(c.Request().Context(), endpoint.ID)
	if err == nil {
		for _, a := range agents {
			agentIDs = append(agentIDs, a.AgentDefinitionID)
		}
	}

	// Create exactly one task
	task := &models.Task{
		ProjectID:  endpoint.ProjectID,
		Title:      title,
		Category:   models.CategoryActive,
		Priority:   endpoint.DefaultPriority,
		Status:     models.StatusPending,
		Prompt:     prompt,
		CreatedVia: models.TaskOriginWebhook,
	}

	// Set primary agent (first selected agent)
	if len(agentIDs) > 0 {
		task.AgentDefinitionID = &agentIDs[0]
	}

	if err := h.taskRepo.Create(c.Request().Context(), task); err != nil {
		if errors.Is(err, repository.ErrDuplicateTask) {
			baseTitle := task.Title
			for i := 2; i <= 100; i++ {
				task.Title = fmt.Sprintf("%s (%d)", baseTitle, i)
				if retryErr := h.taskRepo.Create(c.Request().Context(), task); retryErr == nil {
					err = nil
					break
				} else if !errors.Is(retryErr, repository.ErrDuplicateTask) {
					err = retryErr
					break
				}
			}
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to create task"})
			}
		} else {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to create task"})
		}
	}

	// Persist full agent assignment list for future multi-agent support
	if len(agentIDs) > 0 {
		_ = h.webhookRepo.SetTaskAgentAssignments(c.Request().Context(), task.ID, agentIDs)
	}

	// Submit task to worker for execution
	if h.workerSvc != nil {
		h.workerSvc.Submit(*task)
	}

	return c.JSON(http.StatusAccepted, map[string]interface{}{
		"task_id":    task.ID,
		"title":      task.Title,
		"category":   task.Category,
		"priority":   task.Priority,
		"status":     task.Status,
		"created_via": task.CreatedVia,
	})
}

// verifyWebhookAuth checks the webhook secret using constant-time comparison.
// Supports X-Webhook-Secret header (direct comparison) and X-Hub-Signature-256 (HMAC).
func verifyWebhookAuth(req *http.Request, secret string, body []byte) bool {
	// Check direct secret header first
	headerSecret := req.Header.Get("X-Webhook-Secret")
	if headerSecret != "" {
		return subtle.ConstantTimeCompare([]byte(headerSecret), []byte(secret)) == 1
	}

	// Check HMAC signature (GitHub-style)
	sig := req.Header.Get("X-Hub-Signature-256")
	if sig != "" {
		sig = strings.TrimPrefix(sig, "sha256=")
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		expected := hex.EncodeToString(mac.Sum(nil))
		return subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) == 1
	}

	// No auth header provided
	return false
}

func extractStringField(payload map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if v, ok := payload[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

func buildWebhookTaskTitle(endpoint *models.WebhookEndpoint, eventType, summary string) string {
	if endpoint.TitleTemplate != "" {
		title := endpoint.TitleTemplate
		title = strings.ReplaceAll(title, "{{event_type}}", eventType)
		title = strings.ReplaceAll(title, "{{summary}}", summary)
		title = strings.ReplaceAll(title, "{{name}}", endpoint.Name)
		if title != "" {
			return title
		}
	}

	// Default title generation
	parts := []string{"Webhook"}
	if endpoint.Name != "" {
		parts = []string{endpoint.Name}
	}
	if eventType != "" {
		parts = append(parts, eventType)
	}
	if summary != "" {
		s := summary
		if len(s) > 80 {
			s = s[:80] + "..."
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ": ")
}

func buildWebhookTaskPrompt(endpoint *models.WebhookEndpoint, eventType, summary, rawJSON string) string {
	var sb strings.Builder

	// System instructions
	if endpoint.SystemInstructions != "" {
		sb.WriteString(endpoint.SystemInstructions)
		sb.WriteString("\n\n")
	}

	// Prompt template or default
	if endpoint.PromptTemplate != "" {
		prompt := endpoint.PromptTemplate
		prompt = strings.ReplaceAll(prompt, "{{event_type}}", eventType)
		prompt = strings.ReplaceAll(prompt, "{{summary}}", summary)
		prompt = strings.ReplaceAll(prompt, "{{name}}", endpoint.Name)
		sb.WriteString(prompt)
		sb.WriteString("\n\n")
	} else {
		sb.WriteString("An inbound webhook event was received. Process the following payload and take appropriate action.\n\n")
		if eventType != "" {
			sb.WriteString(fmt.Sprintf("Event Type: %s\n", eventType))
		}
		if summary != "" {
			sb.WriteString(fmt.Sprintf("Summary: %s\n", summary))
		}
		sb.WriteString("\n")
	}

	// Always embed raw JSON payload
	sb.WriteString("--- Webhook Payload (Raw JSON) ---\n```json\n")
	// Pretty-print if possible
	var prettyJSON json.RawMessage
	if err := json.Unmarshal([]byte(rawJSON), &prettyJSON); err == nil {
		if pretty, err := json.MarshalIndent(prettyJSON, "", "  "); err == nil {
			sb.Write(pretty)
		} else {
			sb.WriteString(rawJSON)
		}
	} else {
		sb.WriteString(rawJSON)
	}
	sb.WriteString("\n```\n")

	return sb.String()
}

// --- CRUD handlers ---

func (h *Handler) HandleWebhookCreate(c echo.Context) error {
	if h.webhookRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "webhook repository not configured")
	}

	projectID := strings.TrimSpace(c.FormValue("project_id"))
	if projectID == "" {
		projectID = strings.TrimSpace(c.QueryParam("project_id"))
	}
	if projectID == "" {
		var err error
		projectID, err = h.getCurrentProjectID(c)
		if err != nil || projectID == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "project not found")
		}
	}
	if h.projectSvc != nil {
		p, err := h.projectSvc.GetByID(c.Request().Context(), projectID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to resolve project")
		}
		if p == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "project not found")
		}
	}

	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		name = "New Webhook"
	}

	w := &models.WebhookEndpoint{
		ProjectID:          projectID,
		Name:               name,
		Enabled:            true,
		SystemInstructions: strings.TrimSpace(c.FormValue("system_instructions")),
		TitleTemplate:      strings.TrimSpace(c.FormValue("title_template")),
		PromptTemplate:     strings.TrimSpace(c.FormValue("prompt_template")),
		DefaultPriority:    parseIntClamped(c.FormValue("default_priority"), 0, 4),
	}

	if err := h.webhookRepo.Create(c.Request().Context(), w); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create webhook: "+err.Error())
	}

	// Save agent assignments
	agentIDs := parseAgentIDList(c.FormValue("agent_ids"))
	if len(agentIDs) > 0 {
		_ = h.webhookRepo.SetEndpointAgents(c.Request().Context(), w.ID, agentIDs)
	}

	if isHTMX(c) {
		c.Response().Header().Set("HX-Refresh", "true")
		return c.NoContent(http.StatusOK)
	}
	return c.JSON(http.StatusCreated, w)
}

func (h *Handler) HandleWebhookUpdate(c echo.Context) error {
	if h.webhookRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "webhook repository not configured")
	}

	id := c.Param("id")
	w, err := h.webhookRepo.GetByID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load webhook")
	}
	if w == nil {
		return echo.NewHTTPError(http.StatusNotFound, "webhook not found")
	}

	name := strings.TrimSpace(c.FormValue("name"))
	if name != "" {
		w.Name = name
	}
	w.Enabled = c.FormValue("enabled") == "true" || c.FormValue("enabled") == "1" || c.FormValue("enabled") == "on"
	w.SystemInstructions = strings.TrimSpace(c.FormValue("system_instructions"))
	w.TitleTemplate = strings.TrimSpace(c.FormValue("title_template"))
	w.PromptTemplate = strings.TrimSpace(c.FormValue("prompt_template"))
	w.DefaultPriority = parseIntClamped(c.FormValue("default_priority"), 0, 4)

	if err := h.webhookRepo.Update(c.Request().Context(), w); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update webhook")
	}

	// Update agent assignments
	agentIDs := parseAgentIDList(c.FormValue("agent_ids"))
	_ = h.webhookRepo.SetEndpointAgents(c.Request().Context(), w.ID, agentIDs)

	if isHTMX(c) {
		c.Response().Header().Set("HX-Refresh", "true")
		return c.NoContent(http.StatusOK)
	}
	return c.JSON(http.StatusOK, w)
}

func (h *Handler) HandleWebhookDelete(c echo.Context) error {
	if h.webhookRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "webhook repository not configured")
	}

	id := c.Param("id")
	if err := h.webhookRepo.Delete(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete webhook")
	}

	if isHTMX(c) {
		c.Response().Header().Set("HX-Refresh", "true")
		return c.NoContent(http.StatusOK)
	}
	return c.NoContent(http.StatusOK)
}

func (h *Handler) HandleWebhookRotateSecret(c echo.Context) error {
	if h.webhookRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "webhook repository not configured")
	}

	id := c.Param("id")
	newSecret, err := h.webhookRepo.RotateSecret(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to rotate secret")
	}

	if isHTMX(c) {
		c.Response().Header().Set("HX-Refresh", "true")
		return c.NoContent(http.StatusOK)
	}
	return c.JSON(http.StatusOK, map[string]string{"secret": newSecret})
}

func (h *Handler) HandleWebhookTest(c echo.Context) error {
	if h.webhookRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "webhook repository not configured")
	}

	id := c.Param("id")
	endpoint, err := h.webhookRepo.GetByID(c.Request().Context(), id)
	if err != nil || endpoint == nil {
		return echo.NewHTTPError(http.StatusNotFound, "webhook not found")
	}

	// Create a synthetic test task
	testPayload := `{"event_type":"test","summary":"Test webhook event","source":"openvibely_test"}`
	var payload map[string]interface{}
	_ = json.Unmarshal([]byte(testPayload), &payload)

	title := buildWebhookTaskTitle(endpoint, "test", "Test webhook event")
	prompt := buildWebhookTaskPrompt(endpoint, "test", "Test webhook event", testPayload)

	agentIDs := []string{}
	agents, _ := h.webhookRepo.GetEndpointAgents(c.Request().Context(), endpoint.ID)
	for _, a := range agents {
		agentIDs = append(agentIDs, a.AgentDefinitionID)
	}

	task := &models.Task{
		ProjectID:  endpoint.ProjectID,
		Title:      title,
		Category:   models.CategoryActive,
		Priority:   endpoint.DefaultPriority,
		Status:     models.StatusPending,
		Prompt:     prompt,
		CreatedVia: models.TaskOriginWebhook,
	}
	if len(agentIDs) > 0 {
		task.AgentDefinitionID = &agentIDs[0]
	}

	if err := h.taskRepo.Create(c.Request().Context(), task); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create test task: "+err.Error())
	}

	if len(agentIDs) > 0 {
		_ = h.webhookRepo.SetTaskAgentAssignments(c.Request().Context(), task.ID, agentIDs)
	}

	if h.workerSvc != nil {
		h.workerSvc.Submit(*task)
	}

	if isHTMX(c) {
		return c.HTML(http.StatusOK, `<div class="flex items-center gap-2 text-success"><span>Test task created!</span></div>`)
	}
	return c.JSON(http.StatusAccepted, map[string]string{"task_id": task.ID})
}

func parseAgentIDList(val string) []string {
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

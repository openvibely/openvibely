package handler

import (
	"context"
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

	task, err := h.createWebhookTaskFromEndpoint(c.Request().Context(), endpoint, eventType, summary, string(body))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to create task"})
	}

	return c.JSON(http.StatusAccepted, map[string]interface{}{
		"task_id":     task.ID,
		"title":       task.Title,
		"category":    task.Category,
		"priority":    task.Priority,
		"status":      task.Status,
		"created_via": task.CreatedVia,
	})
}

// verifyWebhookAuth checks the webhook secret using constant-time comparison.
// Supports X-Webhook-Secret header (direct comparison) and X-Hub-Signature-256 (HMAC).
// webhookTaskPriority maps a stored webhook default priority onto the canonical
// task priority scale (1=Low, 2=Normal, 3=High, 4=Urgent). Legacy endpoints
// persisted before the priority scale was corrected may still store 0, which
// on the canonical scale is treated as no badge and sorts last; remap that
// legacy value to Normal (2) instead of silently producing a badge-less,
// bottom-sorted task.
func webhookTaskPriority(defaultPriority int) int {
	if defaultPriority < 1 || defaultPriority > 4 {
		return 2
	}
	return defaultPriority
}

func (h *Handler) validateWebhookAgentIDs(ctx context.Context, projectID string, agentIDs []string) error {
	if len(agentIDs) == 0 {
		return nil
	}
	for _, agentID := range agentIDs {
		agentID = strings.TrimSpace(agentID)
		if agentID == "" {
			return fmt.Errorf("webhook agent ID is empty")
		}
		if _, err := h.resolvePrimaryAgentDefinition(ctx, projectID, agentID); err != nil {
			return fmt.Errorf("webhook agent %q is unavailable: %w", agentID, err)
		}
	}
	return nil
}

func (h *Handler) createWebhookTaskFromEndpoint(ctx context.Context, endpoint *models.WebhookEndpoint, eventType, summary, rawJSON string) (*models.Task, error) {
	if h.taskRepo == nil {
		return nil, fmt.Errorf("task repository not configured")
	}
	if h.webhookRepo == nil {
		return nil, fmt.Errorf("webhook repository not configured")
	}

	task := &models.Task{
		ProjectID:  endpoint.ProjectID,
		Title:      buildWebhookTaskTitle(endpoint, eventType, summary),
		Category:   models.CategoryActive,
		Priority:   webhookTaskPriority(endpoint.DefaultPriority),
		Status:     models.StatusPending,
		Prompt:     buildWebhookTaskPrompt(endpoint, eventType, summary, rawJSON),
		CreatedVia: models.TaskOriginWebhook,
	}

	assignments, err := h.webhookRepo.GetEndpointAgents(ctx, endpoint.ID)
	if err != nil {
		return nil, fmt.Errorf("getting webhook agent assignments: %w", err)
	}
	agentIDs := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		agentIDs = append(agentIDs, assignment.AgentDefinitionID)
	}
	if err := h.validateWebhookAgentIDs(ctx, endpoint.ProjectID, agentIDs); err != nil {
		return nil, fmt.Errorf("validating webhook agent assignments: %w", err)
	}
	if len(agentIDs) > 0 {
		task.AgentDefinitionID = &agentIDs[0]
	}

	if err := h.createWebhookTaskWithUniqueTitle(ctx, task); err != nil {
		return nil, err
	}
	if len(agentIDs) > 0 {
		if err := h.webhookRepo.SetTaskAgentAssignments(ctx, task.ID, agentIDs); err != nil {
			return nil, fmt.Errorf("saving task agent assignments: %w", err)
		}
	}
	if h.workerSvc != nil {
		h.workerSvc.Submit(*task)
	}
	return task, nil
}

func (h *Handler) createWebhookTaskWithUniqueTitle(ctx context.Context, task *models.Task) error {
	if err := h.taskRepo.Create(ctx, task); err != nil {
		if !errors.Is(err, repository.ErrDuplicateTask) {
			return err
		}
		baseTitle := task.Title
		for i := 2; i <= 100; i++ {
			task.Title = fmt.Sprintf("%s (%d)", baseTitle, i)
			if retryErr := h.taskRepo.Create(ctx, task); retryErr == nil {
				return nil
			} else if !errors.Is(retryErr, repository.ErrDuplicateTask) {
				return retryErr
			}
		}
		return err
	}
	return nil
}

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

type webhookDetailResponse struct {
	ID                 string   `json:"id"`
	ProjectID          string   `json:"project_id"`
	Name               string   `json:"name"`
	Enabled            bool     `json:"enabled"`
	PathToken          string   `json:"path_token"`
	Secret             string   `json:"secret"`
	SystemInstructions string   `json:"system_instructions"`
	TitleTemplate      string   `json:"title_template"`
	PromptTemplate     string   `json:"prompt_template"`
	DefaultPriority    int      `json:"default_priority"`
	AgentIDs           []string `json:"agent_ids"`
}

func (h *Handler) HandleWebhookDetail(c echo.Context) error {
	if h.webhookRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "webhook repository not configured")
	}

	projectID := strings.TrimSpace(c.QueryParam("project_id"))
	if projectID == "" {
		var err error
		projectID, err = h.getCurrentProjectID(c)
		if err != nil || projectID == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "project not found")
		}
	}

	w, err := h.webhookRepo.GetByIDForProject(c.Request().Context(), c.Param("id"), projectID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load webhook")
	}
	if w == nil {
		return echo.NewHTTPError(http.StatusNotFound, "webhook not found")
	}

	agentAssignments, err := h.webhookRepo.GetEndpointAgents(c.Request().Context(), w.ID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load webhook agents")
	}
	agentIDs := make([]string, 0, len(agentAssignments))
	for _, assignment := range agentAssignments {
		agentIDs = append(agentIDs, assignment.AgentDefinitionID)
	}

	return c.JSON(http.StatusOK, webhookDetailResponse{
		ID:                 w.ID,
		ProjectID:          w.ProjectID,
		Name:               w.Name,
		Enabled:            w.Enabled,
		PathToken:          w.PathToken,
		Secret:             w.Secret,
		SystemInstructions: w.SystemInstructions,
		TitleTemplate:      w.TitleTemplate,
		PromptTemplate:     w.PromptTemplate,
		DefaultPriority:    w.DefaultPriority,
		AgentIDs:           agentIDs,
	})
}

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

	form := parseWebhookEndpointForm(c)
	if err := h.validateWebhookAgentIDs(c.Request().Context(), projectID, form.AgentIDs); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid webhook agent assignment")
	}
	w := &models.WebhookEndpoint{ProjectID: projectID}
	applyWebhookEndpointForm(w, form)
	if w.Name == "" {
		w.Name = "New Webhook"
	}

	if err := h.webhookRepo.Create(c.Request().Context(), w); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create webhook: "+err.Error())
	}

	// Save agent assignments
	if len(form.AgentIDs) > 0 {
		if err := h.webhookRepo.SetEndpointAgents(c.Request().Context(), w.ID, form.AgentIDs); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to save webhook agents: "+err.Error())
		}
	}

	if isHTMX(c) {
		return triggerChannelsRefresh(c)
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

	form := parseWebhookEndpointForm(c)
	if err := h.validateWebhookAgentIDs(c.Request().Context(), w.ProjectID, form.AgentIDs); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid webhook agent assignment")
	}
	applyWebhookEndpointForm(w, form)

	if err := h.webhookRepo.Update(c.Request().Context(), w); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update webhook")
	}

	// Update agent assignments
	if err := h.webhookRepo.SetEndpointAgents(c.Request().Context(), w.ID, form.AgentIDs); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save webhook agents: "+err.Error())
	}

	if isHTMX(c) {
		return triggerChannelsRefresh(c)
	}
	return c.JSON(http.StatusOK, w)
}

func (h *Handler) HandleWebhookDelete(c echo.Context) error {
	if h.webhookRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "webhook repository not configured")
	}

	id := c.Param("id")
	if err := h.webhookRepo.Delete(c.Request().Context(), id); err != nil {
		if errors.Is(err, repository.ErrWebhookNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "webhook not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete webhook")
	}

	if isHTMX(c) {
		return triggerChannelsRefresh(c)
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
		if errors.Is(err, repository.ErrWebhookNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "webhook not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to rotate secret")
	}

	if isHTMX(c) {
		return triggerChannelsRefresh(c)
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
	task, err := h.createWebhookTaskFromEndpoint(c.Request().Context(), endpoint, "test", "Test webhook event", testPayload)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create test task: "+err.Error())
	}

	if isHTMX(c) {
		return c.HTML(http.StatusOK, `<div class="flex items-center gap-2 text-success"><span>Test task created!</span></div>`)
	}
	return c.JSON(http.StatusAccepted, map[string]string{"task_id": task.ID})
}

type webhookEndpointForm struct {
	Name               string
	Enabled            bool
	SystemInstructions string
	TitleTemplate      string
	PromptTemplate     string
	DefaultPriority    int
	AgentIDs           []string
}

func parseWebhookEndpointForm(c echo.Context) webhookEndpointForm {
	return webhookEndpointForm{
		Name:               strings.TrimSpace(c.FormValue("name")),
		Enabled:            c.FormValue("enabled") == "true" || c.FormValue("enabled") == "1" || c.FormValue("enabled") == "on",
		SystemInstructions: strings.TrimSpace(c.FormValue("system_instructions")),
		TitleTemplate:      strings.TrimSpace(c.FormValue("title_template")),
		PromptTemplate:     strings.TrimSpace(c.FormValue("prompt_template")),
		DefaultPriority:    parseIntClamped(c.FormValue("default_priority"), 1, 4),
		AgentIDs:           parseWebhookAgentIDs(c),
	}
}

func applyWebhookEndpointForm(w *models.WebhookEndpoint, form webhookEndpointForm) {
	if form.Name != "" {
		w.Name = form.Name
	}
	w.Enabled = form.Enabled
	w.SystemInstructions = form.SystemInstructions
	w.TitleTemplate = form.TitleTemplate
	w.PromptTemplate = form.PromptTemplate
	w.DefaultPriority = form.DefaultPriority
}

func parseWebhookAgentIDs(c echo.Context) []string {
	params, err := c.FormParams()
	if err != nil {
		return parseAgentIDList(c.FormValue("agent_ids"))
	}
	values := params["agent_ids"]
	if len(values) == 0 {
		return parseAgentIDList(c.FormValue("agent_ids"))
	}
	var result []string
	seen := map[string]struct{}{}
	for _, raw := range values {
		for _, id := range parseAgentIDList(raw) {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			result = append(result, id)
		}
	}
	return result
}

func parseAgentIDList(val string) []string {
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

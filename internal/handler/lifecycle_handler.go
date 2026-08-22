package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/lifecycle"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/viewmodels"
)

// GetAgentLifecycleHooks returns the lifecycle hooks configured on an agent
// as JSON so the agent edit dialog (Lifecycle Hooks tab) can hydrate the form.
// Runbook §Agent Create/Edit Dialog → Lifecycle Hooks Tab (lines 2203-2246).
func (h *Handler) GetAgentLifecycleHooks(c echo.Context) error {
	if h.lifecycleRepo == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "lifecycle repo not configured")
	}
	agentID := strings.TrimSpace(c.Param("id"))
	if agentID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "agent id is required")
	}
	hooks, err := h.lifecycleRepo.HooksByAgent(c.Request().Context(), agentID)
	if err != nil {
		applog.Infof("[handler] GetAgentLifecycleHooks agent=%s error: %v", agentID, err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, hooks)
}

// hookSavePayload is the wire format sent by the dialog's Lifecycle Hooks tab.
type hookSavePayload struct {
	ID             string `json:"id,omitempty"`
	When           string `json:"when"`
	SkillKey       string `json:"skill_key"`
	Blocking       bool   `json:"blocking"`
	Enabled        bool   `json:"enabled"`
	PromptOverride string `json:"prompt_override,omitempty"`
	RunPolicyJSON  string `json:"run_policy_json,omitempty"`
	ScheduleJSON   string `json:"schedule_json,omitempty"`
	OutputContract string `json:"output_contract,omitempty"`
}

// SaveAgentLifecycleHooks accepts the full set of hooks for an agent and
// reconciles them against the database: existing hooks not in the payload are
// removed (unless protected by the agent's generated_status), and new entries
// are inserted. The runbook treats hooks as a flat set keyed by (when_slot,
// skill_key) per agent — duplicates are rejected by the validator.
func (h *Handler) SaveAgentLifecycleHooks(c echo.Context) error {
	if h.lifecycleRepo == nil || h.agentRepo == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "lifecycle repo not configured")
	}
	agentID := strings.TrimSpace(c.Param("id"))
	if agentID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "agent id is required")
	}
	agent, err := h.agentRepo.GetByID(c.Request().Context(), agentID)
	if err != nil || agent == nil {
		return echo.NewHTTPError(http.StatusNotFound, "agent not found")
	}
	if agent.GeneratedStatus == models.AgentStatusProtected {
		return echo.NewHTTPError(http.StatusForbidden, "lifecycle hooks for protected built-in agents cannot be modified through the dialog")
	}

	var payloads []hookSavePayload
	if err := json.NewDecoder(c.Request().Body).Decode(&payloads); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid payload: "+err.Error())
	}
	if err := h.reconcileAgentLifecycleHooks(c.Request().Context(), agentID, payloads); err != nil {
		return err
	}

	hooks, _ := h.lifecycleRepo.HooksByAgent(c.Request().Context(), agentID)
	return c.JSON(http.StatusOK, hooks)
}

func (h *Handler) saveAgentLifecycleHooksFromForm(c echo.Context, agentID string) error {
	if h.lifecycleRepo == nil {
		return nil
	}
	raw := strings.TrimSpace(c.FormValue("lifecycle_hooks_json"))
	if raw == "" {
		return nil
	}
	var payloads []hookSavePayload
	if err := json.Unmarshal([]byte(raw), &payloads); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid lifecycle_hooks_json: "+err.Error())
	}
	return h.reconcileAgentLifecycleHooks(c.Request().Context(), agentID, payloads)
}

func (h *Handler) reconcileAgentLifecycleHooks(ctx context.Context, agentID string, payloads []hookSavePayload) error {
	// Validate every entry first so we never partially mutate.
	seen := map[string]struct{}{}
	for i, p := range payloads {
		when := strings.TrimSpace(p.When)
		skill := strings.TrimSpace(p.SkillKey)
		if when == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "hook is missing when")
		}
		if !isValidLifecycleWhen(when) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid when value: "+when)
		}
		if skill == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "hook is missing skill_key")
		}
		if p.OutputContract != "" && !isValidLifecycleOutputContract(p.OutputContract) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid output_contract: "+p.OutputContract)
		}
		p.ScheduleJSON = strings.TrimSpace(p.ScheduleJSON)
		if p.ScheduleJSON != "" && !json.Valid([]byte(p.ScheduleJSON)) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid schedule_json for "+skill)
		}
		key := when + "/" + skill
		if _, dup := seen[key]; dup {
			return echo.NewHTTPError(http.StatusBadRequest, "duplicate hook for "+key)
		}
		seen[key] = struct{}{}
		payloads[i].When = when
		payloads[i].SkillKey = skill
		payloads[i].OutputContract = strings.TrimSpace(p.OutputContract)
	}

	existing, err := h.lifecycleRepo.HooksByAgent(ctx, agentID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	existingByID := map[string]*models.AgentLifecycleHook{}
	for i := range existing {
		existingByID[existing[i].ID] = &existing[i]
	}
	keepIDs := map[string]struct{}{}

	for _, p := range payloads {
		hook := &models.AgentLifecycleHook{
			ID:             p.ID,
			AgentID:        agentID,
			When:           models.LifecycleWhen(p.When),
			SkillKey:       p.SkillKey,
			Blocking:       p.Blocking,
			Enabled:        p.Enabled,
			PromptOverride: p.PromptOverride,
			RunPolicyJSON:  p.RunPolicyJSON,
			ScheduleJSON:   p.ScheduleJSON,
			OutputContract: models.LifecycleOutputContract(p.OutputContract),
		}
		if hook.ID != "" {
			if _, ok := existingByID[hook.ID]; ok {
				if err := h.lifecycleRepo.UpdateHook(ctx, hook); err != nil {
					return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
				}
				keepIDs[hook.ID] = struct{}{}
				continue
			}
		}
		if err := h.lifecycleRepo.CreateHook(ctx, hook); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		keepIDs[hook.ID] = struct{}{}
	}

	for id := range existingByID {
		if _, keep := keepIDs[id]; keep {
			continue
		}
		if err := h.lifecycleRepo.DeleteHook(ctx, id); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
	}
	return nil
}

// GetTaskLifecycleExecutions surfaces lifecycle hook activity for a task.
// Runbook §Rollout step 17: expose lifecycle executions and mutation
// summaries in task activity without cluttering the normal task board.
//
// @Summary List lifecycle executions for a task
// @Description Returns lifecycle hook invocations (routing, before-run preparation, after-complete learning) recorded for the given task.
// @Tags Lifecycle
// @Produce json
// @Param id path string true "Task ID"
// @Success 200 {array} viewmodels.LifecycleExecutionView
// @Failure 400 {object} ErrorResponse "Invalid task ID"
// @Failure 404 {object} ErrorResponse "Task not found"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/tasks/{id}/lifecycle-executions [get]
func (h *Handler) GetTaskLifecycleExecutions(c echo.Context) error {
	if h.lifecycleRepo == nil {
		return c.JSON(http.StatusOK, []viewmodels.LifecycleExecutionView{})
	}
	taskID := strings.TrimSpace(c.Param("id"))
	if taskID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "task id is required")
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	task, err := h.taskRepo.GetByID(c.Request().Context(), taskID)
	if err != nil {
		applog.Infof("[handler] GetTaskLifecycleExecutions task=%s lookup error: %v", taskID, err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if task == nil || task.ProjectID != projectID {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	execs, err := h.lifecycleRepo.ListExecutionsForTask(c.Request().Context(), taskID)
	if err != nil {
		applog.Infof("[handler] GetTaskLifecycleExecutions task=%s error: %v", taskID, err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	views := make([]viewmodels.LifecycleExecutionView, 0, len(execs))
	for _, e := range execs {
		views = append(views, toLifecycleExecutionView(e))
	}
	return c.JSON(http.StatusOK, views)
}

// GetLifecycleExecutionEvents returns the durable trace for one lifecycle execution.
// @Summary Get lifecycle execution trace events
// @Description Returns prompt-safe trace events for one lifecycle hook invocation.
// @Tags Lifecycle
// @Produce json
// @Param id path string true "Lifecycle execution ID"
// @Success 200 {array} viewmodels.LifecycleExecutionEventView
// @Failure 400 {object} ErrorResponse "Invalid execution ID"
// @Failure 404 {object} ErrorResponse "Lifecycle execution not found"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/lifecycle-executions/{id}/events [get]
func (h *Handler) GetLifecycleExecutionEvents(c echo.Context) error {
	if h.lifecycleRepo == nil {
		return c.JSON(http.StatusOK, []viewmodels.LifecycleExecutionEventView{})
	}
	execID := strings.TrimSpace(c.Param("id"))
	if execID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "lifecycle execution id is required")
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	events, found, err := h.lifecycleRepo.ListExecutionEventsForProject(c.Request().Context(), execID, projectID)
	if err != nil {
		applog.Infof("[handler] GetLifecycleExecutionEvents exec=%s error: %v", execID, err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if !found {
		return echo.NewHTTPError(http.StatusNotFound, "lifecycle execution not found")
	}
	views := make([]viewmodels.LifecycleExecutionEventView, 0, len(events))
	for _, e := range events {
		views = append(views, toLifecycleExecutionEventView(e))
	}
	return c.JSON(http.StatusOK, views)
}

// toLifecycleExecutionView returns the prompt-safe shape for UI/API use: never
// includes raw_output_text, prompt overrides, or input snapshots.
func toLifecycleExecutionView(e models.LifecycleExecution) viewmodels.LifecycleExecutionView {
	v := viewmodels.LifecycleExecutionView{
		ID:             e.ID,
		When:           string(e.When),
		AgentID:        e.AgentID,
		SkillKey:       e.SkillKey,
		Status:         string(e.Status),
		OutputContract: string(e.OutputContract),
		Error:          e.Error,
		StartedAt:      e.StartedAt,
		CompletedAt:    e.CompletedAt,
	}
	if e.OutputJSON != "" {
		v.Summary = extractStructuredSummary(e.OutputContract, e.OutputJSON)
		v.SelectedSkills = extractSelectedSkills(e.OutputContract, e.OutputJSON)
		v.SelectedMemories = extractSelectedMemoryViews(e, e.OutputJSON)
	}
	return v
}

func toLifecycleExecutionEventView(e models.LifecycleExecutionEvent) viewmodels.LifecycleExecutionEventView {
	payload := map[string]any{}
	if strings.TrimSpace(e.PayloadJSON) != "" {
		_ = json.Unmarshal([]byte(e.PayloadJSON), &payload)
	}
	return viewmodels.LifecycleExecutionEventView{
		ID:        e.ID,
		Seq:       e.Seq,
		EventType: e.EventType,
		Payload:   payload,
		CreatedAt: e.CreatedAt,
	}
}

// extractStructuredSummary pulls the single human-readable string out of each
// of the five output contracts (runbook lines 1325-1450). Returns "" for any
// contract the UI does not have a meaningful one-liner for.
func extractStructuredSummary(contract models.LifecycleOutputContract, raw string) string {
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return ""
	}
	switch contract {
	case models.OutputContractContextBlock:
		if s, ok := probe["title"].(string); ok && s != "" {
			return s
		}
	case models.OutputContractActivitySummary,
		models.OutputContractLearningSummary,
		models.OutputContractLibraryUpdateSummary:
		if s, ok := probe["summary"].(string); ok {
			return s
		}
	case models.OutputContractSelectedMode:
		if s, ok := probe["mode"].(string); ok {
			return s
		}
	case models.OutputContractSelectedSkills:
		if summary := selectedSkillsSummary(probe); summary != "" {
			return summary
		}
	case models.OutputContractSelectedMemories:
		return ""
	}
	return ""
}

func selectedSkillsSummary(probe map[string]any) string {
	parts := selectedSkillsFromProbe(probe)
	if len(parts) == 0 {
		return ""
	}
	return "Selected skills: " + strings.Join(parts, ", ")
}

func extractSelectedSkills(contract models.LifecycleOutputContract, raw string) []string {
	if contract != models.OutputContractSelectedSkills {
		return nil
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return nil
	}
	return selectedSkillsFromProbe(probe)
}

func selectedSkillsFromProbe(probe map[string]any) []string {
	rawSkills, _ := probe["skills"].([]any)
	if len(rawSkills) == 0 {
		rawSkills, _ = probe["selected_skills"].([]any)
	}
	parts := make([]string, 0, len(rawSkills))
	seen := map[string]struct{}{}
	for _, skill := range rawSkills {
		s, ok := skill.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		parts = append(parts, s)
	}
	return parts
}

const maxLifecycleMemoryDetailLen = 240

func extractSelectedMemoryViews(e models.LifecycleExecution, raw string) []viewmodels.SelectedMemoryView {
	if e.OutputContract == models.OutputContractSelectedMemories {
		var probe map[string]any
		if err := json.Unmarshal([]byte(raw), &probe); err != nil {
			return nil
		}
		return selectedMemoryViewsFromProbe(probe)
	}
	if e.When != models.LifecycleBeforeRun || e.SkillKey != "recall_memory" || e.OutputContract != models.OutputContractContextBlock {
		return nil
	}
	var cb lifecycle.ContextBlock
	if err := json.Unmarshal([]byte(raw), &cb); err != nil {
		return nil
	}
	out := make([]viewmodels.SelectedMemoryView, 0, len(cb.SelectedMemories)+len(cb.Sources))
	seen := map[string]struct{}{}
	for _, memory := range cb.SelectedMemories {
		file := sanitizeMemoryIdentifier(memory.File)
		if strings.TrimSpace(memory.File) != "" && file == "" {
			continue
		}
		topic := truncateLifecycleMemoryDetail(memory.Topic)
		identifier := selectedMemoryIdentifier(file, topic)
		if identifier == "" {
			continue
		}
		if _, ok := seen[identifier]; ok {
			continue
		}
		seen[identifier] = struct{}{}
		out = append(out, viewmodels.SelectedMemoryView{
			File:    file,
			Topic:   topic,
			Summary: truncateLifecycleMemoryDetail(memory.Summary),
			Snippet: truncateLifecycleMemoryDetail(memory.Snippet),
		})
	}
	for _, source := range cb.Sources {
		file := sanitizeMemoryIdentifier(source)
		if file == "" {
			continue
		}
		identifier := selectedMemoryIdentifier(file, "")
		if _, ok := seen[identifier]; ok {
			continue
		}
		seen[identifier] = struct{}{}
		out = append(out, viewmodels.SelectedMemoryView{File: file})
	}
	return out
}

func selectedMemoryViewsFromProbe(probe map[string]any) []viewmodels.SelectedMemoryView {
	rawMemories, _ := probe["memories"].([]any)
	if len(rawMemories) == 0 {
		rawMemories, _ = probe["selected_memories"].([]any)
	}
	out := make([]viewmodels.SelectedMemoryView, 0, len(rawMemories))
	seen := map[string]struct{}{}
	for _, raw := range rawMemories {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		file, _ := entry["file"].(string)
		topic, _ := entry["topic"].(string)
		summary, _ := entry["summary"].(string)
		snippet, _ := entry["snippet"].(string)
		file = sanitizeMemoryIdentifier(file)
		if strings.TrimSpace(file) == "" && strings.TrimSpace(topic) == "" {
			continue
		}
		identifier := selectedMemoryIdentifier(file, truncateLifecycleMemoryDetail(topic))
		if identifier == "" {
			continue
		}
		if _, ok := seen[identifier]; ok {
			continue
		}
		seen[identifier] = struct{}{}
		out = append(out, viewmodels.SelectedMemoryView{
			File:    file,
			Topic:   truncateLifecycleMemoryDetail(topic),
			Summary: truncateLifecycleMemoryDetail(summary),
			Snippet: truncateLifecycleMemoryDetail(snippet),
		})
	}
	return out
}

func selectedMemoryIdentifier(file, topic string) string {
	if file != "" {
		return file
	}
	return topic
}

func sanitizeMemoryIdentifier(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || strings.HasPrefix(value, "/") || value == "." || value == ".." || strings.Contains(value, "../") || strings.HasPrefix(value, "./") || strings.Contains(value, ":") {
		return ""
	}
	return truncateLifecycleMemoryDetail(value)
}

func truncateLifecycleMemoryDetail(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len(value) <= maxLifecycleMemoryDetailLen {
		return value
	}
	return value[:maxLifecycleMemoryDetailLen] + "..."
}

func isValidLifecycleWhen(s string) bool {
	switch models.LifecycleWhen(s) {
	case models.LifecycleRouteTask,
		models.LifecycleBeforeRun,
		models.LifecycleAfterComplete:
		return true
	}
	return false
}

func isValidLifecycleOutputContract(s string) bool {
	switch models.LifecycleOutputContract(s) {
	case models.OutputContractSelectedMode,
		models.OutputContractSelectedSkills,
		models.OutputContractSelectedMemories,
		models.OutputContractContextBlock,
		models.OutputContractActivitySummary,
		models.OutputContractLearningSummary,
		models.OutputContractLibraryUpdateSummary:
		return true
	}
	return false
}

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

type automationPreviewActionInput struct {
	Description string `json:"description"`
}

type automationSaveActionInput struct {
	Source         string `json:"source"`
	TemplateKey    string `json:"template_key"`
	Description    string `json:"description"`
	AutomationYAML string `json:"automation_yaml"`
}

func (h *Handler) executeAutomationPreviewAction(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	var request automationPreviewActionInput
	if err := chatcontrol.DecodeRuntimeToolInput(input, &request); err != nil {
		return "", err
	}
	result, err := h.previewAutomationDescription(ctx, params.ProjectID, request.Description)
	if err != nil {
		return "", err
	}
	return marshalAutomationActionResult(map[string]any{"candidate": result.Candidate, "assumptions": result.Assumptions, "warnings": result.Warnings, "validation_errors": result.ValidationErrors, "summary": result.Summary, "persisted": false, "active": false})
}

func (h *Handler) executeAutomationSaveAction(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	if h.automationDraftSvc == nil || h.automationCompiler == nil {
		return "", errors.New("automation save is unavailable")
	}
	var request automationSaveActionInput
	if err := chatcontrol.DecodeRuntimeToolInput(input, &request); err != nil {
		return "", err
	}
	if field, ok := automationSaveRawCandidateIdentityField(input); ok {
		return automationValidationResultForChat("unsupported_candidate_identity", fmt.Sprintf("raw Automation candidate identity/version field %q is not supported; submit only the canonical Automation YAML document format", field))
	}
	var candidate models.AutomationDraftCandidate
	var source string
	switch request.Source {
	case "template":
		var err error
		candidate, err = h.automationDraftSvc.CreationTemplateCandidate(request.TemplateKey)
		if err != nil {
			return "", err
		}
		h.applyAutomationTemplateDefaultModel(ctx, params.ProjectID, &candidate)
		source = "template"
	case "blank":
		var err error
		candidate, err = h.automationDraftSvc.BlankCandidate(request.TemplateKey)
		if err != nil {
			return "", err
		}
		source = "manual"
	case "describe":
		preview, err := h.previewAutomationDescription(ctx, params.ProjectID, request.Description)
		if err != nil {
			return "", err
		}
		candidate = preview.Candidate
		source = "manual"
	case "yaml":
		var err error
		candidate, err = service.DecodeAutomationDraftYAML([]byte(request.AutomationYAML))
		if err != nil {
			return automationValidationResultForChat("invalid_yaml", err.Error())
		}
		source = "manual"
	default:
		return "", errors.New("automation source must be template, describe, blank, or yaml")
	}
	plan, candidate, err := h.automationCompiler.PreviewSave(ctx, params.ProjectID, candidate)
	if err != nil {
		return "", err
	}
	if len(plan.Validation) > 0 {
		return marshalAutomationActionResult(map[string]any{"candidate": candidate, "assumptions": candidate.Assumptions, "warnings": candidate.Warnings, "validation_errors": plan.Validation, "plan": automationSavePlanForChat(plan), "active": false})
	}
	saved, err := h.automationCompiler.Save(ctx, service.AutomationSaveRequest{
		ProjectID: params.ProjectID, Source: source, CreatedVia: "chat", Candidate: candidate,
	})
	if err != nil {
		return "", err
	}
	return marshalAutomationActionResult(map[string]any{"automation_id": saved.Definition.Automation.ID,
		"status": saved.Definition.Automation.LifecycleState,
		"url":    fmt.Sprintf("/automations/%s?project_id=%s", saved.Definition.Automation.ID, params.ProjectID), "active": true})
}

func (h *Handler) previewAutomationDescription(ctx context.Context, projectID, description string) (*models.AutomationDraftResult, error) {
	if h.automationDraftSvc == nil || h.automationCapabilitySvc == nil || h.llmSvc == nil || h.llmConfigRepo == nil {
		return nil, errors.New("automation description generation is unavailable")
	}
	snapshot, err := h.automationCapabilitySvc.Build(ctx, projectID)
	if err != nil {
		return nil, err
	}
	model, err := h.llmConfigRepo.GetDefault(ctx)
	if err != nil || model == nil {
		return nil, errors.New("no default model is configured for automation description generation")
	}
	workDir := h.resolveWorkDir(ctx, projectID)
	return h.automationDraftSvc.PreviewDescription(ctx, description, snapshot, func(callCtx context.Context, prompt string) (string, error) {
		output, _, callErr := h.llmSvc.CallAgentDirectNoTools(service.WithDirectUsageProject(callCtx, projectID), prompt, nil, *model, workDir)
		return output, callErr
	})
}

func automationActionPrincipal(params streamingResponseParams) string {
	if strings.TrimSpace(params.PrincipalID) != "" {
		return strings.TrimSpace(params.PrincipalID)
	}
	return "local"
}

func automationSavePlanForChat(plan *models.AutomationSavePlan) map[string]any {
	if plan == nil {
		return map[string]any{}
	}
	effects := append([]models.AutomationSaveEffect(nil), plan.Effects...)
	for i := range effects {
		effects[i].Name = automationSavePlanDisplayName(effects[i].Name)
	}
	return map[string]any{
		"effects":           effects,
		"validation_errors": plan.Validation,
		"will_not":          plan.WillNot,
	}
}

func automationSavePlanDisplayName(name string) string {
	marker := strings.LastIndex(name, " [")
	if marker < 0 || len(name)-marker != 11 || !strings.HasSuffix(name, "]") {
		return name
	}
	return name[:marker]
}

func automationSaveRawCandidateIdentityField(input json.RawMessage) (string, bool) {
	if strings.TrimSpace(string(input)) == "" {
		return "", false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(input, &raw); err != nil {
		return "", false
	}
	for _, field := range []string{"candidate", "candidate_json", "automation_id", "version_id", "plan_revision", "token_id", "confirming_user_input_id"} {
		if _, ok := raw[field]; ok {
			return field, true
		}
	}
	return "", false
}

func automationValidationResultForChat(code, message string) (string, error) {
	return marshalAutomationActionResult(map[string]any{
		"validation_errors": []models.AutomationValidationIssue{{Code: code, Message: message}},
		"active":            false,
	})
}

func marshalAutomationActionResult(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

type listAutomationsToolInput struct {
	ProjectID string `json:"project_id"`
}

type getAutomationToolInput struct {
	AutomationID string `json:"automation_id"`
	ProjectID    string `json:"project_id"`
}

type automationLifecycleActionInput struct {
	AutomationID string `json:"automation_id"`
	Name         string `json:"name"`
}

// automationCardSummary converts an AutomationCard to the compact prompt-safe shape
// exposed by list_automations and get_automation. It intentionally omits YAML/graph content.
func automationCardSummary(card models.AutomationCard) map[string]any {
	paused := card.Automation.LifecycleState == models.AutomationPaused
	summary := map[string]any{
		"id":                        card.Automation.ID,
		"name":                      card.Automation.Name,
		"status":                    string(card.Automation.LifecycleState),
		"paused":                    paused,
		"adapter_key":               card.Version.AdapterKey,
		"template_update_available": card.TemplateUpdateAvailable,
		"node_count": card.Counts.Running + card.Counts.Waiting +
			card.Counts.Blocked + card.Counts.Failed + card.Counts.CompletedRecently,
		"counts": map[string]int{
			"running":            card.Counts.Running,
			"waiting":            card.Counts.Waiting,
			"blocked":            card.Counts.Blocked,
			"failed":             card.Counts.Failed,
			"completed_recently": card.Counts.CompletedRecently,
		},
	}
	if card.Automation.TemplateRevision != nil {
		summary["template_revision"] = *card.Automation.TemplateRevision
	}
	if current := service.CurrentAutomationTemplateRevision(card.Version.AdapterKey); current > 0 {
		summary["current_template_revision"] = current
	}
	if card.NextRun != nil {
		summary["next_run"] = card.NextRun.UTC().Format("2006-01-02T15:04:05Z")
	}
	if card.LastRun != nil {
		summary["last_run"] = card.LastRun.UTC().Format("2006-01-02T15:04:05Z")
	}
	return summary
}

func (h *Handler) executeListAutomationsTool(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	if h.automationGraphSvc == nil {
		return marshalAutomationActionResult(map[string]any{"automations": []any{}})
	}
	var req listAutomationsToolInput
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", err
	}
	projectID := strings.TrimSpace(params.ProjectID)
	if override := strings.TrimSpace(req.ProjectID); override != "" {
		projectID = override
	}
	if projectID == "" {
		return "", fmt.Errorf("list_automations: no current project")
	}
	cards, err := h.automationGraphSvc.List(ctx, projectID)
	if err != nil {
		return "", err
	}
	summaries := make([]map[string]any, 0, len(cards))
	for _, card := range cards {
		summaries = append(summaries, automationCardSummary(card))
	}
	return marshalAutomationActionResult(map[string]any{"automations": summaries})
}

func (h *Handler) executeGetAutomationTool(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	if h.automationGraphSvc == nil {
		return "", fmt.Errorf("automations unavailable")
	}
	var req getAutomationToolInput
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", err
	}
	automationID := strings.TrimSpace(req.AutomationID)
	if automationID == "" {
		return "", fmt.Errorf("get_automation: automation_id is required")
	}
	projectID := strings.TrimSpace(params.ProjectID)
	if override := strings.TrimSpace(req.ProjectID); override != "" {
		projectID = override
	}
	if projectID == "" {
		return "", fmt.Errorf("get_automation: no current project")
	}
	cards, err := h.automationGraphSvc.List(ctx, projectID)
	if err != nil {
		return "", err
	}
	for _, card := range cards {
		if card.Automation.ID == automationID {
			return marshalAutomationActionResult(map[string]any{"automation": automationCardSummary(card)})
		}
	}
	return marshalAutomationActionResult(map[string]any{"error": fmt.Sprintf("automation %q not found in project %s", automationID, projectID), "found": false})
}

func (h *Handler) executeUpdateAutomationTemplateTool(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	return service.ExecuteAutomationTemplateUpdateRuntime(ctx, service.AutomationTemplateUpdateRuntimeOptions{
		ProjectID:          params.ProjectID,
		Input:              input,
		AutomationGraphSvc: h.automationGraphSvc,
		AutomationDraftSvc: h.automationDraftSvc,
		AutomationCompiler: h.automationCompiler,
	})
}

func (h *Handler) executeRunAutomationNowTool(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	return h.executeAutomationLifecycleAction(ctx, params, input, "run_automation_now")
}

func (h *Handler) executePauseAutomationTool(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	return h.executeAutomationLifecycleAction(ctx, params, input, "pause_automation")
}

func (h *Handler) executeResumeAutomationTool(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	return h.executeAutomationLifecycleAction(ctx, params, input, "resume_automation")
}

func (h *Handler) executeAutomationLifecycleAction(ctx context.Context, params streamingResponseParams, input json.RawMessage, action string) (string, error) {
	if h.automationGraphSvc == nil {
		return "", fmt.Errorf("%s: automations unavailable", action)
	}
	if h.automationLifecycleSvc == nil {
		return "", fmt.Errorf("%s: automation lifecycle unavailable", action)
	}
	var req automationLifecycleActionInput
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", err
	}
	projectID := strings.TrimSpace(params.ProjectID)
	if projectID == "" {
		return "", fmt.Errorf("%s: no current project", action)
	}
	card, err := h.resolveAutomationLifecycleTarget(ctx, projectID, req)
	if err != nil {
		return "", fmt.Errorf("%s: %w", action, err)
	}

	var invocations []models.AutomationInvocation
	switch action {
	case "run_automation_now":
		invocations, _, err = h.automationLifecycleSvc.RunNow(ctx, projectID, card.Automation.ID)
	case "pause_automation":
		err = h.automationLifecycleSvc.Pause(ctx, projectID, card.Automation.ID)
	case "resume_automation":
		err = h.automationLifecycleSvc.Resume(ctx, projectID, card.Automation.ID)
	default:
		err = fmt.Errorf("unsupported Automation lifecycle action %q", action)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", action, err)
	}

	fresh, err := h.automationCardByID(ctx, projectID, card.Automation.ID)
	if err == nil && fresh != nil {
		card = *fresh
	}
	return marshalAutomationActionResult(automationLifecycleActionResult(action, projectID, card, invocations))
}

func (h *Handler) resolveAutomationLifecycleTarget(ctx context.Context, projectID string, req automationLifecycleActionInput) (models.AutomationCard, error) {
	cards, err := h.automationGraphSvc.List(ctx, projectID)
	if err != nil {
		return models.AutomationCard{}, err
	}
	automationID := strings.TrimSpace(req.AutomationID)
	name := strings.TrimSpace(req.Name)
	if automationID == "" && name == "" {
		return models.AutomationCard{}, errors.New("automation_id or name is required")
	}
	if automationID != "" {
		for _, card := range cards {
			if card.Automation.ID == automationID {
				if name != "" && !strings.EqualFold(strings.TrimSpace(card.Automation.Name), name) {
					return models.AutomationCard{}, fmt.Errorf("automation_id %q is named %q, not %q", automationID, card.Automation.Name, name)
				}
				return card, nil
			}
		}
		return models.AutomationCard{}, fmt.Errorf("automation %q not found in current project", automationID)
	}
	var matches []models.AutomationCard
	for _, card := range cards {
		if strings.EqualFold(strings.TrimSpace(card.Automation.Name), name) {
			matches = append(matches, card)
		}
	}
	switch len(matches) {
	case 0:
		return models.AutomationCard{}, fmt.Errorf("automation named %q not found in current project", name)
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, match.Automation.ID)
		}
		sort.Strings(ids)
		return models.AutomationCard{}, fmt.Errorf("automation name %q is ambiguous in current project; use automation_id (%s)", name, strings.Join(ids, ", "))
	}
}

func (h *Handler) automationCardByID(ctx context.Context, projectID, automationID string) (*models.AutomationCard, error) {
	cards, err := h.automationGraphSvc.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, card := range cards {
		if card.Automation.ID == automationID {
			return &card, nil
		}
	}
	return nil, nil
}

func automationLifecycleActionResult(action, projectID string, card models.AutomationCard, invocations []models.AutomationInvocation) map[string]any {
	result := map[string]any{
		"action":          action,
		"automation_id":   card.Automation.ID,
		"name":            card.Automation.Name,
		"lifecycle_state": string(card.Automation.LifecycleState),
		"url":             "/automations/" + url.PathEscape(card.Automation.ID) + "?project_id=" + url.QueryEscape(projectID),
	}
	if action == "run_automation_now" {
		started := make([]string, 0, len(invocations))
		all := make([]map[string]string, 0, len(invocations))
		for _, invocation := range invocations {
			if invocation.ID == "" {
				continue
			}
			status := string(invocation.Status)
			all = append(all, map[string]string{"id": invocation.ID, "status": status})
			switch invocation.Status {
			case models.AutomationInvocationClaimed, models.AutomationInvocationDispatched, models.AutomationInvocationRunning:
				started = append(started, invocation.ID)
			}
		}
		result["started"] = len(started) > 0
		result["started_invocation_ids"] = started
		result["invocations"] = all
	}
	return result
}

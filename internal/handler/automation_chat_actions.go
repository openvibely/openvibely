package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

type automationPreviewActionInput struct {
	Description string `json:"description"`
}

type automationPlanSaveActionInput struct {
	Source      string `json:"source"`
	TemplateKey string `json:"template_key"`
	Description string `json:"description"`
}

type automationSaveActionInput struct {
	ConfirmationToken     string `json:"confirmation_token"`
	ConfirmingUserInputID string `json:"confirming_user_input_id"`
}

func (h *Handler) executeAutomationPreviewAction(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	var request automationPreviewActionInput
	if err := decodeChatActionInput(input, &request); err != nil {
		return "", err
	}
	result, err := h.previewAutomationDescription(ctx, params.ProjectID, request.Description)
	if err != nil {
		return "", err
	}
	return marshalAutomationActionResult(map[string]any{"candidate": result.Candidate, "assumptions": result.Assumptions, "warnings": result.Warnings, "validation_errors": result.ValidationErrors, "summary": result.Summary, "persisted": false, "active": false})
}

func (h *Handler) executeAutomationPlanSaveAction(ctx context.Context, params streamingResponseParams, input json.RawMessage, collector *chatActionSummaryCollector) (string, error) {
	if h.automationDraftSvc == nil || h.automationPlanner == nil || h.automationConfirmationSvc == nil {
		return "", errors.New("automation save planning is unavailable")
	}
	if strings.TrimSpace(params.TaskID) == "" || strings.TrimSpace(params.ExecID) == "" || collector == nil {
		return "", errors.New("automation save planning requires a durable chat thread and stored plan message")
	}
	var request automationPlanSaveActionInput
	if err := decodeChatActionInput(input, &request); err != nil {
		return "", err
	}
	var candidate models.AutomationDraftCandidate
	var source string
	switch request.Source {
	case "template":
		var err error
		candidate, err = h.automationDraftSvc.TemplateCandidate(request.TemplateKey)
		if err != nil {
			return "", err
		}
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
	default:
		return "", errors.New("automation source must be template, describe, or blank")
	}
	staged, err := h.automationDraftSvc.CreateDraft(ctx, service.AutomationDraftCreateRequest{ProjectID: params.ProjectID, Source: source, CreatedVia: "chat", Candidate: candidate})
	if err != nil {
		return "", err
	}
	if staged == nil || staged.Definition == nil {
		return "", errors.New("automation save plan could not be staged")
	}
	automationID := staged.Definition.Automation.ID
	versionID := staged.Definition.Version.ID
	stagingRemoved := false
	defer func() {
		if !stagingRemoved {
			_ = h.automationDraftSvc.DiscardStagedVersion(context.Background(), params.ProjectID, automationID, versionID)
		}
	}()
	plan, err := h.automationPlanner.Plan(ctx, params.ProjectID, automationID, versionID)
	if err != nil {
		return "", err
	}
	if len(plan.Validation) > 0 {
		return marshalAutomationActionResult(map[string]any{"candidate": staged.Candidate, "assumptions": staged.Assumptions, "warnings": staged.Warnings, "validation_errors": plan.Validation, "plan": automationSavePlanForChat(plan), "active": false, "confirmation_required": false})
	}
	if err := h.automationDraftSvc.DiscardStagedVersion(ctx, params.ProjectID, automationID, versionID); err != nil {
		return "", err
	}
	stagingRemoved = true
	principal := automationActionPrincipal(params)
	name := staged.Candidate.Name
	collector.addAutomationPlan(pendingAutomationPlanConfirmation{
		Issue: service.AutomationConfirmationIssue{ProjectID: params.ProjectID, AutomationID: automationID,
			VersionID: versionID, PlanRevision: plan.PlanRevision, PrincipalID: principal,
			ThreadID: params.TaskID, PlanMessageID: params.ExecID, AutomationName: name, Source: source,
			Candidate: staged.Candidate},
		Plan: *plan, Name: name,
	})
	return marshalAutomationActionResult(map[string]any{"candidate": staged.Candidate, "assumptions": staged.Assumptions, "warnings": staged.Warnings, "validation_errors": staged.ValidationErrors, "summary": staged.Summary, "plan": automationSavePlanForChat(plan), "confirmation_required": true, "confirmation_command": "save " + name, "active": false, "message": "Review this save plan. Nothing has been created or activated yet. The Chat host will enable Save only after this plan message is durably stored."})
}

func (h *Handler) executeAutomationSaveAction(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	if h.automationDraftSvc == nil || h.automationPlanner == nil || h.automationConfirmationSvc == nil || h.automationCompiler == nil {
		return "", errors.New("automation save is unavailable")
	}
	var request automationSaveActionInput
	if err := decodeChatActionInput(input, &request); err != nil {
		return "", err
	}
	principal := automationActionPrincipal(params)
	pending, err := h.automationConfirmationSvc.ResolveChatConfirmation(ctx, request.ConfirmationToken, params.ProjectID, principal, params.TaskID)
	if err != nil {
		return "", err
	}
	staged, err := h.automationDraftSvc.GetDraft(ctx, params.ProjectID, pending.AutomationID, pending.VersionID)
	createdStaging := false
	if err == nil && staged == nil {
		staged, err = h.automationDraftSvc.CreateDraft(ctx, service.AutomationDraftCreateRequest{
			ProjectID: params.ProjectID, AutomationID: pending.AutomationID, VersionID: pending.VersionID,
			Source: pending.Source, CreatedVia: "chat", Candidate: pending.Candidate,
		})
		createdStaging = err == nil
	}
	if err != nil {
		return "", err
	}
	if staged == nil || staged.Definition == nil {
		return "", errors.New("pending Automation save could not be staged")
	}
	reserved := false
	defer func() {
		if createdStaging && !reserved {
			_ = h.automationDraftSvc.DiscardStagedVersion(context.Background(), params.ProjectID, pending.AutomationID, pending.VersionID)
		}
	}()
	plan, err := h.automationPlanner.Plan(ctx, params.ProjectID, pending.AutomationID, pending.VersionID)
	if err != nil {
		return "", err
	}
	if plan.PlanRevision != pending.PlanRevision {
		return "", errors.New("the Automation save plan changed; review a new plan before saving")
	}
	name := pending.AutomationName
	_, err = h.automationConfirmationSvc.ConfirmChat(ctx, service.AutomationChatConfirmation{Token: request.ConfirmationToken, ProjectID: params.ProjectID, AutomationID: pending.AutomationID, VersionID: pending.VersionID, PlanRevision: pending.PlanRevision, PrincipalID: principal, ThreadID: params.TaskID, ConfirmingUserInputID: request.ConfirmingUserInputID, AutomationName: name, Effects: plan.Effects})
	if err != nil {
		return "", err
	}
	reserved = true
	saved, saveErr := h.automationCompiler.Publish(ctx, service.AutomationPublishRequest{ProjectID: params.ProjectID, AutomationID: pending.AutomationID, VersionID: pending.VersionID, PlanRevision: pending.PlanRevision})
	if saveErr != nil {
		if saved != nil {
			return marshalAutomationActionResult(map[string]any{"active": false, "error": saveErr.Error(), "resources": saved.Resources, "message": "Save failed. The current Automation remains unchanged."})
		}
		return "", saveErr
	}
	return marshalAutomationActionResult(map[string]any{"automation_id": saved.Definition.Automation.ID, "status": saved.Definition.Automation.LifecycleState, "resources": saved.Resources, "url": fmt.Sprintf("/automations/%s?project_id=%s", saved.Definition.Automation.ID, params.ProjectID), "active": true})
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

func automationSavePlanForChat(plan *models.AutomationPublicationPlan) map[string]any {
	if plan == nil {
		return map[string]any{}
	}
	effects := append([]models.AutomationPublicationEffect(nil), plan.Effects...)
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

func marshalAutomationActionResult(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

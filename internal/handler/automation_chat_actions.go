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

type automationCreateDraftActionInput struct {
	Source      string `json:"source"`
	TemplateKey string `json:"template_key"`
	Description string `json:"description"`
}

type automationPlanActionInput struct {
	AutomationID string `json:"automation_id"`
	VersionID    string `json:"version_id"`
}

type automationPublishActionInput struct {
	AutomationID          string `json:"automation_id"`
	VersionID             string `json:"version_id"`
	PlanRevision          string `json:"plan_revision"`
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

func (h *Handler) executeAutomationCreateDraftAction(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	if h.automationDraftSvc == nil {
		return "", errors.New("automation draft service is unavailable")
	}
	var request automationCreateDraftActionInput
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
		return "", errors.New("automation draft source must be template, describe, or blank")
	}
	result, err := h.automationDraftSvc.CreateDraft(ctx, service.AutomationDraftCreateRequest{ProjectID: params.ProjectID, Source: source, CreatedVia: "chat", Candidate: candidate})
	if err != nil {
		return "", err
	}
	return marshalAutomationActionResult(map[string]any{"automation_id": result.Definition.Automation.ID, "version_id": result.Definition.Version.ID, "candidate": result.Candidate, "assumptions": result.Assumptions, "warnings": result.Warnings, "validation_errors": result.ValidationErrors, "summary": result.Summary, "url": result.URL, "active": false, "message": "Draft created. Nothing has been scheduled or started."})
}

func (h *Handler) executeAutomationPlanAction(ctx context.Context, params streamingResponseParams, input json.RawMessage, collector *chatActionSummaryCollector) (string, error) {
	if h.automationPlanner == nil || h.automationConfirmationSvc == nil {
		return "", errors.New("automation publication planning is unavailable")
	}
	var request automationPlanActionInput
	if err := decodeChatActionInput(input, &request); err != nil {
		return "", err
	}
	plan, err := h.automationPlanner.Plan(ctx, params.ProjectID, request.AutomationID, request.VersionID)
	if err != nil {
		return "", err
	}
	if len(plan.Validation) > 0 {
		return marshalAutomationActionResult(map[string]any{"plan": plan, "active": false, "confirmation_required": false})
	}
	principal := automationActionPrincipal(params)
	if strings.TrimSpace(params.TaskID) == "" || strings.TrimSpace(params.ExecID) == "" || collector == nil {
		return "", errors.New("automation publication planning requires a durable chat thread and stored plan message")
	}
	name := automationNameFromPlan(ctx, h, params.ProjectID, request.AutomationID)
	collector.addAutomationPlan(pendingAutomationPlanConfirmation{
		Issue: service.AutomationConfirmationIssue{ProjectID: params.ProjectID, AutomationID: request.AutomationID,
			VersionID: request.VersionID, PlanRevision: plan.PlanRevision, PrincipalID: principal,
			ThreadID: params.TaskID, PlanMessageID: params.ExecID},
		Plan: *plan, Name: name,
	})
	return marshalAutomationActionResult(map[string]any{"plan": plan, "confirmation_required": true, "confirmation_command": "publish " + name, "active": false, "message": "Review this publication plan. Nothing has been created or activated yet. The Chat host will enable confirmation only after this plan message is durably stored."})
}

func (h *Handler) executeAutomationPublishAction(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	if h.automationPlanner == nil || h.automationConfirmationSvc == nil || h.automationCompiler == nil {
		return "", errors.New("automation publication is unavailable")
	}
	var request automationPublishActionInput
	if err := decodeChatActionInput(input, &request); err != nil {
		return "", err
	}
	plan, err := h.automationPlanner.Plan(ctx, params.ProjectID, request.AutomationID, request.VersionID)
	if err != nil {
		return "", err
	}
	if plan.PlanRevision != request.PlanRevision {
		return "", errors.New("stale publication plan; preview the publication again")
	}
	name := automationNameFromPlan(ctx, h, params.ProjectID, request.AutomationID)
	_, err = h.automationConfirmationSvc.ConfirmChat(ctx, service.AutomationChatConfirmation{Token: request.ConfirmationToken, ProjectID: params.ProjectID, AutomationID: request.AutomationID, VersionID: request.VersionID, PlanRevision: request.PlanRevision, PrincipalID: automationActionPrincipal(params), ThreadID: params.TaskID, ConfirmingUserInputID: request.ConfirmingUserInputID, AutomationName: name, Effects: plan.Effects})
	if err != nil {
		return "", err
	}
	published, publishErr := h.automationCompiler.Publish(ctx, service.AutomationPublishRequest{ProjectID: params.ProjectID, AutomationID: request.AutomationID, VersionID: request.VersionID, PlanRevision: request.PlanRevision})
	if publishErr != nil {
		if published != nil {
			return marshalAutomationActionResult(map[string]any{"automation_id": request.AutomationID, "version_id": request.VersionID, "active": false, "error": publishErr.Error(), "resources": published.Resources, "message": "Publication failed. The draft remains unpublished and the prior version remains active."})
		}
		return "", publishErr
	}
	return marshalAutomationActionResult(map[string]any{"automation_id": published.Definition.Automation.ID, "version_id": published.Definition.Version.ID, "status": published.Definition.Automation.LifecycleState, "resources": published.Resources, "url": fmt.Sprintf("/automations/%s?project_id=%s&view=live", published.Definition.Automation.ID, params.ProjectID), "active": true})
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

func automationNameFromPlan(ctx context.Context, h *Handler, projectID, automationID string) string {
	if h != nil && h.automationGraphSvc != nil {
		if definition, _, err := h.automationGraphSvc.GetDefinition(ctx, projectID, automationID); err == nil && definition != nil {
			return definition.Automation.Name
		}
	}
	return "automation"
}

func marshalAutomationActionResult(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

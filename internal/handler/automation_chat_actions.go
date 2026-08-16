package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	var candidate models.AutomationDraftCandidate
	var source string
	switch request.Source {
	case "template":
		var err error
		candidate, err = h.automationDraftSvc.CreationTemplateCandidate(request.TemplateKey)
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

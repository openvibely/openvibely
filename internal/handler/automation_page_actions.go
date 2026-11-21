package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
)

type automationMutationArgs struct {
	ProjectID, AutomationID, Operation string
	AutomationIDs                      []string
}

var actAutomationMutation = defineAction("automation.mutate", func(h *Handler, ctx context.Context, a automationMutationArgs) (string, authz.Permission, error) {
	return a.ProjectID, authz.Edit, nil
}, func(h *Handler, ctx context.Context, a automationMutationArgs) ([]models.AutomationInvocation, error) {
	if h.automationLifecycleSvc == nil {
		return nil, statusErr(http.StatusServiceUnavailable, "automation lifecycle unavailable")
	}
	var invocations []models.AutomationInvocation
	var err error
	switch a.Operation {
	case "run":
		invocations, _, err = h.automationLifecycleSvc.RunNow(ctx, a.ProjectID, a.AutomationID)
	case "pause":
		err = h.automationLifecycleSvc.Pause(ctx, a.ProjectID, a.AutomationID)
	case "resume":
		err = h.automationLifecycleSvc.Resume(ctx, a.ProjectID, a.AutomationID)
	case "delete":
		err = h.automationLifecycleSvc.Delete(ctx, a.ProjectID, a.AutomationID)
	case "delete_bulk":
		err = h.automationLifecycleSvc.DeleteBulk(ctx, a.ProjectID, a.AutomationIDs)
	default:
		return nil, statusErr(http.StatusBadRequest, "unknown automation action")
	}
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return nil, statusErr(http.StatusNotFound, "automation not found")
		}
		return nil, statusErr(http.StatusBadRequest, err.Error())
	}
	return invocations, nil
})

var actAutomationRefresh = defineAction("automation.refresh", func(h *Handler, ctx context.Context, a automationMutationArgs) (string, authz.Permission, error) {
	return a.ProjectID, authz.Edit, nil
}, func(h *Handler, ctx context.Context, a automationMutationArgs) (struct{}, error) {
	if h.automationExternalStateSvc == nil {
		return struct{}{}, statusErr(http.StatusServiceUnavailable, "automation external refresh unavailable")
	}
	_, err := h.automationExternalStateSvc.Refresh(ctx, a.ProjectID, a.AutomationID, time.Now().UTC())
	if err != nil {
		if err.Error() == "automation not found" {
			return struct{}{}, statusErr(http.StatusNotFound, err.Error())
		}
		return struct{}{}, statusErr(http.StatusBadGateway, err.Error())
	}
	return struct{}{}, nil
})

type automationSaveArgs struct {
	ProjectID, AutomationID, Source string
	Candidate                       models.AutomationDraftCandidate
	UpdateToLatestTemplate          bool
}

var actAutomationSave = defineAction("automation.save", func(h *Handler, ctx context.Context, a automationSaveArgs) (string, authz.Permission, error) {
	return a.ProjectID, authz.Edit, nil
}, func(h *Handler, ctx context.Context, a automationSaveArgs) (*service.AutomationSaveResult, error) {
	if h.automationCompiler == nil {
		return nil, statusErr(http.StatusServiceUnavailable, "automation save unavailable")
	}
	return h.automationCompiler.SaveValidatedCandidate(ctx, service.AutomationSaveRequest{ProjectID: a.ProjectID, AutomationID: a.AutomationID, Source: a.Source, CreatedVia: "web", Candidate: a.Candidate, UpdateToLatestTemplate: a.UpdateToLatestTemplate})
})

type automationPreviewArgs struct {
	ProjectID  string
	Candidate  models.AutomationDraftCandidate
	Definition *models.AutomationDefinition
}

var actAutomationPreview = defineAction("automation.preview", func(h *Handler, ctx context.Context, a automationPreviewArgs) (string, authz.Permission, error) {
	return a.ProjectID, authz.Edit, nil
}, func(h *Handler, ctx context.Context, a automationPreviewArgs) (*models.AutomationDraftResult, error) {
	return h.previewAutomationBuilderLocal(ctx, a.ProjectID, a.Candidate, a.Definition)
})

type automationDescriptionArgs struct {
	ProjectID   string
	Description string `remote:"raw"`
}

var actAutomationDescribe = defineAction("automation.describe", func(h *Handler, ctx context.Context, a automationDescriptionArgs) (string, authz.Permission, error) {
	return a.ProjectID, authz.Edit, nil
}, func(h *Handler, ctx context.Context, a automationDescriptionArgs) (*models.AutomationDraftResult, error) {
	return h.previewAutomationDescriptionLocal(ctx, a.ProjectID, a.Description)
})

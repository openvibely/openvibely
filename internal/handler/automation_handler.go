package handler

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) SetAutomationServices(graph *service.AutomationGraphService, registration *service.AutomationRegistrationService) {
	h.automationGraphSvc = graph
	h.automationRegistrationSvc = registration
}

func (h *Handler) SetAutomationExternalStateService(external *service.AutomationExternalStateService) {
	h.automationExternalStateSvc = external
}

func (h *Handler) SetAutomationBuilderServices(drafts *service.AutomationDraftService, capabilities *service.AutomationCapabilitySnapshotBuilder, planner *service.AutomationPublicationPlanner, compiler *service.AutomationCompiler, confirmation *service.AutomationConfirmationService, lifecycle *service.AutomationLifecycleService) {
	h.automationDraftSvc = drafts
	h.automationCapabilitySvc = capabilities
	h.automationPlanner = planner
	h.automationCompiler = compiler
	h.automationConfirmationSvc = confirmation
	h.automationLifecycleSvc = lifecycle
	if drafts != nil {
		drafts.SetCapabilitySnapshotBuilder(capabilities)
	}
	if h.agentRepo != nil {
		if planner != nil {
			planner.SetAgentRepository(h.agentRepo)
		}
		if compiler != nil {
			compiler.SetAgentRepository(h.agentRepo)
		}
	}
}

func (h *Handler) ListAutomations(c echo.Context) error {
	if h.automationGraphSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automations unavailable")
	}
	ctx := c.Request().Context()
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	cards, err := h.automationGraphSvc.List(ctx, projectID)
	if err != nil {
		return err
	}
	var recoveries []models.AutomationSaveRecovery
	if h.automationDraftSvc != nil {
		recoveries, err = h.automationDraftSvc.ListRecoverableSaves(ctx, projectID)
		if err != nil {
			return err
		}
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AutomationsContent(cards, recoveries, projectID))
	}
	projects, _ := h.projectSvc.List(ctx)
	return render(c, http.StatusOK, pages.Automations(projects, projectID, cards, recoveries))
}

func (h *Handler) GetAutomationLive(c echo.Context) error {
	if h.automationGraphSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automations unavailable")
	}
	ctx := c.Request().Context()
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	graph, err := h.automationGraphSvc.GetLive(ctx, projectID, c.Param("automationId"), time.Now())
	if err != nil {
		return err
	}
	if graph == nil {
		return echo.NewHTTPError(http.StatusNotFound, "automation not found")
	}
	deleteAvailable := true
	if h.automationDraftSvc != nil {
		recovery, recoveryErr := h.automationDraftSvc.RecoverableSave(ctx, projectID, graph.Automation.ID)
		if recoveryErr != nil {
			return recoveryErr
		}
		deleteAvailable = recovery == nil
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AutomationLiveContent(*graph, projectID, deleteAvailable))
	}
	projects, _ := h.projectSvc.List(ctx)
	return render(c, http.StatusOK, pages.AutomationLive(projects, projectID, *graph, deleteAvailable))
}

func (h *Handler) RefreshAutomationExternalState(c echo.Context) error {
	if h.automationExternalStateSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation external refresh unavailable")
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	if _, err := h.automationExternalStateSvc.Refresh(c.Request().Context(), projectID, c.Param("automationId"), time.Now().UTC()); err != nil {
		if err.Error() == "automation not found" {
			return echo.NewHTTPError(http.StatusNotFound, "automation not found")
		}
		return echo.NewHTTPError(http.StatusBadGateway, err.Error())
	}
	c.Response().Header().Set("HX-Trigger", "automationExternalRefreshed")
	return h.GetAutomationLive(c)
}

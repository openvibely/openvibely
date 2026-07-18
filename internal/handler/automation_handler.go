package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) SetAutomationServices(graph *service.AutomationGraphService, registration *service.AutomationRegistrationService) {
	h.automationGraphSvc = graph
	h.automationRegistrationSvc = registration
}

func (h *Handler) SetAutomationBuilderServices(drafts *service.AutomationDraftService, capabilities *service.AutomationCapabilitySnapshotBuilder, planner *service.AutomationPublicationPlanner, compiler *service.AutomationCompiler, confirmation *service.AutomationConfirmationService, lifecycle *service.AutomationLifecycleService) {
	h.automationDraftSvc = drafts
	h.automationCapabilitySvc = capabilities
	h.automationPlanner = planner
	h.automationCompiler = compiler
	h.automationConfirmationSvc = confirmation
	h.automationLifecycleSvc = lifecycle
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
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AutomationsContent(cards, projectID))
	}
	projects, _ := h.projectSvc.List(ctx)
	return render(c, http.StatusOK, pages.Automations(projects, projectID, cards))
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
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AutomationLiveContent(*graph, projectID))
	}
	projects, _ := h.projectSvc.List(ctx)
	return render(c, http.StatusOK, pages.AutomationLive(projects, projectID, *graph))
}

func (h *Handler) GetAutomationNodeResources(c echo.Context) error {
	if h.automationGraphSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automations unavailable")
	}
	ctx := c.Request().Context()
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	resources, err := h.automationGraphSvc.ListNodeResources(ctx, projectID, c.Param("automationId"), c.Param("nodeId"), 50)
	if err != nil {
		return err
	}
	if resources == nil {
		return echo.NewHTTPError(http.StatusNotFound, "automation node not found")
	}
	return render(c, http.StatusOK, pages.AutomationNodeResources(resources))
}

func automationHistoryError(err error) error {
	if errors.Is(err, repository.ErrAutomationCursor) {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid automation history cursor")
	}
	if errors.Is(err, repository.ErrAutomationWorkItemStatus) {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid automation work-item status")
	}
	return err
}

func (h *Handler) GetAutomationHistory(c echo.Context) error {
	if h.automationGraphSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automations unavailable")
	}
	ctx := c.Request().Context()
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	dashboard, err := h.automationGraphSvc.GetHistoryDashboard(ctx, projectID, c.Param("automationId"),
		c.QueryParam("invocation_cursor"), c.QueryParam("work_item_status"), c.QueryParam("work_item_cursor"), time.Now().UTC())
	if err != nil {
		return automationHistoryError(err)
	}
	if dashboard == nil {
		return echo.NewHTTPError(http.StatusNotFound, "automation not found")
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AutomationHistoryContent(*dashboard, projectID, c.QueryParam("work_item_status")))
	}
	projects, _ := h.projectSvc.List(ctx)
	return render(c, http.StatusOK, pages.AutomationHistory(projects, projectID, *dashboard, c.QueryParam("work_item_status")))
}

func (h *Handler) GetAutomationInvocationHistory(c echo.Context) error {
	if h.automationGraphSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automations unavailable")
	}
	ctx := c.Request().Context()
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	history, err := h.automationGraphSvc.GetInvocationHistory(ctx, projectID, c.Param("automationId"), c.Param("invocationId"), 20,
		c.QueryParam("cursor"), c.QueryParam("activity_cursor"))
	if err != nil {
		return automationHistoryError(err)
	}
	if history == nil {
		return echo.NewHTTPError(http.StatusNotFound, "automation invocation not found")
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AutomationInvocationHistoryContent(*history, projectID))
	}
	projects, _ := h.projectSvc.List(ctx)
	return render(c, http.StatusOK, pages.AutomationInvocationHistory(projects, projectID, *history))
}

func (h *Handler) GetAutomationWorkItemHistory(c echo.Context) error {
	if h.automationGraphSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automations unavailable")
	}
	ctx := c.Request().Context()
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	history, err := h.automationGraphSvc.GetWorkItemHistory(ctx, projectID, c.Param("automationId"), c.Param("workItemId"), 20,
		c.QueryParam("cursor"), c.QueryParam("activity_cursor"))
	if err != nil {
		return automationHistoryError(err)
	}
	if history == nil {
		return echo.NewHTTPError(http.StatusNotFound, "automation work item not found")
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AutomationWorkItemHistoryContent(*history, projectID))
	}
	projects, _ := h.projectSvc.List(ctx)
	return render(c, http.StatusOK, pages.AutomationWorkItemHistory(projects, projectID, *history))
}

func (h *Handler) GetAutomationDefinition(c echo.Context) error {
	if h.automationGraphSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automations unavailable")
	}
	ctx := c.Request().Context()
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	definition, resources, err := h.automationGraphSvc.GetDefinition(ctx, projectID, c.Param("automationId"))
	if err != nil {
		return err
	}
	if definition == nil {
		return echo.NewHTTPError(http.StatusNotFound, "automation not found")
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AutomationDefinitionContent(*definition, resources, projectID))
	}
	projects, _ := h.projectSvc.List(ctx)
	return render(c, http.StatusOK, pages.AutomationDefinition(projects, projectID, *definition, resources))
}

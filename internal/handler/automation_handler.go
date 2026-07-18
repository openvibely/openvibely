package handler

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) SetAutomationServices(graph *service.AutomationGraphService, registration *service.AutomationRegistrationService) {
	h.automationGraphSvc = graph
	h.automationRegistrationSvc = registration
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

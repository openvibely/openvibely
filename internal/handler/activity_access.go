package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service/authz"
)

func (h *Handler) authorizeActivityResource(c echo.Context) error {
	permission := authz.View
	switch c.Path() {
	case "/upcoming", "/history", "/schedule", "/schedule/calendar-state", "/insights", "/insights/by-type", "/insights/reports":
	case "/reviews/:id", "/schedule/calendar-action":
		permission = authz.Edit
	case "/upcoming/summary", "/history/summary", "/insights/analyze", "/insights/extract-knowledge", "/insights/health-check", "/history/grade-ideas", "/insights/:id/status", "/insights/:id", "/insights/knowledge/:id":
		permission = authz.Edit // Mutations and model-backed analysis require editor access.
		if strings.TrimSpace(c.QueryParam("project_id")) == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "missing project_id")
		}
	default:
		return nil
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	if err := h.projectAccessSvc.Require(c.Request().Context(), projectID, permission); err != nil {
		return err
	}
	query := c.QueryParams()
	query.Set("project_id", projectID)
	c.Request().URL.RawQuery = query.Encode()
	return nil
}

package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service/authz"
)

func (h *Handler) authorizeAutomationResource(c echo.Context) error {
	path := c.Path()
	if path != "/automations" && !strings.HasPrefix(path, "/automations/") {
		return nil
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	permission := authz.Manage
	if (c.Request().Method == http.MethodGet || c.Request().Method == http.MethodHead) && !strings.HasSuffix(path, "/builder") && !strings.HasSuffix(path, "/duplicate") {
		permission = authz.View
	}
	if err := h.projectAccessSvc.Require(c.Request().Context(), projectID, permission); err != nil {
		return err
	}
	// Keep handler resolution on precisely the project checked above.
	query := c.QueryParams()
	query.Set("project_id", projectID)
	c.Request().URL.RawQuery = query.Encode()
	if id := c.Param("automationId"); id != "" {
		return h.requireAutomationProject(c.Request().Context(), projectID, id)
	}
	return nil
}

func (h *Handler) requireAutomationProject(ctx context.Context, projectID, automationID string) error {
	if h.automationGraphSvc == nil {
		return authz.ErrDenied
	}
	definition, _, err := h.automationGraphSvc.GetDefinition(ctx, projectID, automationID)
	if err != nil {
		return err
	}
	if definition == nil || definition.Automation.ProjectID != projectID {
		return authz.ErrDenied
	}
	return nil
}

package handler

import (
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service/authz"
)

func (h *Handler) authorizeCapacityResource(c echo.Context) error {
	switch c.Path() {
	case "/workers", "/settings/workers", "/workers/stats/global", "/workers/stats/models", "/api/capacity/global", "/api/capacity/models", "/api/capacity/models/:modelId", "/settings/worktree":
		return h.requireCurrentOrgAdmin(c)
	case "/workers/projects/:projectId/limit":
		return h.projectAccessSvc.Require(c.Request().Context(), c.Param("projectId"), authz.Manage)
	case "/api/capacity/projects/:projectId":
		return h.projectAccessSvc.Require(c.Request().Context(), c.Param("projectId"), authz.View)
	}
	// Project collection projections filter their rows using current access.
	return nil
}

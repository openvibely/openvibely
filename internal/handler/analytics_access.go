package handler

import (
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// Account projections are instance-wide until credential ownership is implemented.
// A project grant must never authorize reading or consuming another account's limits.
func (h *Handler) authorizeAnalyticsResource(c echo.Context) error {
	path := c.Path()
	if path != "/analytics" && !strings.HasPrefix(path, "/api/analytics/") {
		return nil
	}
	account := path == "/api/analytics/usage/resets" || path == "/api/analytics/usage/resets/consume" ||
		(path == "/api/analytics/usage" && analyticsUsageProjection(c) != "local")
	if account {
		principal, ok := models.PrincipalFromContext(c.Request().Context())
		if !ok || principal.UserID == "" || h.organizationSvc == nil {
			return authz.ErrDenied
		}
		user, err := h.organizationSvc.Store.UserByID(c.Request().Context(), principal.UserID)
		if err != nil {
			return err
		}
		if user == nil || user.Disabled || !user.IsOrgAdmin() {
			return authz.ErrDenied
		}
		// Account inventory and resets have no project-scoped representation.
		if strings.TrimSpace(c.QueryParam("project_id")) == "" && path != "/api/analytics/usage" {
			return nil
		}
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	if err := h.projectAccessSvc.Require(c.Request().Context(), projectID, authz.View); err != nil {
		return err
	}
	query := c.QueryParams()
	query.Set("project_id", projectID)
	c.Request().URL.RawQuery = query.Encode()
	return nil
}

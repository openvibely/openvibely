package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
)

func (h *Handler) authorizeAlertResource(c echo.Context) error {
	path := c.Path()
	if path != "/alerts" && !strings.HasPrefix(path, "/alerts/") && path != "/api/alerts/pending-count" {
		return nil
	}
	// Preserve the JSON endpoint's explicit project requirement.
	if path == "/api/alerts/pending-count" && strings.TrimSpace(c.QueryParam("project_id")) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id required")
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	permission := authz.Edit
	if c.Request().Method == http.MethodGet || c.Request().Method == http.MethodHead {
		permission = authz.View
	}
	if err := h.projectAccessSvc.Require(c.Request().Context(), projectID, permission); err != nil {
		return err
	}
	query := c.QueryParams()
	query.Set("project_id", projectID)
	c.Request().URL.RawQuery = query.Encode()
	if id := c.Param("id"); id != "" {
		if h.alertSvc == nil {
			return authz.ErrDenied
		}
		alert, err := h.alertSvc.GetByID(c.Request().Context(), projectID, id)
		if errors.Is(err, persistence.ErrAlertNotFound) {
			return authz.ErrDenied
		}
		if err != nil {
			return err
		}
		if alert == nil {
			return authz.ErrDenied
		}
		if err := h.projectAccessSvc.Require(c.Request().Context(), alert.ProjectID, permission); err != nil {
			return err
		}
	}
	// Bulk operations validate all IDs transactionally in the scoped alert store.
	return nil
}

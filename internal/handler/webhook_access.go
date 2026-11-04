package handler

import (
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service/authz"
)

func (h *Handler) authorizeWebhookResource(c echo.Context) error {
	path := c.Path()
	if path != "/channels/webhooks" && !strings.HasPrefix(path, "/channels/webhooks/") {
		return nil
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	if formProject := strings.TrimSpace(c.FormValue("project_id")); formProject != "" && formProject != projectID {
		return authz.ErrDenied
	}
	// Detail includes the endpoint secret, so inspection requires management too.
	if err := h.projectAccessSvc.Require(c.Request().Context(), projectID, authz.Manage); err != nil {
		return err
	}
	query := c.QueryParams()
	query.Set("project_id", projectID)
	c.Request().URL.RawQuery = query.Encode()
	if id := c.Param("id"); id != "" {
		if h.webhookRepo == nil {
			return authz.ErrDenied
		}
		endpoint, err := h.webhookRepo.GetByIDForProject(c.Request().Context(), id, projectID)
		if err != nil {
			return err
		}
		if endpoint == nil {
			return authz.ErrDenied
		}
	}
	return nil
}

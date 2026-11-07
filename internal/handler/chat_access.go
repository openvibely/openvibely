package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// authorizeChatResource gates project chat, conversation model settings and
// outbound-target routes. The authorized project is pinned into the query so
// the handler cannot resolve a different selected project afterwards.
func (h *Handler) authorizeChatResource(c echo.Context) error {
	path := c.Path()
	method := c.Request().Method
	permission := authz.View
	formProject := false
	switch path {
	case "/chat", "/chat/output/:id", "/chat/composer-action", "/chat/pending-inputs", "/chat/attachments/:id/download",
		"/channels/outbound-targets", "/channels/outbound-targets/card":
	case "/chat/send", "/chat/stop", "/chat/steer", "/chat/attachments", "/chat/attachments/:id", "/chat/history":
		permission = authz.Edit
	case "/conversation/model-settings":
		if method != http.MethodGet && method != http.MethodHead {
			permission = authz.Edit
		}
	case "/channels/outbound-targets/:id/test":
		permission = authz.Edit
	case "/channels/outbound-targets/test-draft":
		permission, formProject = authz.Edit, true
	case "/channels/send-message-explicit-targets":
		permission, formProject = authz.Manage, true
	default:
		return nil
	}
	ctx := c.Request().Context()
	if formProject {
		if projectID := strings.TrimSpace(c.FormValue("project_id")); projectID != "" {
			return h.projectAccessSvc.Require(ctx, projectID, permission)
		}
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	if projectID == "" {
		return nil
	}
	if err := h.projectAccessSvc.Require(ctx, projectID, permission); err != nil {
		return err
	}
	if path == "/chat/output/:id" {
		if err := h.projectAccessSvc.ChatResourceAllowed(ctx, "execution", c.Param("id")); err != nil {
			return err
		}
	}
	query := c.QueryParams()
	query.Set("project_id", projectID)
	c.Request().URL.RawQuery = query.Encode()
	return nil
}

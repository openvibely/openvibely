package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// Global sender lists allow self-management; repositories enforce assignment ownership.
// GitHub retains its separate admin-managed reviewer trust boundary.
func (h *Handler) authorizeChannelTrustResource(c echo.Context) error {
	switch c.Path() {
	case "/channels/github/authorized-actors/:id/identity", "/channels/github/authorized-actors", "/channels/github/authorized-actors/:id",
		"/channels/telegram/authorized-users", "/channels/telegram/authorized-users/:id",
		"/channels/slack/authorized-users", "/channels/slack/authorized-users/:id", "/channels/slack/authorized-users/:id/identity",
		"/channels/discord/authorized-users/:id/identity", "/channels/discord/authorized-users", "/channels/discord/authorized-users/:id",
		"/channels/email/authorized-senders", "/channels/email/authorized-senders/:id":
	default:
		return nil
	}
	if !strings.Contains(c.Path(), "/github/") {
		return h.prepareChannelSenderOptions(c, "", false)
	}
	projectID := c.QueryParam("project_id")
	mutation := c.Request().Method != http.MethodGet
	if mutation && c.Request().Method != http.MethodDelete {
		projectID = c.FormValue("project_id")
	}
	return h.authorizeChannelTrustOperation(c, projectID, mutation)
}

// Also used by the CRUD handlers so direct callers cannot omit the route guard.
func (h *Handler) authorizeChannelTrustOperation(c echo.Context, projectID string, mutation bool) error {
	if h.projectAccessSvc == nil {
		return nil // Unconfigured embedded handlers retain compatibility.
	}
	if mutation {
		if err := h.requireCurrentOrgAdmin(c); err != nil {
			return err
		}
	}
	if strings.TrimSpace(projectID) == "" {
		return authz.ErrDenied
	}
	ctx := h.projectRuntimePrincipalContext(c.Request().Context())
	if err := h.projectAccessSvc.Require(ctx, projectID, authz.View); err != nil {
		return err
	}
	c.SetRequest(c.Request().WithContext(ctx))
	return nil
}

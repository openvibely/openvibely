package handler

import (
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// Agent definitions, skills, personalities, hooks, and plugins are instance-wide.
// A project maintainer grant must not authorize changing shared executable code.
func (h *Handler) authorizeSharedDefinitionResource(c echo.Context) error {
	path := c.Path()
	if h.personalGitHub != nil {
		switch path {
		case "/channels/github/identity":
			return nil
		case "/channels/github/configure":
			if c.FormValue("github_auth_mode") != "app" {
				return nil
			}
		case "/channels/github/remove", "/channels/github/disconnect":
			personal, err := h.personalGitHub.UsesPersonalPAT(c.Request().Context())
			if err != nil {
				return err
			}
			if personal {
				return nil
			}
		}
	}
	// OAuth callbacks authenticate using their single-use state, not a browser
	// session. Callback run-as checks belong in flow completion.
	if path == "/models/oauth/callback" {
		return nil
	}
	// Provider credentials and connection lifecycle affect every project. Keep
	// project-scoped webhooks, outbound targets, and trust lists separate.
	switch path {
	case "/channels/bulk", "/channels/x/authorized-users", "/channels/x/authorized-users/:id",
		"/channels/telegram", "/channels/telegram/test", "/channels/telegram/remove", "/channels/telegram/send-responses",
		"/channels/github/configure", "/channels/github/connect", "/channels/github/disconnect", "/channels/github/remove",
		"/channels/slack/configure", "/channels/slack/connect", "/channels/slack/disconnect", "/channels/slack/remove", "/channels/slack/test",
		"/channels/discord/configure", "/channels/discord/remove", "/channels/discord/test",
		"/channels/x/configure", "/channels/x/remove", "/channels/x/test",
		"/channels/email/configure", "/channels/email/remove", "/channels/email/test":
		return h.requireCurrentOrgAdmin(c)
	}
	protected := false
	for _, root := range []string{"/agents", "/skills", "/personality", "/models"} {
		if path == root || strings.HasPrefix(path, root+"/") {
			protected = true
			break
		}
	}
	if !protected {
		return nil
	}
	return h.requireCurrentOrgAdmin(c)
}

func (h *Handler) requireCurrentOrgAdmin(c echo.Context) error {
	ctx := h.projectRuntimePrincipalContext(c.Request().Context())
	principal, ok := models.PrincipalFromContext(ctx)
	if !ok || principal.UserID == "" || h.organizationSvc == nil {
		return authz.ErrDenied
	}
	user, err := h.organizationSvc.Store.UserByID(ctx, principal.UserID)
	if err != nil {
		return err
	}
	if user == nil || user.Disabled || !user.IsOrgAdmin() {
		return authz.ErrDenied
	}
	c.SetRequest(c.Request().WithContext(models.WithPrincipal(ctx, user.Principal)))
	return nil
}

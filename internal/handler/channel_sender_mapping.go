package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
)

func (h *Handler) prepareChannelSenderOptions(c echo.Context, _ string, _ bool) error {
	if h.organizationSvc == nil {
		return nil
	}
	ctx := h.projectRuntimePrincipalContext(c.Request().Context())
	p, ok := models.PrincipalFromContext(ctx)
	if !ok {
		return authz.ErrDenied
	}
	actor, err := h.organizationSvc.Store.UserByID(ctx, p.UserID)
	if err != nil {
		return err
	}
	if actor == nil || actor.Disabled || actor.Role == "" {
		return authz.ErrDenied
	}
	options := models.ChannelSenderOptions{Actor: actor.Principal, Authenticated: true}
	if actor.IsOrgAdmin() {
		options.Users, err = h.organizationSvc.Store.Members(ctx)
		if err != nil {
			return err
		}
	} else {
		options.Users = []models.OrgUser{*actor}
	}
	ctx = models.WithPrincipal(ctx, actor.Principal)
	c.SetRequest(c.Request().WithContext(models.WithChannelSenderOptions(ctx, options)))
	return nil
}
func senderMappingForm(c echo.Context) models.ChannelSenderMapping {
	return models.ChannelSenderMapping{ApplicationUserID: strings.TrimSpace(c.FormValue("application_user_id")), TeamID: strings.TrimSpace(c.FormValue("team_id"))}
}

type channelSenderBinder interface {
	BindSender(context.Context, string, string, string) error
}

func (h *Handler) BindChannelSender(c echo.Context) error {
	if err := h.prepareChannelSenderOptions(c, "", true); err != nil {
		return projectAccessHTTPError(err)
	}
	var repo any
	var reload func(echo.Context, string) error
	switch c.Param("channel") {
	case "telegram":
		repo = h.telegramAuthRepo
		reload = h.telegramAuthorizedUserCRUD().reload
	case "slack":
		repo = h.slackAuthRepo
		reload = h.slackAuthorizedUserCRUD().reload
	case "discord":
		repo = h.discordAuthRepo
		reload = h.discordAuthorizedUserCRUD().reload
	case "email":
		repo = h.emailAuthRepo
		reload = h.emailAuthorizedUserCRUD().reload
	default:
		return echo.ErrNotFound
	}
	binder, ok := repo.(channelSenderBinder)
	if !ok {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "Channel sender mappings unavailable")
	}
	m := senderMappingForm(c)
	if err := binder.BindSender(c.Request().Context(), c.Param("id"), m.ApplicationUserID, m.TeamID); err != nil {
		return projectAccessHTTPError(err)
	}
	return reload(c, "")
}

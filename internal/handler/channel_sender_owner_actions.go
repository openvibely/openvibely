package handler

import (
	"context"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/web/templates/components"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type channelSenderArgs struct {
	ProjectID, ID, Provider, Operation string
	Mapping                            models.ChannelSenderMapping
	Values                             url.Values `remote:"raw"`
}
type channelSenderResult struct {
	Slack    []models.SlackAuthorizedUser
	Telegram []models.TelegramAuthorizedUser
	Discord  []models.DiscordAuthorizedUser
	Email    []models.EmailAuthorizedSender
	Options  models.ChannelSenderOptions
}

var actChannelSender = defineAction("channel.sender", func(h *Handler, ctx context.Context, a channelSenderArgs) (string, authz.Permission, error) {
	return a.ProjectID, authz.View, nil
}, func(h *Handler, ctx context.Context, a channelSenderArgs) (channelSenderResult, error) {
	out := channelSenderResult{}
	if h.organizationSvc != nil {
		principal, ok := models.PrincipalFromContext(ctx)
		if !ok {
			return out, authz.ErrDenied
		}
		actor, err := h.organizationSvc.Store.UserByID(ctx, principal.UserID)
		if err != nil {
			return out, err
		}
		if actor == nil || actor.Disabled || actor.Role == "" {
			return out, authz.ErrDenied
		}
		out.Options = models.ChannelSenderOptions{Actor: actor.Principal, Authenticated: true}
		if actor.IsOrgAdmin() {
			out.Options.Users, err = h.organizationSvc.Store.Members(ctx)
			if err != nil {
				return out, err
			}
		} else {
			out.Options.Users = []models.OrgUser{*actor}
		}
		ctx = models.WithPrincipal(ctx, actor.Principal)
	}
	var err error
	switch a.Provider {
	case "slack":
		if h.slackAuthRepo == nil {
			return out, statusErr(http.StatusServiceUnavailable, "channel authorization unavailable")
		}
		switch a.Operation {
		case "create":
			value := strings.TrimSpace(a.Values.Get("slack_user_id"))
			if value == "" {
				return out, statusErr(http.StatusBadRequest, "sender identity is required")
			}
			user := &models.SlackAuthorizedUser{ChannelSenderMapping: a.Mapping, ProjectID: a.ProjectID, AddedBy: "web", DisplayName: strings.TrimSpace(a.Values.Get("display_name"))}
			user.SlackUserID = value
			if user.DisplayName == "" {
				user.DisplayName = value
			}
			err = h.slackAuthRepo.Create(ctx, user)
		case "delete":
			user, e := h.slackAuthRepo.GetByID(ctx, a.ID)
			if e != nil {
				return out, e
			}
			if user == nil {
				return out, statusErr(http.StatusNotFound, "User not found")
			}
			err = h.slackAuthRepo.DeleteForProject(ctx, a.ProjectID, a.ID)
		case "bind-application":
			if err = h.requireActionAdmin(ctx); err == nil {
				err = h.slackAuthRepo.BindApplicationUser(ctx, a.ProjectID, a.ID, a.Mapping.TeamID, a.Mapping.ApplicationUserID)
			}
		case "bind":
			err = bindOwnerSender(ctx, h.slackAuthRepo, a.ID, a.Mapping)
		case "list":
		default:
			return out, statusErr(http.StatusBadRequest, "unknown sender operation")
		}
		if err != nil {
			return out, err
		}
		out.Slack, err = h.slackAuthRepo.ListByProject(ctx, a.ProjectID)
	case "telegram":
		if h.telegramAuthRepo == nil {
			return out, statusErr(http.StatusServiceUnavailable, "channel authorization unavailable")
		}
		switch a.Operation {
		case "create":
			value := strings.TrimSpace(a.Values.Get("user_id_or_username"))
			if value == "" {
				return out, statusErr(http.StatusBadRequest, "sender identity is required")
			}
			user := &models.TelegramAuthorizedUser{ChannelSenderMapping: a.Mapping, ProjectID: a.ProjectID, AddedBy: "web", DisplayName: strings.TrimSpace(a.Values.Get("display_name"))}
			if id, e := strconv.ParseInt(value, 10, 64); e == nil {
				user.TelegramUserID = id
			} else {
				user.TelegramUsername = strings.ToLower(strings.TrimPrefix(value, "@"))
			}
			if user.DisplayName == "" {
				user.DisplayName = value
			}
			err = h.telegramAuthRepo.Create(ctx, user)
		case "delete":
			user, e := h.telegramAuthRepo.GetByID(ctx, a.ID)
			if e != nil {
				return out, e
			}
			if user == nil {
				return out, statusErr(http.StatusNotFound, "User not found")
			}
			err = h.telegramAuthRepo.DeleteForProject(ctx, a.ProjectID, a.ID)
		case "bind":
			err = bindOwnerSender(ctx, h.telegramAuthRepo, a.ID, a.Mapping)
		case "list":
		default:
			return out, statusErr(http.StatusBadRequest, "unknown sender operation")
		}
		if err != nil {
			return out, err
		}
		out.Telegram, err = h.telegramAuthRepo.ListByProject(ctx, a.ProjectID)
	case "discord":
		if h.discordAuthRepo == nil {
			return out, statusErr(http.StatusServiceUnavailable, "channel authorization unavailable")
		}
		switch a.Operation {
		case "create":
			value := strings.TrimSpace(a.Values.Get("discord_user_id"))
			if value == "" {
				return out, statusErr(http.StatusBadRequest, "sender identity is required")
			}
			user := &models.DiscordAuthorizedUser{ChannelSenderMapping: a.Mapping, ProjectID: a.ProjectID, AddedBy: "web", DisplayName: strings.TrimSpace(a.Values.Get("display_name"))}
			if !discordNumericUserIDPattern.MatchString(value) {
				return out, statusErr(http.StatusBadRequest, "Discord user ID must be numeric")
			}
			user.DiscordUserID = value
			if user.DisplayName == "" {
				user.DisplayName = value
			}
			err = h.discordAuthRepo.Create(ctx, user)
		case "delete":
			user, e := h.discordAuthRepo.GetByID(ctx, a.ID)
			if e != nil {
				return out, e
			}
			if user == nil {
				return out, statusErr(http.StatusNotFound, "User not found")
			}
			err = h.discordAuthRepo.DeleteForProject(ctx, a.ProjectID, a.ID)
		case "bind":
			err = bindOwnerSender(ctx, h.discordAuthRepo, a.ID, a.Mapping)
		case "list":
		default:
			return out, statusErr(http.StatusBadRequest, "unknown sender operation")
		}
		if err != nil {
			return out, err
		}
		out.Discord, err = h.discordAuthRepo.ListByProject(ctx, a.ProjectID)
	case "email":
		if h.emailAuthRepo == nil {
			return out, statusErr(http.StatusServiceUnavailable, "channel authorization unavailable")
		}
		switch a.Operation {
		case "create":
			value := strings.TrimSpace(a.Values.Get("authorized_email_address"))
			if value == "" {
				return out, statusErr(http.StatusBadRequest, "sender identity is required")
			}
			user := &models.EmailAuthorizedSender{ChannelSenderMapping: a.Mapping, ProjectID: a.ProjectID, AddedBy: "web", DisplayName: strings.TrimSpace(a.Values.Get("display_name"))}
			value = persistence.NormalizeEmailAddress(value)
			user.EmailAddress = value
			if user.DisplayName == "" {
				user.DisplayName = value
			}
			err = h.emailAuthRepo.Create(ctx, user)
		case "delete":
			user, e := h.emailAuthRepo.GetByID(ctx, a.ID)
			if e != nil {
				return out, e
			}
			if user == nil {
				return out, statusErr(http.StatusNotFound, "User not found")
			}
			err = h.emailAuthRepo.DeleteForProject(ctx, a.ProjectID, a.ID)
		case "bind":
			err = bindOwnerSender(ctx, h.emailAuthRepo, a.ID, a.Mapping)
		case "list":
		default:
			return out, statusErr(http.StatusBadRequest, "unknown sender operation")
		}
		if err != nil {
			return out, err
		}
		out.Email, err = h.emailAuthRepo.ListByProject(ctx, a.ProjectID)
	default:
		return out, statusErr(http.StatusBadRequest, "unsupported provider")
	}
	return out, err
})

func (h *Handler) remoteChannelSender(c echo.Context, provider, operation string) (bool, error) {
	project := c.QueryParam("project_id")
	if operation == "create" || operation == "bind" || operation == "bind-application" {
		if value := c.FormValue("project_id"); value != "" {
			project = value
		}
	}
	if project == "" {
		project, _ = h.getCurrentProjectID(c)
	}
	if _, remote := remoteid.ServerOf(project); !remote {
		return false, nil
	}
	values, err := c.FormParams()
	if err != nil {
		return true, err
	}
	out, err := actChannelSender(h, c.Request().Context(), channelSenderArgs{ProjectID: project, ID: c.Param("id"), Provider: provider, Operation: operation, Values: values, Mapping: senderMappingForm(c)})
	if err != nil {
		return true, err
	}
	c.SetRequest(c.Request().WithContext(models.WithChannelSenderOptions(c.Request().Context(), out.Options)))
	switch provider {
	case "slack":
		return true, render(c, http.StatusOK, components.SlackAuthorizedUsersList(out.Slack, project))
	case "telegram":
		return true, render(c, http.StatusOK, components.TelegramAuthorizedUsersList(out.Telegram, project))
	case "discord":
		return true, render(c, http.StatusOK, components.DiscordAuthorizedUsersList(out.Discord, project))
	case "email":
		return true, render(c, http.StatusOK, components.EmailAuthorizedSendersList(out.Email, project))
	}
	return true, echo.ErrNotFound
}

func bindOwnerSender(ctx context.Context, repo any, id string, mapping models.ChannelSenderMapping) error {
	binder, ok := repo.(channelSenderBinder)
	if !ok {
		return statusErr(http.StatusServiceUnavailable, "Channel sender mappings unavailable")
	}
	return binder.BindSender(ctx, id, mapping.ApplicationUserID, mapping.TeamID)
}

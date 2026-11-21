package handler

import (
	"context"
	"net/http"
	"net/url"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
)

func (h *Handler) removeRemoteChannel(c echo.Context, provider string) (bool, error) {
	if h.settingsRepo == nil {
		return false, nil
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return true, err
	}
	if _, remote := remoteid.ServerOf(projectID); !remote {
		return false, nil
	}
	if _, err := actChannelsBulk(h, c.Request().Context(), cardBulkArgs{ProjectID: projectID, IDs: []string{"channel:" + provider}}); err != nil {
		return true, err
	}
	return true, returnToChannels(c)
}

type channelConfigureArgs struct {
	ProjectID, Provider string
	Values              url.Values `remote:"raw"`
}

var actChannelConfigure = defineAction("channel.configure", func(h *Handler, ctx context.Context, a channelConfigureArgs) (string, authz.Permission, error) {
	if a.Provider == "github" && h.personalGitHub != nil && service.NormalizeGitHubAuthMode(a.Values.Get("github_auth_mode")) == service.GitHubAuthModePAT {
		personal, err := h.personalGitHub.UsesPersonalPAT(ctx)
		if err != nil {
			return "", authz.View, err
		}
		if personal {
			return a.ProjectID, authz.View, nil
		}
	}
	return h.instanceSettingsOwner(ctx, a.ProjectID)
}, func(h *Handler, ctx context.Context, a channelConfigureArgs) (struct{}, error) {
	var err error
	switch a.Provider {
	case "telegram-responses":
		value := "false"
		if a.Values.Get("enabled") == "true" {
			value = "true"
		}
		if h.settingsRepo != nil {
			err = h.settingsRepo.Set(ctx, service.TelegramSettingSendResponses, value)
		}
	case "telegram":
		err = h.configureTelegramOnOwner(ctx, a.Values)
	case "x":
		err = h.configureXOnOwner(ctx, ownerFormValues(a.Values))
	case "github":
		err = h.configureGitHubOnOwner(ctx, a.Values)
	case "slack":
		err = h.configureSlackOnOwner(ctx, a.Values)
	case "discord":
		err = h.configureDiscordOnOwner(ctx, a.Values)
	case "email":
		err = h.configureEmailOnOwner(ctx, a.Values)
	default:
		err = statusErr(http.StatusBadRequest, "unsupported channel provider")
	}
	return struct{}{}, err
})

type ownerFormValues url.Values

func (v ownerFormValues) FormValue(key string) string { return url.Values(v).Get(key) }

func (v ownerFormValues) FormParams() (url.Values, error) { return url.Values(v), nil }

type ownerForm interface {
	FormValue(string) string
	FormParams() (url.Values, error)
}

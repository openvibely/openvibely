package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service/authz"
)

type channelTrustArgs struct {
	ProjectID, ID, ApplicationUserID, Provider, Operation string
	ExternalID, Name, Permission, Host                    string `remote:"raw"`
	Attested                                              bool
}

var actChannelTrust = defineAction("channel.trust", func(h *Handler, ctx context.Context, a channelTrustArgs) (string, authz.Permission, error) {
	if a.Provider == "github" {
		return h.instanceSettingsOwner(ctx, a.ProjectID)
	}
	return a.ProjectID, authz.View, nil
}, func(h *Handler, ctx context.Context, a channelTrustArgs) (struct{}, error) {
	var err error
	switch a.Provider {
	case "github":
		if h.githubAuthRepo == nil {
			return struct{}{}, statusErr(http.StatusServiceUnavailable, "GitHub authorization unavailable")
		}
		switch a.Operation {
		case "create":
			login := persistence.NormalizeGitHubLogin(a.ExternalID)
			if login == "" {
				return struct{}{}, statusErr(http.StatusBadRequest, "GitHub login is required")
			}
			name := strings.TrimSpace(a.Name)
			if name == "" {
				name = login
			}
			permission := strings.ToLower(strings.TrimSpace(a.Permission))
			if permission != "admin" && permission != "approve" {
				permission = "triage"
			}
			err = h.githubAuthRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: login, DisplayName: name, Permission: permission, AddedBy: "web"})
		case "delete":
			err = h.githubAuthRepo.DeleteAuthorizedActor(ctx, a.ID)
		case "bind":
			id, e := strconv.ParseInt(a.ExternalID, 10, 64)
			if e != nil || id <= 0 || !a.Attested {
				return struct{}{}, statusErr(http.StatusBadRequest, "Confirm the numeric GitHub user ID and application user binding")
			}
			err = h.githubAuthRepo.BindReviewerIdentity(ctx, models.GitHubReviewerIdentity{ProjectID: a.ProjectID, ActorID: a.ID, Host: a.Host, ExternalUserID: id, ApplicationUserID: a.ApplicationUserID})
		default:
			return struct{}{}, statusErr(http.StatusBadRequest, "unknown trust operation")
		}
	case "x":
		if h.xAuthRepo == nil {
			return struct{}{}, statusErr(http.StatusServiceUnavailable, "X authorization unavailable")
		}
		switch a.Operation {
		case "create":
			if _, e := strconv.ParseUint(a.ExternalID, 10, 64); e != nil {
				return struct{}{}, statusErr(http.StatusBadRequest, "X user ID must be numeric")
			}
			err = h.xAuthRepo.Create(ctx, &models.XAuthorizedUser{ProjectID: a.ProjectID, XUserID: a.ExternalID, Username: strings.TrimPrefix(strings.TrimSpace(a.Name), "@"), ApplicationUserID: a.ApplicationUserID})
		case "delete":
			err = h.xAuthRepo.Delete(ctx, a.ProjectID, a.ID)
		default:
			return struct{}{}, statusErr(http.StatusBadRequest, "unknown trust operation")
		}
	default:
		return struct{}{}, statusErr(http.StatusBadRequest, "unknown trust provider")
	}
	return struct{}{}, err
})

func (h *Handler) remoteChannelTrust(c echo.Context, provider, operation string) (bool, error) {
	project := c.FormValue("project_id")
	if project == "" {
		project = c.QueryParam("project_id")
	}
	if project == "" && h.projectSvc != nil {
		project, _ = h.getCurrentProjectID(c)
	}
	if _, remote := remoteid.ServerOf(project); !remote {
		return false, nil
	}
	a := channelTrustArgs{ProjectID: project, ID: c.Param("id"), Provider: provider, Operation: operation, ApplicationUserID: strings.TrimSpace(c.FormValue("application_user_id"))}
	if provider == "github" {
		a.ExternalID = c.FormValue("github_login")
		a.Name = c.FormValue("display_name")
		a.Permission = c.FormValue("permission")
		if operation == "bind" {
			a.ExternalID = c.FormValue("external_user_id")
			a.Host = c.FormValue("host")
			a.Attested = c.FormValue("attested") == "true"
		}
	} else {
		a.ExternalID = strings.TrimSpace(c.FormValue("x_user_id"))
		a.Name = c.FormValue("x_username")
	}
	if _, err := actChannelTrust(h, c.Request().Context(), a); err != nil {
		return true, err
	}
	if provider == "github" {
		return true, h.renderGitHubRuntimeSettings(c, project)
	}
	return true, returnToChannels(c)
}

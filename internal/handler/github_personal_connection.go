package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
)

func (h *Handler) SetPersonalGitHubService(s *service.GitHubService) { h.personalGitHub = s }

func (h *Handler) savePersonalGitHub(c echo.Context) error {
	ctx := h.projectRuntimePrincipalContext(c.Request().Context())
	personalMode, err := h.personalGitHub.UsesPersonalPAT(ctx)
	if err != nil {
		return err
	}
	if !personalMode {
		// Switching a shared App installation back to PAT mode remains an
		// organization-admin action, even though the PAT itself is personal.
		if err := h.requireCurrentOrgAdmin(c); err != nil {
			return projectAccessResponse(c, err)
		}
	}
	existing, err := h.personalGitHub.PersonalConnection(ctx)
	if err != nil {
		return projectAccessResponse(c, err)
	}
	revision, err := strconv.ParseInt(c.FormValue("github_revision"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(400, "Refresh GitHub settings before saving")
	}
	pat := strings.TrimSpace(c.FormValue("github_pat"))
	login := existing.Login
	if pat == "" {
		pat = existing.PAT
	} else if pat != existing.PAT {
		login = ""
	}
	connection := models.GitHubConnection{PAT: pat, Login: login, AuthorName: c.FormValue("github_author_name"), AuthorEmail: c.FormValue("github_author_email"), APIEndpoint: c.FormValue("github_api_endpoint")}
	changed, err := h.personalGitHub.SavePersonalConnection(ctx, connection, revision)
	if errors.Is(err, persistence.ErrProjectDenied) {
		return projectAccessResponse(c, err)
	}
	if err != nil {
		return echo.NewHTTPError(400, err.Error())
	}
	if !changed {
		return echo.NewHTTPError(409, "GitHub settings changed. Reopen them before saving.")
	}
	if !personalMode {
		if err := h.settingsRepo.Set(ctx, service.GitHubSettingAuthMode, service.GitHubAuthModePAT); err != nil {
			return echo.NewHTTPError(500, "Personal connection saved, but the shared authentication mode could not be changed. Reopen GitHub settings.")
		}
	}
	return returnToChannels(c)
}
func (h *Handler) lookupPersonalGitHub(c echo.Context) error {
	if h.personalGitHub == nil {
		return echo.NewHTTPError(503, "Personal GitHub connections are not available")
	}
	ctx := h.projectRuntimePrincipalContext(c.Request().Context())
	existing, err := h.personalGitHub.PersonalConnection(ctx)
	if err != nil {
		return projectAccessResponse(c, err)
	}
	token := strings.TrimSpace(c.FormValue("github_pat"))
	if token == "" {
		token = existing.PAT
	}
	identity, err := h.personalGitHub.LookupPersonalGitHubIdentity(ctx, token, c.FormValue("github_api_endpoint"))
	if err != nil {
		return echo.NewHTTPError(400, err.Error())
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, identity)
}
func (h *Handler) removePersonalGitHub(c echo.Context) error {
	ctx := h.projectRuntimePrincipalContext(c.Request().Context())
	existing, err := h.personalGitHub.PersonalConnection(ctx)
	if err != nil {
		return projectAccessResponse(c, err)
	}
	changed, err := h.personalGitHub.DeletePersonalConnection(ctx, existing.Revision)
	if err != nil {
		return projectAccessResponse(c, err)
	}
	if !changed && existing.Revision != 0 {
		return echo.NewHTTPError(409, "GitHub settings changed. Reopen them before disconnecting.")
	}
	return returnToChannels(c)
}

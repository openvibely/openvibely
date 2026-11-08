package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/web/templates/components"
)

func (h *Handler) githubRuntimeSettingsPreflight(projectID string) (persistence.GitHubAuthStore, error) {
	if h.githubAuthRepo == nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "GitHub auth not configured")
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}
	return h.githubAuthRepo, nil
}

func (h *Handler) renderGitHubRuntimeSettings(c echo.Context, projectID string) error {
	if h.githubAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "GitHub auth not configured")
	}
	actors, err := h.githubAuthRepo.ListAuthorizedActors(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load GitHub authorized actors")
	}
	bindings, err := h.githubAuthRepo.ListReviewerIdentities(c.Request().Context(), projectID)
	if err != nil {
		return err
	}
	for i := range actors {
		for j := range bindings {
			if actors[i].ID == bindings[j].ActorID {
				actors[i].ReviewerIdentity = &bindings[j]
			}
		}
	}
	var inbox *models.GitHubProjectInbox
	if strings.TrimSpace(projectID) != "" {
		inbox, err = h.githubAuthRepo.GetProjectInbox(c.Request().Context(), projectID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load GitHub project inbox")
		}
	}
	return render(c, http.StatusOK, components.GitHubRuntimeSettings(actors, inbox, projectID))
}

// GitHubRuntimeSettingsFragment returns generic GitHub runtime trust/inbox settings.
func (h *Handler) GitHubRuntimeSettingsFragment(c echo.Context) error {
	projectID := strings.TrimSpace(c.QueryParam("project_id"))
	if err := h.authorizeChannelTrustOperation(c, projectID, false); err != nil {
		return err
	}
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}
	return h.renderGitHubRuntimeSettings(c, projectID)
}

// AddGitHubAuthorizedActor adds or updates a system-level GitHub authorized actor.
func (h *Handler) AddGitHubAuthorizedActor(c echo.Context) error {
	projectID := strings.TrimSpace(c.FormValue("project_id"))
	if err := h.authorizeChannelTrustOperation(c, projectID, true); err != nil {
		return err
	}
	authRepo, err := h.githubRuntimeSettingsPreflight(projectID)
	if err != nil {
		return err
	}
	githubLogin := persistence.NormalizeGitHubLogin(c.FormValue("github_login"))
	if githubLogin == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "GitHub login is required")
	}
	displayName := strings.TrimSpace(c.FormValue("display_name"))
	if displayName == "" {
		displayName = githubLogin
	}
	permission := strings.ToLower(strings.TrimSpace(c.FormValue("permission")))
	switch permission {
	case "triage", "approve", "admin":
	default:
		permission = "triage"
	}
	actor := &models.GitHubAuthorizedActor{
		GitHubLogin: githubLogin,
		DisplayName: displayName,
		Permission:  permission,
		AddedBy:     "web",
	}
	if err := authRepo.UpsertAuthorizedActor(c.Request().Context(), actor); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to add GitHub authorized actor: "+err.Error())
	}
	return h.renderGitHubRuntimeSettings(c, projectID)
}

// RemoveGitHubAuthorizedActor removes a system-level GitHub authorized actor.
func (h *Handler) RemoveGitHubAuthorizedActor(c echo.Context) error {
	projectID := strings.TrimSpace(c.QueryParam("project_id"))
	if err := h.authorizeChannelTrustOperation(c, projectID, true); err != nil {
		return err
	}
	authRepo, err := h.githubRuntimeSettingsPreflight(projectID)
	if err != nil {
		return err
	}
	if err := authRepo.DeleteAuthorizedActor(c.Request().Context(), c.Param("id")); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to remove GitHub authorized actor: "+err.Error())
	}
	return h.renderGitHubRuntimeSettings(c, projectID)
}

// SaveGitHubProjectInbox stores the project-scoped authorized GitHub inbox assignee for runtime prompts/tools.
func (h *Handler) SaveGitHubProjectInbox(c echo.Context) error {
	projectID := strings.TrimSpace(c.FormValue("project_id"))
	if h.projectAccessSvc != nil {
		ctx := h.projectRuntimePrincipalContext(c.Request().Context())
		if err := h.projectAccessSvc.Require(ctx, projectID, authz.Manage); err != nil {
			return err
		}
		c.SetRequest(c.Request().WithContext(ctx))
	}
	authRepo, err := h.githubRuntimeSettingsPreflight(projectID)
	if err != nil {
		return err
	}
	githubLogin := persistence.NormalizeGitHubLogin(c.FormValue("github_login"))
	if githubLogin == "" {
		existing, err := authRepo.GetProjectInbox(c.Request().Context(), projectID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load GitHub project inbox: "+err.Error())
		}
		if existing != nil {
			existing.Enabled = false
			if err := authRepo.UpsertProjectInbox(c.Request().Context(), existing); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed to save GitHub project inbox: "+err.Error())
			}
		}
		return h.renderGitHubRuntimeSettings(c, projectID)
	}
	inbox := &models.GitHubProjectInbox{
		ProjectID:   projectID,
		GitHubLogin: githubLogin,
		Enabled:     c.FormValue("enabled") == "true",
	}
	if err := authRepo.UpsertProjectInbox(c.Request().Context(), inbox); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to save GitHub project inbox: "+err.Error())
	}
	return h.renderGitHubRuntimeSettings(c, projectID)
}

// BindGitHubReviewerIdentity requires explicit administrator attestation of the
// provider host, immutable numeric identity, and project application user.
func (h *Handler) BindGitHubReviewerIdentity(c echo.Context) error {
	projectID := strings.TrimSpace(c.FormValue("project_id"))
	if err := h.authorizeChannelTrustOperation(c, projectID, true); err != nil {
		return err
	}
	repo, err := h.githubRuntimeSettingsPreflight(projectID)
	if err != nil {
		return err
	}
	externalID, err := strconv.ParseInt(c.FormValue("external_user_id"), 10, 64)
	if err != nil || externalID <= 0 || c.FormValue("attested") != "true" {
		return echo.NewHTTPError(http.StatusBadRequest, "Confirm the numeric GitHub user ID and application user binding")
	}
	if err := repo.BindReviewerIdentity(c.Request().Context(), models.GitHubReviewerIdentity{ProjectID: projectID, ActorID: c.Param("id"), Host: c.FormValue("host"), ExternalUserID: externalID, ApplicationUserID: strings.TrimSpace(c.FormValue("application_user_id"))}); err != nil {
		return err
	}
	return h.renderGitHubRuntimeSettings(c, projectID)
}

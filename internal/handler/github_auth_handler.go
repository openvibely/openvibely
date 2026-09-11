package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/web/templates/components"
)

func (h *Handler) githubRuntimeSettingsPreflight(projectID string) (*repository.GitHubAuthRepo, error) {
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
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}
	return h.renderGitHubRuntimeSettings(c, projectID)
}

// AddGitHubAuthorizedActor adds or updates a system-level GitHub authorized actor.
func (h *Handler) AddGitHubAuthorizedActor(c echo.Context) error {
	projectID := strings.TrimSpace(c.FormValue("project_id"))
	authRepo, err := h.githubRuntimeSettingsPreflight(projectID)
	if err != nil {
		return err
	}
	githubLogin := repository.NormalizeGitHubLogin(c.FormValue("github_login"))
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
	authRepo, err := h.githubRuntimeSettingsPreflight(projectID)
	if err != nil {
		return err
	}
	githubLogin := repository.NormalizeGitHubLogin(c.FormValue("github_login"))
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

package handler

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
)

var discordNumericUserIDPattern = regexp.MustCompile(`^[0-9]+$`)

// ListDiscordAuthorizedUsers returns the authorized Discord users list for a project.
func (h *Handler) ListDiscordAuthorizedUsers(c echo.Context) error {
	if h.discordAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Discord auth not configured")
	}
	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}
	users, err := h.discordAuthRepo.ListByProject(c.Request().Context(), projectID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load authorized users")
	}
	return render(c, http.StatusOK, components.DiscordAuthorizedUsersList(users, projectID))
}

// AddDiscordAuthorizedUser adds a new authorized Discord user.
func (h *Handler) AddDiscordAuthorizedUser(c echo.Context) error {
	if h.discordAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Discord auth not configured")
	}
	projectID := c.FormValue("project_id")
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}
	discordUserID := strings.TrimSpace(c.FormValue("discord_user_id"))
	displayName := strings.TrimSpace(c.FormValue("display_name"))
	if discordUserID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Discord user ID is required")
	}
	if !discordNumericUserIDPattern.MatchString(discordUserID) {
		return echo.NewHTTPError(http.StatusBadRequest, "Discord user ID must be the numeric ID copied from Discord Developer Mode")
	}
	user := &models.DiscordAuthorizedUser{ProjectID: projectID, DiscordUserID: discordUserID, DisplayName: displayName, AddedBy: "web"}
	if user.DisplayName == "" {
		user.DisplayName = discordUserID
	}
	if err := h.discordAuthRepo.Create(c.Request().Context(), user); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to add authorized user: "+err.Error())
	}
	users, err := h.discordAuthRepo.ListByProject(c.Request().Context(), projectID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load authorized users")
	}
	return render(c, http.StatusOK, components.DiscordAuthorizedUsersList(users, projectID))
}

// RemoveDiscordAuthorizedUser removes an authorized Discord user.
func (h *Handler) RemoveDiscordAuthorizedUser(c echo.Context) error {
	if h.discordAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Discord auth not configured")
	}
	id := c.Param("id")
	projectID := c.QueryParam("project_id")
	user, err := h.discordAuthRepo.GetByID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to find user")
	}
	if user == nil {
		return echo.NewHTTPError(http.StatusNotFound, "User not found")
	}
	if projectID == "" {
		projectID = user.ProjectID
	}
	if err := h.discordAuthRepo.Delete(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to remove user: "+err.Error())
	}
	users, err := h.discordAuthRepo.ListByProject(c.Request().Context(), projectID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load authorized users")
	}
	return render(c, http.StatusOK, components.DiscordAuthorizedUsersList(users, projectID))
}

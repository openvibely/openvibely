package handler

import (
	"context"
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
	return h.discordAuthorizedUserCRUD().listUsers(c, c.QueryParam("project_id"))
}

// AddDiscordAuthorizedUser adds a new authorized Discord user.
func (h *Handler) AddDiscordAuthorizedUser(c echo.Context) error {
	if h.discordAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Discord auth not configured")
	}

	projectID := c.FormValue("project_id")
	return h.discordAuthorizedUserCRUD().createUser(c, projectID, func(ctx context.Context) error {
		discordUserID := strings.TrimSpace(c.FormValue("discord_user_id"))
		displayName := strings.TrimSpace(c.FormValue("display_name"))
		if discordUserID == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "Discord user ID is required")
		}
		if !discordNumericUserIDPattern.MatchString(discordUserID) {
			return echo.NewHTTPError(http.StatusBadRequest, "Discord user ID must be the numeric ID copied from Discord Developer Mode")
		}

		user := &models.DiscordAuthorizedUser{
			ProjectID:     projectID,
			DiscordUserID: discordUserID,
			DisplayName:   displayName,
			AddedBy:       "web",
		}
		if user.DisplayName == "" {
			user.DisplayName = discordUserID
		}
		return h.discordAuthRepo.Create(ctx, user)
	})
}

// RemoveDiscordAuthorizedUser removes an authorized Discord user.
func (h *Handler) RemoveDiscordAuthorizedUser(c echo.Context) error {
	if h.discordAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Discord auth not configured")
	}
	return h.discordAuthorizedUserCRUD().deleteUser(
		c,
		c.Param("id"),
		c.QueryParam("project_id"),
		authorizedUserProjectLookup(h.discordAuthRepo.GetByID, func(user *models.DiscordAuthorizedUser) string {
			return user.ProjectID
		}),
		h.discordAuthRepo.Delete,
	)
}

func (h *Handler) discordAuthorizedUserCRUD() authorizedUserCRUD[models.DiscordAuthorizedUser] {
	return authorizedUserCRUD[models.DiscordAuthorizedUser]{
		list:   h.discordAuthRepo.ListByProject,
		render: components.DiscordAuthorizedUsersList,
	}
}

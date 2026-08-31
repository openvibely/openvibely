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
	return h.discordAuthorizedUserCRUD().listHandler(c)
}

// AddDiscordAuthorizedUser adds a new authorized Discord user.
func (h *Handler) AddDiscordAuthorizedUser(c echo.Context) error {
	return h.discordAuthorizedUserCRUD().createHandler(c, func(ctx context.Context, projectID string) error {
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
	return h.discordAuthorizedUserCRUD().deleteHandler(c)
}

func (h *Handler) discordAuthorizedUserCRUD() authorizedUserCRUD[models.DiscordAuthorizedUser] {
	crud := authorizedUserCRUD[models.DiscordAuthorizedUser]{
		render:               components.DiscordAuthorizedUsersList,
		notConfiguredMessage: "Discord auth not configured",
		configured:           h.discordAuthRepo != nil,
	}
	if h.discordAuthRepo != nil {
		crud.list = h.discordAuthRepo.ListByProject
		crud.getByID = h.discordAuthRepo.GetByID
		crud.delete = h.discordAuthRepo.Delete
		crud.projectID = func(user *models.DiscordAuthorizedUser) string { return user.ProjectID }
	}
	return crud
}

package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
)

// ListTelegramAuthorizedUsers returns the authorized users list for a project.
func (h *Handler) ListTelegramAuthorizedUsers(c echo.Context) error {
	return h.telegramAuthorizedUserCRUD().listHandler(c)
}

// AddTelegramAuthorizedUser adds a new authorized Telegram user.
func (h *Handler) AddTelegramAuthorizedUser(c echo.Context) error {
	return h.telegramAuthorizedUserCRUD().createHandler(c, func(ctx context.Context, projectID string) error {
		userIDOrUsername := c.FormValue("user_id_or_username")
		displayName := c.FormValue("display_name")
		if userIDOrUsername == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "User ID or username is required")
		}

		user := &models.TelegramAuthorizedUser{ProjectID: projectID, AddedBy: "web"}
		if telegramUserID, err := strconv.ParseInt(userIDOrUsername, 10, 64); err == nil {
			user.TelegramUserID = telegramUserID
		} else {
			user.TelegramUsername = strings.ToLower(strings.TrimPrefix(userIDOrUsername, "@"))
		}

		if displayName != "" {
			user.DisplayName = displayName
		} else if user.TelegramUsername != "" {
			user.DisplayName = "@" + user.TelegramUsername
		} else {
			user.DisplayName = strconv.FormatInt(user.TelegramUserID, 10)
		}
		return h.telegramAuthRepo.Create(ctx, user)
	})
}

// RemoveTelegramAuthorizedUser removes an authorized Telegram user.
func (h *Handler) RemoveTelegramAuthorizedUser(c echo.Context) error {
	return h.telegramAuthorizedUserCRUD().deleteHandler(c)
}

func (h *Handler) telegramAuthorizedUserCRUD() authorizedUserCRUD[models.TelegramAuthorizedUser] {
	crud := authorizedUserCRUD[models.TelegramAuthorizedUser]{
		render:               components.TelegramAuthorizedUsersList,
		notConfiguredMessage: "Telegram auth not configured",
		configured:           h.telegramAuthRepo != nil,
	}
	if h.telegramAuthRepo != nil {
		crud.list = h.telegramAuthRepo.ListByProject
		crud.getByID = h.telegramAuthRepo.GetByID
		crud.delete = h.telegramAuthRepo.Delete
		crud.projectID = func(user *models.TelegramAuthorizedUser) string { return user.ProjectID }
	}
	return crud
}

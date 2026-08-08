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
	if h.telegramAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Telegram auth not configured")
	}
	return h.telegramAuthorizedUserCRUD().listUsers(c, c.QueryParam("project_id"))
}

// AddTelegramAuthorizedUser adds a new authorized Telegram user.
func (h *Handler) AddTelegramAuthorizedUser(c echo.Context) error {
	if h.telegramAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Telegram auth not configured")
	}

	projectID := c.FormValue("project_id")
	return h.telegramAuthorizedUserCRUD().createUser(c, projectID, func(ctx context.Context) error {
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
	if h.telegramAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Telegram auth not configured")
	}
	return h.telegramAuthorizedUserCRUD().deleteUser(
		c,
		c.Param("id"),
		c.QueryParam("project_id"),
		authorizedUserProjectLookup(h.telegramAuthRepo.GetByID, func(user *models.TelegramAuthorizedUser) string {
			return user.ProjectID
		}),
		h.telegramAuthRepo.Delete,
	)
}

func (h *Handler) telegramAuthorizedUserCRUD() authorizedUserCRUD[models.TelegramAuthorizedUser] {
	return authorizedUserCRUD[models.TelegramAuthorizedUser]{
		list:   h.telegramAuthRepo.ListByProject,
		render: components.TelegramAuthorizedUsersList,
	}
}

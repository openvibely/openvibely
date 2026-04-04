package handler

import (
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

	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}

	users, err := h.telegramAuthRepo.ListByProject(c.Request().Context(), projectID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load authorized users")
	}

	return render(c, http.StatusOK, components.TelegramAuthorizedUsersList(users, projectID))
}

// AddTelegramAuthorizedUser adds a new authorized Telegram user.
func (h *Handler) AddTelegramAuthorizedUser(c echo.Context) error {
	if h.telegramAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Telegram auth not configured")
	}

	projectID := c.FormValue("project_id")
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}

	userIDOrUsername := c.FormValue("user_id_or_username")
	displayName := c.FormValue("display_name")

	if userIDOrUsername == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "User ID or username is required")
	}

	user := &models.TelegramAuthorizedUser{
		ProjectID: projectID,
		AddedBy:   "web",
	}

	// Try to parse as a numeric user ID first
	if telegramUserID, err := strconv.ParseInt(userIDOrUsername, 10, 64); err == nil {
		user.TelegramUserID = telegramUserID
		user.TelegramUsername = ""
	} else {
		// Treat as username — strip leading @ and lowercase for consistent matching.
		// Telegram usernames are case-insensitive and the API returns them without @.
		username := strings.TrimPrefix(userIDOrUsername, "@")
		username = strings.ToLower(username)
		user.TelegramUserID = 0
		user.TelegramUsername = username
	}

	if displayName != "" {
		user.DisplayName = displayName
	} else if user.TelegramUsername != "" {
		user.DisplayName = "@" + user.TelegramUsername
	} else {
		user.DisplayName = strconv.FormatInt(user.TelegramUserID, 10)
	}

	if err := h.telegramAuthRepo.Create(c.Request().Context(), user); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to add authorized user: "+err.Error())
	}

	// Return updated list
	users, err := h.telegramAuthRepo.ListByProject(c.Request().Context(), projectID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load authorized users")
	}

	return render(c, http.StatusOK, components.TelegramAuthorizedUsersList(users, projectID))
}

// RemoveTelegramAuthorizedUser removes an authorized Telegram user.
func (h *Handler) RemoveTelegramAuthorizedUser(c echo.Context) error {
	if h.telegramAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Telegram auth not configured")
	}

	id := c.Param("id")
	projectID := c.QueryParam("project_id")

	// Get the user first to know the project_id for the list refresh
	user, err := h.telegramAuthRepo.GetByID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to find user")
	}
	if user == nil {
		return echo.NewHTTPError(http.StatusNotFound, "User not found")
	}

	if projectID == "" {
		projectID = user.ProjectID
	}

	if err := h.telegramAuthRepo.Delete(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to remove user: "+err.Error())
	}

	// Return updated list
	users, err := h.telegramAuthRepo.ListByProject(c.Request().Context(), projectID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load authorized users")
	}

	return render(c, http.StatusOK, components.TelegramAuthorizedUsersList(users, projectID))
}

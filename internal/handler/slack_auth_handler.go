package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
)

// ListSlackAuthorizedUsers returns the authorized Slack users list for a project.
func (h *Handler) ListSlackAuthorizedUsers(c echo.Context) error {
	if h.slackAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Slack auth not configured")
	}

	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}

	users, err := h.slackAuthRepo.ListByProject(c.Request().Context(), projectID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load authorized users")
	}

	return render(c, http.StatusOK, components.SlackAuthorizedUsersList(users, projectID))
}

// AddSlackAuthorizedUser adds a new authorized Slack user.
func (h *Handler) AddSlackAuthorizedUser(c echo.Context) error {
	if h.slackAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Slack auth not configured")
	}

	projectID := c.FormValue("project_id")
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}

	slackUserID := strings.TrimSpace(c.FormValue("slack_user_id"))
	displayName := strings.TrimSpace(c.FormValue("display_name"))

	if slackUserID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Slack user ID is required")
	}

	user := &models.SlackAuthorizedUser{
		ProjectID:   projectID,
		SlackUserID: slackUserID,
		DisplayName: displayName,
		AddedBy:     "web",
	}
	if user.DisplayName == "" {
		user.DisplayName = slackUserID
	}

	if err := h.slackAuthRepo.Create(c.Request().Context(), user); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to add authorized user: "+err.Error())
	}

	users, err := h.slackAuthRepo.ListByProject(c.Request().Context(), projectID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load authorized users")
	}

	return render(c, http.StatusOK, components.SlackAuthorizedUsersList(users, projectID))
}

// RemoveSlackAuthorizedUser removes an authorized Slack user.
func (h *Handler) RemoveSlackAuthorizedUser(c echo.Context) error {
	if h.slackAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Slack auth not configured")
	}

	id := c.Param("id")
	projectID := c.QueryParam("project_id")

	user, err := h.slackAuthRepo.GetByID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to find user")
	}
	if user == nil {
		return echo.NewHTTPError(http.StatusNotFound, "User not found")
	}

	if projectID == "" {
		projectID = user.ProjectID
	}

	if err := h.slackAuthRepo.Delete(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to remove user: "+err.Error())
	}

	users, err := h.slackAuthRepo.ListByProject(c.Request().Context(), projectID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load authorized users")
	}

	return render(c, http.StatusOK, components.SlackAuthorizedUsersList(users, projectID))
}

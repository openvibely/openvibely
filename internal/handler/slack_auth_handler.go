package handler

import (
	"context"
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
	return h.slackAuthorizedUserCRUD().listUsers(c, c.QueryParam("project_id"))
}

// AddSlackAuthorizedUser adds a new authorized Slack user.
func (h *Handler) AddSlackAuthorizedUser(c echo.Context) error {
	if h.slackAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Slack auth not configured")
	}

	projectID := c.FormValue("project_id")
	return h.slackAuthorizedUserCRUD().createUser(c, projectID, func(ctx context.Context) error {
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
		return h.slackAuthRepo.Create(ctx, user)
	})
}

// RemoveSlackAuthorizedUser removes an authorized Slack user.
func (h *Handler) RemoveSlackAuthorizedUser(c echo.Context) error {
	if h.slackAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Slack auth not configured")
	}
	return h.slackAuthorizedUserCRUD().deleteUser(
		c,
		c.Param("id"),
		c.QueryParam("project_id"),
		authorizedUserProjectLookup(h.slackAuthRepo.GetByID, func(user *models.SlackAuthorizedUser) string {
			return user.ProjectID
		}),
		h.slackAuthRepo.Delete,
	)
}

func (h *Handler) slackAuthorizedUserCRUD() authorizedUserCRUD[models.SlackAuthorizedUser] {
	return authorizedUserCRUD[models.SlackAuthorizedUser]{
		list:   h.slackAuthRepo.ListByProject,
		render: components.SlackAuthorizedUsersList,
	}
}

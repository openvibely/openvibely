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
	return h.slackAuthorizedUserCRUD().listHandler(c)
}

// AddSlackAuthorizedUser adds a new authorized Slack user.
func (h *Handler) AddSlackAuthorizedUser(c echo.Context) error {
	return h.slackAuthorizedUserCRUD().createHandler(c, func(ctx context.Context, projectID string) error {
		slackUserID := strings.TrimSpace(c.FormValue("slack_user_id"))
		displayName := strings.TrimSpace(c.FormValue("display_name"))
		if slackUserID == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "Slack user ID is required")
		}

		user := &models.SlackAuthorizedUser{
			ChannelSenderMapping: senderMappingForm(c),
			ProjectID:            projectID,
			SlackUserID:          slackUserID,
			DisplayName:          displayName,
			AddedBy:              "web",
		}
		if user.DisplayName == "" {
			user.DisplayName = slackUserID
		}
		return h.slackAuthRepo.Create(ctx, user)
	})
}

// RemoveSlackAuthorizedUser removes an authorized Slack user.
func (h *Handler) RemoveSlackAuthorizedUser(c echo.Context) error {
	return h.slackAuthorizedUserCRUD().deleteHandler(c)
}

func (h *Handler) slackAuthorizedUserCRUD() authorizedUserCRUD[models.SlackAuthorizedUser] {
	crud := authorizedUserCRUD[models.SlackAuthorizedUser]{
		authorize:            h.prepareChannelSenderOptions,
		render:               components.SlackAuthorizedUsersList,
		notConfiguredMessage: "Slack auth not configured",
		configured:           h.slackAuthRepo != nil,
	}
	if h.slackAuthRepo != nil {
		crud.list = h.slackAuthRepo.ListByProject
		crud.getByID = h.slackAuthRepo.GetByID
		crud.delete = h.slackAuthRepo.DeleteForProject
		crud.projectID = func(user *models.SlackAuthorizedUser) string { return user.ProjectID }
	}
	return crud
}

// BindSlackApplicationUser records an administrator-attested workspace identity.
func (h *Handler) BindSlackApplicationUser(c echo.Context) error {
	projectID := c.FormValue("project_id")
	if err := h.prepareChannelSenderOptions(c, projectID, true); err != nil {
		return err
	}
	if h.slackAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Slack auth not configured")
	}
	teamID, userID := strings.TrimSpace(c.FormValue("team_id")), strings.TrimSpace(c.FormValue("application_user_id"))
	if teamID == "" || userID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Workspace and application user IDs are required")
	}
	if err := h.slackAuthRepo.BindApplicationUser(c.Request().Context(), projectID, c.Param("id"), teamID, userID); err != nil {
		return err
	}
	return h.slackAuthorizedUserCRUD().reload(c, projectID)
}

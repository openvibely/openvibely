package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/web/templates/components"
)

// ListEmailAuthorizedSenders returns the authorized email senders list for a project.
func (h *Handler) ListEmailAuthorizedSenders(c echo.Context) error {
	if h.emailAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Email auth not configured")
	}
	return h.emailAuthorizedUserCRUD().listUsers(c, c.QueryParam("project_id"))
}

// AddEmailAuthorizedSender adds a new authorized email sender.
func (h *Handler) AddEmailAuthorizedSender(c echo.Context) error {
	if h.emailAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Email auth not configured")
	}

	projectID := c.FormValue("project_id")
	return h.emailAuthorizedUserCRUD().createUser(c, projectID, func(ctx context.Context) error {
		emailAddress := repository.NormalizeEmailAddress(c.FormValue("authorized_email_address"))
		displayName := strings.TrimSpace(c.FormValue("display_name"))
		if emailAddress == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "Email address is required")
		}
		if displayName == "" {
			displayName = emailAddress
		}
		sender := &models.EmailAuthorizedSender{
			ProjectID:    projectID,
			EmailAddress: emailAddress,
			DisplayName:  displayName,
			AddedBy:      "web",
		}
		return h.emailAuthRepo.Create(ctx, sender)
	})
}

// RemoveEmailAuthorizedSender removes an authorized email sender.
func (h *Handler) RemoveEmailAuthorizedSender(c echo.Context) error {
	if h.emailAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Email auth not configured")
	}
	return h.emailAuthorizedUserCRUD().deleteUser(
		c,
		c.Param("id"),
		c.QueryParam("project_id"),
		authorizedUserProjectLookup(h.emailAuthRepo.GetByID, func(sender *models.EmailAuthorizedSender) string {
			return sender.ProjectID
		}),
		h.emailAuthRepo.Delete,
	)
}

func (h *Handler) emailAuthorizedUserCRUD() authorizedUserCRUD[models.EmailAuthorizedSender] {
	return authorizedUserCRUD[models.EmailAuthorizedSender]{
		list:   h.emailAuthRepo.ListByProject,
		render: components.EmailAuthorizedSendersList,
	}
}

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
	return h.emailAuthorizedUserCRUD().listHandler(c)
}

// AddEmailAuthorizedSender adds a new authorized email sender.
func (h *Handler) AddEmailAuthorizedSender(c echo.Context) error {
	return h.emailAuthorizedUserCRUD().createHandler(c, func(ctx context.Context, projectID string) error {
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
	return h.emailAuthorizedUserCRUD().deleteHandler(c)
}

func (h *Handler) emailAuthorizedUserCRUD() authorizedUserCRUD[models.EmailAuthorizedSender] {
	crud := authorizedUserCRUD[models.EmailAuthorizedSender]{
		render:               components.EmailAuthorizedSendersList,
		notConfiguredMessage: "Email auth not configured",
		configured:           h.emailAuthRepo != nil,
	}
	if h.emailAuthRepo != nil {
		crud.list = h.emailAuthRepo.ListByProject
		crud.getByID = h.emailAuthRepo.GetByID
		crud.delete = h.emailAuthRepo.Delete
		crud.projectID = func(sender *models.EmailAuthorizedSender) string { return sender.ProjectID }
	}
	return crud
}

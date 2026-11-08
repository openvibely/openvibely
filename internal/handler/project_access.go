package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
)

func (h *Handler) SetProjectAccessService(s *service.ProjectAccessService) { h.projectAccessSvc = s }

// projectAccessBoundary protects project management and resolved task resources
// before handlers can perform side effects. A selected project is not proof of
// ownership for an arbitrary task, schedule, execution, input, or attachment ID.
func (h *Handler) projectAccessBoundary(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if h.projectAccessSvc != nil {
			switch c.Path() {
			case "/api/system/update/apply", "/api/system/update/cancel":
				if err := h.requireCurrentOrgAdmin(c); err != nil {
					return projectAccessResponse(c, err)
				}
			}
			if err := h.authorizeChannelTrustResource(c); err != nil {
				return projectAccessResponse(c, err)
			}
			if err := h.authorizeCapacityResource(c); err != nil {
				return projectAccessResponse(c, err)
			}
			if err := h.authorizeSharedDefinitionResource(c); err != nil {
				return projectAccessResponse(c, err)
			}
			if err := h.authorizeAnalyticsResource(c); err != nil {
				return projectAccessResponse(c, err)
			}
			if err := h.authorizeActivityResource(c); err != nil {
				return projectAccessResponse(c, err)
			}
			if err := h.authorizeChatResource(c); err != nil {
				return projectAccessResponse(c, err)
			}
			if err := h.authorizeAlertResource(c); err != nil {
				return projectAccessResponse(c, err)
			}
			if err := h.authorizeWebhookResource(c); err != nil {
				return projectAccessResponse(c, err)
			}
			if err := h.authorizeAutomationResource(c); err != nil {
				return projectAccessResponse(c, err)
			}
			if err := h.authorizeTaskResource(c); err != nil {
				return projectAccessResponse(c, err)
			}
			permission := authz.Permission("")
			switch c.Path() {
			case "/projects/:id":
				switch c.Request().Method {
				case http.MethodDelete:
					permission = authz.Own
				case http.MethodPut:
					permission = authz.Manage
				}
			case "/projects/:id/edit", "/projects/:id/reclone":
				permission = authz.Manage
			}
			if permission != "" {
				if err := h.projectAccessSvc.Require(c.Request().Context(), c.Param("id"), permission); err != nil {
					return projectAccessResponse(c, err)
				}
			}
		}
		return projectAccessResponse(c, next(c))
	}
}
func projectAccessHTTPError(err error) error {
	if errors.Is(err, authz.ErrDenied) {
		return echo.NewHTTPError(http.StatusNotFound, "project not found")
	}
	return err
}

// Report insufficient roles without disclosing projects the caller cannot view.
func projectAccessResponse(c echo.Context, err error) error {
	var denied *authz.InsufficientRole
	if errors.As(err, &denied) {
		requirement := "Editor access is required for this action."
		switch c.Path() {
		case "/tasks":
			if c.Request().Method == http.MethodPost {
				requirement = "Editor access is required to create tasks."
			}
		case "/tasks/:taskId/thread":
			requirement = "Editor access is required to send follow-ups."
		case "/conversation/model-settings":
			requirement = "Editor access is required to change the model or its settings."
		case "/tasks/:taskId/run":
			requirement = "Editor access is required to run tasks."
		}
		if denied.Permission == authz.Manage {
			requirement = "Maintainer access is required for this action."
		}
		if denied.Permission == authz.Own {
			requirement = "Only the project owner or an organization admin can perform this action."
		}
		message := "You have " + denied.Role + " access to this project. " + requirement + " Ask the project owner or an organization admin for access."
		c.Response().Header().Set("X-OpenVibely-Permission-Denied", "true")
		setHTMXToast(c, message, "failed")
		c.Response().Header().Set("HX-Reswap", "none")
		return echo.NewHTTPError(http.StatusForbidden, message)
	}
	return projectAccessHTTPError(err)
}

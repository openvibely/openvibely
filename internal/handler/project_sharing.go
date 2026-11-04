package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) ProjectSharing(c echo.Context) error { return h.renderProjectSharing(c, "") }

func (h *Handler) renderProjectSharing(c echo.Context, message string) error {
	if h.projectAccessSvc == nil || h.organizationSvc == nil {
		return echo.NewHTTPError(404)
	}
	ctx, id := c.Request().Context(), c.Param("id")
	policy, err := h.projectAccessSvc.Policy(ctx, id)
	if err != nil {
		return projectAccessHTTPError(err)
	}
	members, err := h.projectAccessSvc.Members(ctx, id)
	if err != nil {
		return err
	}
	users, err := h.organizationSvc.Store.Members(ctx)
	if err != nil {
		return err
	}
	audit, err := h.projectAccessSvc.Audit(ctx, id)
	if err != nil {
		return err
	}
	return render(c, http.StatusOK, pages.ProjectSharing(policy, members, users, audit, h.projectAccessSvc.Require(ctx, id, authz.Own) == nil, message))
}

func (h *Handler) UpdateProjectSharing(c echo.Context) error {
	if err := requireOrgSameOrigin(c); err != nil {
		return err
	}
	if h.projectAccessSvc == nil || h.organizationSvc == nil {
		return echo.NewHTTPError(404)
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 16<<10)
	ctx, id := c.Request().Context(), c.Param("id")
	policy, err := h.projectAccessSvc.Policy(ctx, id)
	if err != nil {
		return projectAccessHTTPError(err)
	}
	switch c.FormValue("action") {
	case "policy":
		policy.OrgRole = c.FormValue("org_role")
		err = h.projectAccessSvc.Update(ctx, *policy)
	case "member":
		err = h.projectAccessSvc.SetMember(ctx, id, c.FormValue("user_id"), c.FormValue("role"))
	case "transfer":
		if err = h.projectAccessSvc.Require(ctx, id, authz.Own); err == nil {
			policy.OwnerUserID = c.FormValue("user_id")
			err = h.projectAccessSvc.Update(ctx, *policy)
		}
	default:
		return h.renderProjectSharing(c, "Choose a valid sharing action.")
	}
	if err != nil {
		if errors.Is(err, authz.ErrDenied) {
			return h.renderProjectSharing(c, "This change is not allowed. Check the member and your project role.")
		}
		if errors.Is(err, authz.ErrSandboxRequired) {
			return h.renderProjectSharing(c, "Private projects are not available until sandboxing is implemented.")
		}
		return err
	}
	if h.projectAccessSvc.Require(ctx, id, authz.Manage) != nil {
		return c.HTML(http.StatusOK, `<section id="project-sharing" role="status">Sharing updated. You no longer have access to manage this project.</section>`)
	}
	return h.renderProjectSharing(c, "")
}

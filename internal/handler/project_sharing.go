package handler

import (
	"encoding/json"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) ProjectSharing(c echo.Context) error { return h.renderProjectSharing(c, "") }

func (h *Handler) renderProjectSharing(c echo.Context, message string) error {
	state, err := actProjectSharing(h, c.Request().Context(), c.Param("id"))
	if err != nil {
		return projectAccessResponse(c, err)
	}
	return render(c, http.StatusOK, pages.ProjectSharing(state.Policy, state.Members, state.Users, state.Audit, state.Owner, message))
}

func (h *Handler) UpdateProjectSharing(c echo.Context) error {
	if err := requireOrgSameOrigin(c); err != nil {
		return err
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 16<<10)
	args := projectSharingArgs{ProjectID: c.Param("id"), Operation: c.FormValue("action"), UserID: c.FormValue("user_id"), Role: c.FormValue("role")}
	if args.Operation == "members" {
		var roles map[string]string
		if err := json.Unmarshal([]byte(c.FormValue("changes")), &roles); err != nil || roles == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid membership changes")
		}
		for userID, role := range roles {
			args.Changes = append(args.Changes, projectRoleChange{UserID: userID, Role: role})
		}
	}
	result, err := actUpdateProjectSharing(h, c.Request().Context(), args)
	if err != nil {
		return projectAccessResponse(c, err)
	}
	if result.Message != "" {
		return h.renderProjectSharing(c, result.Message)
	}
	if args.Operation == "members" {
		return c.NoContent(http.StatusNoContent)
	}
	if result.Transferred {
		return render(c, http.StatusOK, pages.ProjectSharingTransferred())
	}
	return h.renderProjectSharing(c, "")
}

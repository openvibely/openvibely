package handler

import (
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/web/templates/pages"
	"net/http"
)

func (h *Handler) Shortcuts(c echo.Context) error {
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.ShortcutsContent())
	}
	projectID, _ := h.getCurrentProjectID(c)
	projects, err := h.projectSvc.List(c.Request().Context())
	if err != nil {
		return err
	}
	return render(c, http.StatusOK, pages.Shortcuts(projects, projectID))
}

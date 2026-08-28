package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/themes"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) Themes(c echo.Context) error {
	catalog := themes.Catalog()
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.ThemesContent(catalog))
	}
	currentProjectID, _ := h.getCurrentProjectID(c)
	projects, _ := h.projectSvc.List(c.Request().Context())
	return render(c, http.StatusOK, pages.Themes(projects, currentProjectID, catalog))
}

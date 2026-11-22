package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) UpdateSettings(c echo.Context) error {
	if h.updateCoordinator == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "App updates are unavailable")
	}
	canManage := h.projectAccessSvc == nil || h.requireCurrentOrgAdmin(c) == nil
	saved := false
	message := ""
	var attemptedValue *bool
	if c.Request().Method == http.MethodPost {
		if !computerSameOrigin(c.Request()) {
			return echo.NewHTTPError(http.StatusForbidden, "Cross-origin request denied")
		}
		if !canManage {
			return echo.NewHTTPError(http.StatusForbidden, "Administrator access required")
		}
		value := c.FormValue("automatic_updates")
		if value != "" && value != "true" && value != "false" {
			return echo.NewHTTPError(http.StatusBadRequest, "Choose enabled or disabled")
		}
		if err := h.auditedSystemUpdate(c, "settings", func() error {
			return h.updateCoordinator.SetAutomaticUpdates(value == "true")
		}); err != nil {
			message = "Could not confirm update settings were saved. Please try again."
			enabled := value == "true"
			attemptedValue = &enabled
		} else {
			saved = true
		}
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	snapshot := h.updateCoordinator.Snapshot()
	if attemptedValue != nil {
		snapshot.AutomaticUpdates = *attemptedValue
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.UpdateSettingsContent(projectID, snapshot, canManage, saved, message))
	}
	projects, err := h.projectSvc.ListSelectorOptions(c.Request().Context())
	if err != nil {
		return err
	}
	return render(c, http.StatusOK, pages.UpdateSettingsPage(projects, projectID, snapshot, canManage, saved, message))
}

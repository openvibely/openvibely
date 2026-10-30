package handler

import (
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service"
	"net/http"
	"strconv"
)

func (h *Handler) AccountUsageNotifications(c echo.Context) error {
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "settings unavailable")
	}
	key := service.AccountUsageAlertsSetting(projectID)
	if c.Request().Method == http.MethodPost {
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		if err := c.Bind(&input); err != nil || input.Enabled == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "enabled is required")
		}
		if err := h.settingsRepo.Set(c.Request().Context(), key, strconv.FormatBool(*input.Enabled)); err != nil {
			return err
		}
	}
	value, err := h.settingsRepo.Get(c.Request().Context(), key)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]bool{"enabled": value != "false"})
}

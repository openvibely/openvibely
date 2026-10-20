package handler

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/update"
)

func (h *Handler) SetUpdateCoordinator(coordinator *update.Coordinator) {
	h.updateCoordinator = coordinator
}
func (h *Handler) SetUpdateWorkTracker(tracker *update.WorkTracker) { h.updateWorkTracker = tracker }

// SystemUpdate returns global update state without creating project alerts.
// @Summary Get system update state
// @Tags system
// @Produce json
// @Success 200 {object} update.CoordinatorSnapshot
// @Success 204
// @Router /api/system/update [get]
func (h *Handler) SystemUpdate(c echo.Context) error {
	if h.updateCoordinator == nil || !h.updateCoordinator.Visible() {
		return c.NoContent(http.StatusNoContent)
	}
	return c.JSON(http.StatusOK, h.updateCoordinator.Snapshot())
}

// ApplySystemUpdate accepts, stages, and begins draining for a managed update.
// @Summary Accept a system update
// @Tags system
// @Produce json
// @Success 202 {object} update.CoordinatorSnapshot
// @Failure 409 {object} map[string]any
// @Router /api/system/update/apply [post]
func (h *Handler) ApplySystemUpdate(c echo.Context) error {
	if h.updateCoordinator == nil || !h.updateCoordinator.Visible() {
		return echo.NewHTTPError(http.StatusNotFound)
	}
	if err := h.updateCoordinator.Accept(c.Request().Context(), 10*time.Minute); err != nil {
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	}
	return c.JSON(http.StatusAccepted, h.updateCoordinator.Snapshot())
}

// CancelSystemUpdate cancels drain and reopens execution admission.
// @Summary Cancel a system update
// @Tags system
// @Produce json
// @Success 200 {object} update.CoordinatorSnapshot
// @Failure 409 {object} map[string]any
// @Router /api/system/update/cancel [post]
func (h *Handler) CancelSystemUpdate(c echo.Context) error {
	if h.updateCoordinator == nil {
		return echo.NewHTTPError(http.StatusNotFound)
	}
	if err := h.updateCoordinator.Cancel(); err != nil {
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	}
	return c.JSON(http.StatusOK, h.updateCoordinator.Snapshot())
}

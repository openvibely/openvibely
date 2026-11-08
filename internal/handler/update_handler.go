package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
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
	canManage := h.projectAccessSvc == nil || h.requireCurrentOrgAdmin(c) == nil
	c.Response().Header().Set("X-OpenVibely-Can-Manage-Updates", strconv.FormatBool(canManage))
	c.Response().Header().Set("Cache-Control", "no-store")
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
	if err := h.auditedSystemUpdate(c, "apply", func() error { return h.updateCoordinator.Accept(c.Request().Context(), 10*time.Minute) }); err != nil {
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
	if err := h.auditedSystemUpdate(c, "cancel", h.updateCoordinator.Cancel); err != nil {
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	}
	return c.JSON(http.StatusOK, h.updateCoordinator.Snapshot())
}

// Update coordination is an external side effect: persist intent first and keep
// an outcome even if the request disconnects. An outcome audit failure cannot
// undo an update that has already been accepted.
func (h *Handler) auditedSystemUpdate(c echo.Context, operation string, perform func() error) error {
	if h.projectAccessSvc == nil {
		return perform()
	}
	if err := h.requireCurrentOrgAdmin(c); err != nil {
		return err
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	action := "system_update." + operation
	if err := h.projectAccessSvc.RecordAction(ctx, projectID, action+".requested", "instance", `{}`); err != nil {
		return err
	}
	operationErr := perform()
	outcome := ".succeeded"
	if operationErr != nil {
		outcome = ".failed_or_unknown"
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	auditErr := h.projectAccessSvc.RecordAction(auditCtx, projectID, action+outcome, "instance", `{}`)
	return errors.Join(operationErr, auditErr)
}

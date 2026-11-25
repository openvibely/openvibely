package handler

import (
	"context"
	"errors"
	"github.com/openvibely/openvibely/internal/service/authz"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
)

func (h *Handler) ScheduleCalendarState(c echo.Context) error {
	if h.scheduleRepo == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "schedules unavailable")
	}
	projectID := h.mutationProjectID(c)
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project required")
	}
	state, err := h.scheduleRepo.CalendarState(c.Request().Context(), projectID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, state)
}

func (h *Handler) ScheduleCalendarAction(c echo.Context) error {
	if h.scheduleRepo == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "schedules unavailable")
	}
	var action models.ScheduleCalendarAction
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 2<<20)
	if err := c.Bind(&action); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid calendar action")
	}
	undo, err := actCalendarAction(h, c.Request().Context(), calendarActionArgs{ProjectID: h.mutationProjectID(c), Action: action})
	if errors.Is(err, persistence.ErrScheduleCalendarSelection) {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"undo": undo})
}

type calendarActionArgs struct {
	ProjectID string
	Action    models.ScheduleCalendarAction
}

var actCalendarAction = defineAction("schedule.calendar", func(h *Handler, ctx context.Context, a calendarActionArgs) (string, authz.Permission, error) {
	return a.ProjectID, authz.Edit, nil
}, func(h *Handler, ctx context.Context, a calendarActionArgs) (models.ScheduleCalendarAction, error) {
	undo, err := service.ApplyScheduleCalendarAction(ctx, h.scheduleRepo, a.ProjectID, a.Action)
	if errors.Is(err, persistence.ErrScheduleCalendarSelection) {
		return undo, statusErr(http.StatusBadRequest, err.Error())
	}
	return undo, err
})

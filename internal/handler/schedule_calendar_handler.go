package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
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
	undo, err := service.ApplyScheduleCalendarAction(c.Request().Context(), h.scheduleRepo, h.mutationProjectID(c), action)
	if errors.Is(err, repository.ErrScheduleCalendarSelection) {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"undo": undo})
}

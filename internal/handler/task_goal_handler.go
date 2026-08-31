package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

type taskGoalResponse struct {
	OK   bool             `json:"ok"`
	Goal *models.TaskGoal `json:"goal"`
}

func wantsJSON(c echo.Context) bool {
	return c.Request().Header.Get("Accept") == "application/json"
}

func (h *Handler) requireTaskGoalInRequestProject(c echo.Context, taskID string) error {
	projectID := h.mutationProjectID(c)
	if _, err := h.requireTaskInRequestProject(c.Request().Context(), taskID, projectID); err != nil {
		return err
	}
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project context required")
	}
	return nil
}

func (h *Handler) renderTaskGoal(c echo.Context, taskID string, status int) error {
	if h.taskGoalSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "task goal service unavailable")
	}
	goal, err := h.taskGoalSvc.GetGoal(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	if wantsJSON(c) {
		return c.JSON(status, taskGoalResponse{OK: true, Goal: goal})
	}
	return render(c, status, pages.TaskGoalPanel(taskID, goal))
}

func (h *Handler) GetTaskGoal(c echo.Context) error {
	if h.taskGoalSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "task goal service unavailable")
	}
	taskID := c.Param("taskId")
	if err := h.requireTaskGoalInRequestProject(c, taskID); err != nil {
		return err
	}
	return h.renderTaskGoal(c, taskID, http.StatusOK)
}

func (h *Handler) SetTaskGoal(c echo.Context) error {
	if h.taskGoalSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "task goal service unavailable")
	}
	taskID := c.Param("taskId")
	if err := h.requireTaskGoalInRequestProject(c, taskID); err != nil {
		return err
	}
	goal, err := h.taskGoalSvc.SetGoal(c.Request().Context(), taskID, c.FormValue("goal"), service.GoalOptions{Actor: "user"})
	if err != nil {
		if err == service.ErrTaskGoalEmpty || err == service.ErrTaskGoalTooLong {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return err
	}
	if wantsJSON(c) {
		return c.JSON(http.StatusOK, taskGoalResponse{OK: true, Goal: goal})
	}
	return render(c, http.StatusOK, pages.TaskGoalPanel(taskID, goal))
}

func (h *Handler) PauseTaskGoal(c echo.Context) error {
	if h.taskGoalSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "task goal service unavailable")
	}
	taskID := c.Param("taskId")
	if err := h.requireTaskGoalInRequestProject(c, taskID); err != nil {
		return err
	}
	if err := h.taskGoalSvc.PauseGoal(c.Request().Context(), taskID, "user"); err != nil {
		if err == service.ErrTaskGoalNotFound {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		return err
	}
	return h.renderTaskGoal(c, taskID, http.StatusOK)
}

func (h *Handler) ResumeTaskGoal(c echo.Context) error {
	if h.taskGoalSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "task goal service unavailable")
	}
	taskID := c.Param("taskId")
	if err := h.requireTaskGoalInRequestProject(c, taskID); err != nil {
		return err
	}
	if err := h.taskGoalSvc.ResumeGoal(c.Request().Context(), taskID, "user"); err != nil {
		if err == service.ErrTaskGoalNotFound {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		return err
	}
	return h.renderTaskGoal(c, taskID, http.StatusOK)
}

func (h *Handler) ClearTaskGoal(c echo.Context) error {
	if h.taskGoalSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "task goal service unavailable")
	}
	taskID := c.Param("taskId")
	if err := h.requireTaskGoalInRequestProject(c, taskID); err != nil {
		return err
	}
	if err := h.taskGoalSvc.ClearGoal(c.Request().Context(), taskID, "user"); err != nil {
		return err
	}
	return h.renderTaskGoal(c, taskID, http.StatusOK)
}

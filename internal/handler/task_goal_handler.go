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

type taskGoalRouteResult struct {
	goal   *models.TaskGoal
	reload bool
}

type taskGoalRouteOperation func(c echo.Context, taskID string, svc *service.TaskGoalService) (taskGoalRouteResult, error)

func (h *Handler) handleTaskGoalRoute(c echo.Context, status int, operation taskGoalRouteOperation) error {
	if h.taskGoalSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "task goal service unavailable")
	}
	taskID := c.Param("taskId")
	if err := h.requireTaskGoalInRequestProject(c, taskID); err != nil {
		return err
	}
	result, err := operation(c, taskID, h.taskGoalSvc)
	if err != nil {
		return err
	}
	goal := result.goal
	if result.reload {
		goal, err = h.taskGoalSvc.GetGoal(c.Request().Context(), taskID)
		if err != nil {
			return err
		}
	}
	return renderTaskGoalResponse(c, taskID, goal, status)
}

func renderTaskGoalResponse(c echo.Context, taskID string, goal *models.TaskGoal, status int) error {
	if wantsJSON(c) {
		return c.JSON(status, taskGoalResponse{OK: true, Goal: goal})
	}
	return render(c, status, pages.TaskGoalPanel(taskID, goal))
}

func (h *Handler) GetTaskGoal(c echo.Context) error {
	return h.handleTaskGoalRoute(c, http.StatusOK, func(c echo.Context, taskID string, svc *service.TaskGoalService) (taskGoalRouteResult, error) {
		goal, err := svc.GetGoal(c.Request().Context(), taskID)
		return taskGoalRouteResult{goal: goal}, err
	})
}

func (h *Handler) SetTaskGoal(c echo.Context) error {
	return h.handleTaskGoalRoute(c, http.StatusOK, func(c echo.Context, taskID string, svc *service.TaskGoalService) (taskGoalRouteResult, error) {
		goal, err := svc.SetGoal(c.Request().Context(), taskID, c.FormValue("goal"), service.GoalOptions{Actor: "user"})
		if err != nil {
			if err == service.ErrTaskGoalEmpty || err == service.ErrTaskGoalTooLong {
				return taskGoalRouteResult{}, echo.NewHTTPError(http.StatusBadRequest, err.Error())
			}
			return taskGoalRouteResult{}, err
		}
		return taskGoalRouteResult{goal: goal}, nil
	})
}

func (h *Handler) PauseTaskGoal(c echo.Context) error {
	return h.handleTaskGoalRoute(c, http.StatusOK, func(c echo.Context, taskID string, svc *service.TaskGoalService) (taskGoalRouteResult, error) {
		if err := svc.PauseGoal(c.Request().Context(), taskID, "user"); err != nil {
			if err == service.ErrTaskGoalNotFound {
				return taskGoalRouteResult{}, echo.NewHTTPError(http.StatusNotFound, err.Error())
			}
			return taskGoalRouteResult{}, err
		}
		return taskGoalRouteResult{reload: true}, nil
	})
}

func (h *Handler) ResumeTaskGoal(c echo.Context) error {
	return h.handleTaskGoalRoute(c, http.StatusOK, func(c echo.Context, taskID string, svc *service.TaskGoalService) (taskGoalRouteResult, error) {
		if err := svc.ResumeGoal(c.Request().Context(), taskID, "user"); err != nil {
			if err == service.ErrTaskGoalNotFound {
				return taskGoalRouteResult{}, echo.NewHTTPError(http.StatusNotFound, err.Error())
			}
			return taskGoalRouteResult{}, err
		}
		return taskGoalRouteResult{reload: true}, nil
	})
}

func (h *Handler) ClearTaskGoal(c echo.Context) error {
	return h.handleTaskGoalRoute(c, http.StatusOK, func(c echo.Context, taskID string, svc *service.TaskGoalService) (taskGoalRouteResult, error) {
		if err := svc.ClearGoal(c.Request().Context(), taskID, "user"); err != nil {
			return taskGoalRouteResult{}, err
		}
		return taskGoalRouteResult{reload: true}, nil
	})
}

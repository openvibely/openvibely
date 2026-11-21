package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

type taskGoalResponse struct {
	OK   bool             `json:"ok"`
	Goal *models.TaskGoal `json:"goal"`
}

func wantsJSON(c echo.Context) bool {
	return c.Request().Header.Get("Accept") == "application/json"
}

func (h *Handler) taskGoalRoute(c echo.Context, op string) error {
	taskID := c.Param("taskId")
	goal, err := actTaskGoal(h, c.Request().Context(), taskGoalArgs{
		TaskRef: TaskRef{TaskID: taskID, ProjectID: h.mutationProjectID(c)},
		Op:      op, Goal: strings.TrimSpace(c.FormValue("goal")),
	})
	if err != nil {
		return err
	}
	return renderTaskGoalResponse(c, taskID, goal, http.StatusOK)
}

func renderTaskGoalResponse(c echo.Context, taskID string, goal *models.TaskGoal, status int) error {
	if wantsJSON(c) {
		return c.JSON(status, taskGoalResponse{OK: true, Goal: goal})
	}
	return render(c, status, pages.TaskGoalPanel(taskID, goal))
}

func (h *Handler) GetTaskGoal(c echo.Context) error   { return h.taskGoalRoute(c, "get") }
func (h *Handler) SetTaskGoal(c echo.Context) error   { return h.taskGoalRoute(c, "set") }
func (h *Handler) PauseTaskGoal(c echo.Context) error { return h.taskGoalRoute(c, "pause") }
func (h *Handler) ResumeTaskGoal(c echo.Context) error {
	return h.taskGoalRoute(c, "resume")
}
func (h *Handler) ClearTaskGoal(c echo.Context) error { return h.taskGoalRoute(c, "clear") }

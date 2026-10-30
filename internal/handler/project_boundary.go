package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
)

// taskProjectBoundary checks task ownership before any task route runs. This is
// a project-consistency check, not user authorization (auth is instance-wide).
// Unscoped deep links retain their existing behavior; an explicit or selected
// project must never be ignored in favor of the task's own project.
func (h *Handler) taskProjectBoundary(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		path := c.Path()
		taskID := ""
		if strings.HasPrefix(path, "/tasks/:taskId") {
			taskID = c.Param("taskId")
		} else if strings.HasPrefix(path, "/api/tasks/:id/") {
			taskID = c.Param("id")
		} else {
			return next(c)
		}
		projectID := h.mutationProjectID(c)
		if projectID == "" {
			return next(c)
		}
		// Pulse owns the rejection toast and already verifies the project before cancellation.
		if path == "/tasks/:taskId/cancel" && c.QueryParam("pulse") == "1" {
			return next(c)
		}
		task, err := h.taskRepo.GetThreadRenderMetadata(c.Request().Context(), taskID)
		if err != nil {
			return err
		}
		if task == nil || (projectID != "" && task.ProjectID != projectID) {
			return echo.NewHTTPError(http.StatusNotFound, "task not found")
		}
		return next(c)
	}
}

// threadInputProjectBoundary protects pending-input mutations, including nested
// task/input IDs. Missing rows retain the handler's existing conflict response.
func (h *Handler) threadInputProjectBoundary(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if c.Param("inputId") == "" || h.threadInputRepo == nil {
			return next(c)
		}
		input, err := h.threadInputRepo.GetByID(c.Request().Context(), c.Param("inputId"))
		if err != nil {
			return err
		}
		if input != nil {
			projectID := h.mutationProjectID(c)
			if (projectID != "" && input.ProjectID != projectID) ||
				(c.Param("taskId") != "" && input.TaskID != c.Param("taskId")) ||
				(strings.HasPrefix(c.Path(), "/chat/queued/") && input.Scope != models.ThreadInputScopeChat) {
				return echo.NewHTTPError(http.StatusNotFound, "input not found")
			}
		}
		return next(c)
	}
}

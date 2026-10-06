package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/components"
)

func (h *Handler) StopAllActiveTasks(c echo.Context) error {
	return h.applyActiveTaskAction(c, false)
}

func (h *Handler) DeleteAllActiveTasks(c echo.Context) error {
	return h.applyActiveTaskAction(c, true)
}

func (h *Handler) applyActiveTaskAction(c echo.Context, deleting bool) error {
	projectID := strings.TrimSpace(c.QueryParam("project_id"))
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}
	ctx := c.Request().Context()
	tasks, err := h.taskSvc.ListBoardByProjectWithCategorySorts(ctx, projectID, "", "", "")
	if err != nil {
		return err
	}
	tasks = components.ActiveBoardTasks(service.AttachSwarmChildren(tasks))
	var actionErrors []error
	for _, task := range tasks {
		if deleting {
			err = h.taskSvc.DeleteObservedTask(ctx, &task)
		} else {
			observed, cutoff, observeErr := h.taskSvc.ObserveTaskCancellation(ctx, task.ID)
			if observeErr != nil {
				actionErrors = append(actionErrors, observeErr)
				continue
			}
			if observed == nil || observed.ProjectID != projectID || observed.Category != task.Category {
				continue
			}
			if task.AutomationCapacityQueued && observed.Category == models.CategoryScheduled && observed.Status == models.StatusPending {
				err = h.taskSvc.CancelTaskObservedWithPending(ctx, observed, cutoff, nil)
				if err == nil {
					h.cancelActiveExecutionsAndPublish(ctx, task.ID, "StopAllActiveTasks", cutoff)
				}
			} else {
				_, err = h.cancelTaskWork(ctx, observed, cutoff, false, "StopAllActiveTasks")
			}
		}
		if err != nil {
			actionErrors = append(actionErrors, err)
		}
	}
	if err := errors.Join(actionErrors...); err != nil {
		return err
	}
	if isHTMX(c) {
		return h.renderTaskBoardRefresh(c, projectID, nil)
	}
	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/components"
)

func (h *Handler) StopAllActiveTasks(c echo.Context) error {
	return h.boardAction(c, actStopActive, strings.TrimSpace(c.QueryParam("project_id")))
}

func (h *Handler) DeleteAllActiveTasks(c echo.Context) error {
	return h.boardAction(c, actDeleteActive, strings.TrimSpace(c.QueryParam("project_id")))
}

var actStopActive = defineAction("board.stop_active", requiredBoardProject, func(h *Handler, ctx context.Context, projectID string) (int, error) {
	return 0, h.applyActiveTaskAction(ctx, projectID, false)
})

var actDeleteActive = defineAction("board.delete_active", requiredBoardProject, func(h *Handler, ctx context.Context, projectID string) (int, error) {
	return 0, h.applyActiveTaskAction(ctx, projectID, true)
})

func (h *Handler) applyActiveTaskAction(ctx context.Context, projectID string, deleting bool) error {
	if projectID == "" {
		return statusErr(http.StatusBadRequest, "project_id is required")
	}
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
	return errors.Join(actionErrors...)
}

package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// boardProject authorizes a whole-board action. All projects is allowed here;
// a remote caller is still refused it by requireActionProject.
func boardProject(h *Handler, ctx context.Context, projectID string) (string, authz.Permission, error) {
	if models.IsAggregateProject(projectID) {
		return projectID, authz.Edit, nil
	}
	return editProject(h, ctx, projectID)
}

func requiredBoardProject(h *Handler, ctx context.Context, projectID string) (string, authz.Permission, error) {
	if projectID == "" {
		return "", "", statusErr(http.StatusBadRequest, "project_id is required")
	}
	return boardProject(h, ctx, projectID)
}

func (h *Handler) boardRefresh(c echo.Context, projectID string) error {
	if isHTMX(c) {
		return h.renderTaskBoardRefresh(c, projectID, nil)
	}
	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) boardAction(c echo.Context, act func(*Handler, context.Context, string) (int, error), projectID string) error {
	if _, err := act(h, c.Request().Context(), projectID); err != nil {
		return err
	}
	return h.boardRefresh(c, projectID)
}

var actMoveCompleted = defineAction("board.move_completed", requiredBoardProject, func(h *Handler, ctx context.Context, projectID string) (int, error) {
	count, err := h.taskSvc.MoveCompletedActiveToCompleted(ctx, projectID)
	if errors.Is(err, service.ErrTaskProjectScopeRequired) {
		return 0, statusErr(http.StatusBadRequest, "project_id is required")
	}
	applog.Infof("[handler] MoveCompletedActiveToCompleted project=%s moved=%d err=%v", projectID, count, err)
	return count, err
})

var actDeleteCompleted = defineAction("board.delete_completed", boardProject, func(h *Handler, ctx context.Context, projectID string) (int, error) {
	count, err := h.taskSvc.DeleteAllCompleted(ctx, projectID)
	applog.Infof("[handler] DeleteAllCompletedTasks project=%s deleted=%d err=%v", projectID, count, err)
	return count, err
})

var actDeleteBacklog = defineAction("board.delete_backlog", boardProject, func(h *Handler, ctx context.Context, projectID string) (int, error) {
	count, err := h.taskSvc.DeleteAllBacklog(ctx, projectID)
	applog.Infof("[handler] DeleteAllBacklogTasks project=%s deleted=%d err=%v", projectID, count, err)
	return count, err
})

type executeBacklogArgs struct {
	ProjectID string
	Priority  int
}

var actExecuteBacklog = defineAction("board.execute_backlog", func(h *Handler, ctx context.Context, a executeBacklogArgs) (string, authz.Permission, error) {
	return boardProject(h, ctx, a.ProjectID)
}, func(h *Handler, ctx context.Context, a executeBacklogArgs) (int, error) {
	tasks, submitted, err := h.taskSvc.ExecuteBacklogTasks(ctx, a.ProjectID, a.Priority)
	applog.Infof("[handler] ExecuteBacklogTasks project=%s submitted=%d/%d err=%v", a.ProjectID, submitted, len(tasks), err)
	return submitted, err
})

type batchCategoryArgs struct {
	ProjectID      string
	TaskIDs        []string
	Category       models.TaskCategory
	TargetStatus   models.TaskStatus
	ExpectedStates string
}

var actBatchCategory = defineAction("board.batch_category", func(h *Handler, ctx context.Context, a batchCategoryArgs) (string, authz.Permission, error) {
	return boardProject(h, ctx, a.ProjectID)
}, func(h *Handler, ctx context.Context, a batchCategoryArgs) (struct{}, error) {
	if a.TargetStatus != "" && (a.Category != models.CategoryActive || (a.TargetStatus != models.StatusPending && a.TargetStatus != models.StatusRunning)) {
		return struct{}{}, statusErr(http.StatusBadRequest, "invalid Active target status")
	}
	// Preflight every selected task before any schedule, model, worker, or
	// category mutation can occur. This prevents stale selections from changing
	// a task outside the project whose board is currently open.
	if err := h.taskSvc.ValidateTaskIDsForProject(ctx, a.ProjectID, a.TaskIDs); err != nil {
		if errors.Is(err, service.ErrTaskNotFoundInProject) {
			return struct{}{}, statusErr(http.StatusBadRequest, "one or more selected tasks were not found in the selected project")
		}
		return struct{}{}, err
	}
	if a.Category == models.CategoryScheduled {
		for _, id := range a.TaskIDs {
			schedules, err := h.scheduleRepo.ListByTask(ctx, id)
			if err != nil {
				return struct{}{}, err
			}
			if len(schedules) == 0 {
				return struct{}{}, statusErr(http.StatusBadRequest, "Cannot move tasks to Scheduled category: one or more tasks have no schedule")
			}
		}
	}
	if a.Category == models.CategoryActive {
		if err := h.requireModels(ctx); err != nil {
			return struct{}{}, err
		}
	}
	if a.Category == models.CategoryActive && a.TargetStatus != "" {
		moves, err := h.activeLaneTaskMoves(ctx, a.TaskIDs, a.ExpectedStates)
		if err != nil {
			return struct{}{}, err
		}
		if err := h.taskSvc.MoveTasksToActiveLane(ctx, a.ProjectID, moves, a.TargetStatus); err != nil {
			if errors.Is(err, persistence.ErrActiveLaneTaskChanged) || errors.Is(err, persistence.ErrActiveLaneLifecycleOwned) || errors.Is(err, service.ErrActiveLaneLifecycleRouted) {
				return struct{}{}, statusErr(http.StatusConflict, "one or more selected tasks changed before the move completed")
			}
			return struct{}{}, err
		}
		return struct{}{}, nil
	}
	for _, id := range a.TaskIDs {
		if err := h.taskSvc.UpdateCategory(ctx, id, a.Category); err != nil {
			applog.Infof("[handler] BatchUpdateTaskCategory error task=%s: %v", id, err)
			return struct{}{}, err
		}
	}
	return struct{}{}, nil
})

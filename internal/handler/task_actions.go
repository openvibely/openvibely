package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
)

type TaskRef struct {
	TaskID    string
	ProjectID string
}

func (a TaskRef) owner(h *Handler, ctx context.Context) (string, authz.Permission, error) {
	projectID, permission, err := editTask(h, ctx, a.TaskID)
	if err == nil && a.ProjectID != "" && !models.IsAggregateProject(a.ProjectID) && a.ProjectID != projectID {
		return "", "", statusErr(http.StatusBadRequest, "task does not belong to the active project")
	}
	return projectID, permission, err
}

func ownTaskRef[A interface{ ref() TaskRef }](h *Handler, ctx context.Context, a A) (string, authz.Permission, error) {
	return a.ref().owner(h, ctx)
}

func (a TaskRef) ref() TaskRef { return a }

// loadTask returns the task, failing with 404 when it no longer exists.
func (h *Handler) loadTask(ctx context.Context, taskID string) (*models.Task, error) {
	task, err := h.taskSvc.GetByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, statusErr(http.StatusNotFound, "task not found")
	}
	return task, nil
}

var actRunTask = defineAction("task.run", ownTaskRef[TaskRef], func(h *Handler, ctx context.Context, a TaskRef) (struct{}, error) {
	hasModels, err := h.llmConfigRepo.HasAny(ctx)
	if err != nil {
		return struct{}{}, statusErr(http.StatusInternalServerError, "failed to check model availability")
	}
	if !hasModels {
		return struct{}{}, errNoModels
	}
	task, err := h.loadTask(ctx, a.TaskID)
	if err != nil {
		return struct{}{}, err
	}
	if task.SwarmRole == models.SwarmRoleParent {
		if h.swarmSvc == nil {
			return struct{}{}, statusErr(http.StatusInternalServerError, "swarm service unavailable")
		}
		if err := h.swarmSvc.StartPlanner(ctx, a.TaskID); err != nil {
			return struct{}{}, statusErr(http.StatusBadRequest, err.Error())
		}
		return struct{}{}, nil
	}
	return struct{}{}, h.taskSvc.RunTask(ctx, a.TaskID)
})

type cancelTaskArgs struct {
	TaskRef
	ComposerStop         bool
	ExpectedGeneration   string
	ExpectedStopRevision string
	ExpectedTurnID       string
}

var actCancelTask = defineAction("task.cancel", func(h *Handler, ctx context.Context, a cancelTaskArgs) (string, authz.Permission, error) {
	projectID, permission, err := editTask(h, ctx, a.TaskID)
	if err == nil && a.ProjectID != "" && a.ProjectID != projectID {
		return "", "", statusErr(http.StatusNotFound, "This task is not available in the selected project.")
	}
	return projectID, permission, err
}, func(h *Handler, ctx context.Context, a cancelTaskArgs) (*taskCancellationResult, error) {
	task, cutoff, err := h.taskSvc.ObserveTaskCancellation(ctx, a.TaskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, statusErr(http.StatusNotFound, "task not found")
	}
	if a.ComposerStop && task.SwarmRole == models.SwarmRoleParent && (a.ExpectedGeneration != "" || a.ExpectedStopRevision != "") {
		cfg, err := models.ParseSwarmConfig(task.SwarmConfig)
		if err != nil {
			return nil, err
		}
		if a.ExpectedGeneration != "" {
			expected, err := strconv.Atoi(a.ExpectedGeneration)
			if err != nil {
				return nil, statusErr(http.StatusBadRequest, "invalid swarm generation")
			}
			if expected != cfg.Generation {
				return nil, statusErr(http.StatusConflict, "swarm generation changed; Stop was not applied")
			}
		}
		if a.ExpectedStopRevision != "" {
			expected, err := strconv.Atoi(a.ExpectedStopRevision)
			if err != nil {
				return nil, statusErr(http.StatusBadRequest, "invalid swarm Stop revision")
			}
			if expected != cfg.StopRevision {
				return nil, statusErr(http.StatusConflict, "swarm follow-up changed; Stop was not applied")
			}
		}
	}
	if a.ComposerStop && a.ExpectedTurnID != "" {
		matches, err := h.execRepo.IsTaskExecutionAtHistoryCutoff(ctx, a.TaskID, a.ExpectedTurnID, cutoff)
		if err != nil {
			return nil, err
		}
		if !matches {
			return nil, statusErr(http.StatusConflict, "response turn changed; Stop was not applied")
		}
	}
	result, err := h.cancelTaskWork(ctx, task, cutoff, a.ComposerStop, "CancelTask")
	if err != nil {
		if errors.Is(err, service.ErrTaskCancellationSuperseded) {
			return nil, statusErr(http.StatusConflict, err.Error())
		}
		return nil, statusErr(http.StatusBadRequest, err.Error())
	}
	if result != nil && !result.Accepted {
		return nil, statusErr(http.StatusBadRequest, result.Message)
	}
	if result == nil {
		result = &taskCancellationResult{}
	}
	result.TaskID = task.ID
	return result, nil
})

// deleteTaskResult reports the project a deleted task belonged to.
var actDeleteTask = defineAction("task.delete", ownTaskRef[TaskRef], func(h *Handler, ctx context.Context, a TaskRef) (string, error) {
	task, err := h.loadTask(ctx, a.TaskID)
	if err != nil {
		return "", err
	}
	return task.ProjectID, h.taskSvc.Delete(ctx, a.TaskID)
})

type taskCategoryArgs struct {
	TaskRef
	Category       models.TaskCategory
	TargetStatus   models.TaskStatus
	ExpectedStates string
}

func (a taskCategoryArgs) ref() TaskRef { return a.TaskRef }

var actUpdateTaskCategory = defineAction("task.category", ownTaskRef[taskCategoryArgs], func(h *Handler, ctx context.Context, a taskCategoryArgs) (string, error) {
	if a.Category == models.CategoryScheduled {
		schedules, err := h.scheduleRepo.ListByTask(ctx, a.TaskID)
		if err != nil {
			return "", err
		}
		if len(schedules) == 0 {
			return "", statusErr(http.StatusBadRequest, "Cannot move task to Scheduled category: task has no schedule. Create a schedule first.")
		}
	}
	if a.Category == models.CategoryActive {
		hasModels, err := h.llmConfigRepo.HasAny(ctx)
		if err != nil {
			return "", statusErr(http.StatusInternalServerError, "failed to check model availability")
		}
		if !hasModels {
			return "", errNoModels
		}
	}
	task, err := h.loadTask(ctx, a.TaskID)
	if err != nil {
		return "", err
	}
	if a.Category == models.CategoryActive && a.TargetStatus != "" {
		moves, err := h.activeLaneTaskMoves(ctx, []string{a.TaskID}, a.ExpectedStates)
		if err != nil {
			return "", err
		}
		if err := h.taskSvc.MoveTasksToActiveLane(ctx, task.ProjectID, moves, a.TargetStatus); err != nil {
			if errors.Is(err, persistence.ErrActiveLaneTaskChanged) || errors.Is(err, persistence.ErrActiveLaneLifecycleOwned) || errors.Is(err, service.ErrActiveLaneLifecycleRouted) {
				return "", statusErr(http.StatusConflict, "task lifecycle changed before the move completed")
			}
			return "", err
		}
		return task.ProjectID, nil
	}
	return task.ProjectID, h.taskSvc.UpdateCategory(ctx, a.TaskID, a.Category)
})

type taskStatusArgs struct {
	TaskRef
	Status models.TaskStatus
}

func (a taskStatusArgs) ref() TaskRef { return a.TaskRef }

var actUpdateTaskStatus = defineAction("task.status", ownTaskRef[taskStatusArgs], func(h *Handler, ctx context.Context, a taskStatusArgs) (string, error) {
	if err := h.taskSvc.UpdateStatus(ctx, a.TaskID, a.Status); err != nil {
		return "", err
	}
	task, err := h.loadTask(ctx, a.TaskID)
	if err != nil {
		return "", err
	}
	return task.ProjectID, nil
})

type reorderTaskArgs struct {
	TaskRef
	Scope    string
	BeforeID string
	AfterID  string
	Position string
}

func (a reorderTaskArgs) ref() TaskRef { return a.TaskRef }

var actReorderTask = defineAction("task.reorder", ownTaskRef[reorderTaskArgs], func(h *Handler, ctx context.Context, a reorderTaskArgs) (string, error) {
	var err error
	if a.BeforeID != "" || a.AfterID != "" {
		anchorID := a.BeforeID
		if anchorID == "" {
			anchorID = a.AfterID
		}
		err = h.taskSvc.ReorderTaskRelative(ctx, a.TaskID, anchorID, a.BeforeID != "", a.Scope)
	} else {
		position, parseErr := strconv.Atoi(a.Position)
		if parseErr != nil {
			return "", statusErr(http.StatusBadRequest, "invalid position")
		}
		err = h.taskSvc.ReorderTask(ctx, a.TaskID, position, a.Scope)
	}
	if err != nil {
		return "", err
	}
	task, err := h.loadTask(ctx, a.TaskID)
	if err != nil {
		return "", err
	}
	return task.ProjectID, nil
})

type taskGoalArgs struct {
	TaskRef
	Op   string
	Goal string
}

var actTaskGoal = defineAction("task.goal", func(h *Handler, ctx context.Context, a taskGoalArgs) (string, authz.Permission, error) {
	projectID, permission, err := a.owner(h, ctx)
	if a.Op == "get" {
		permission = authz.View
	}
	return projectID, permission, err
}, func(h *Handler, ctx context.Context, a taskGoalArgs) (*models.TaskGoal, error) {
	svc := h.taskGoalSvc
	if svc == nil {
		return nil, statusErr(http.StatusInternalServerError, "task goal service unavailable")
	}
	if a.ProjectID == "" {
		return nil, statusErr(http.StatusBadRequest, "project context required")
	}
	var err error
	switch a.Op {
	case "set":
		previous, err := svc.GetGoal(ctx, a.TaskID)
		if err != nil {
			return nil, err
		}
		if previous != nil && previous.Status != models.TaskGoalStatusCleared && previous.Objective == a.Goal {
			return previous, nil
		}
		goal, err := svc.SetGoal(ctx, a.TaskID, a.Goal, service.GoalOptions{Actor: "user"})
		if err == service.ErrTaskGoalEmpty || err == service.ErrTaskGoalTooLong {
			return nil, statusErr(http.StatusBadRequest, err.Error())
		}
		return goal, err
	case "pause":
		err = svc.PauseGoal(ctx, a.TaskID, "user")
	case "resume":
		err = svc.ResumeGoal(ctx, a.TaskID, "user")
	case "clear":
		err = svc.ClearGoal(ctx, a.TaskID, "user")
	case "get":
	default:
		return nil, statusErr(http.StatusBadRequest, "unknown goal operation")
	}
	if err == service.ErrTaskGoalNotFound {
		return nil, statusErr(http.StatusNotFound, err.Error())
	}
	if err != nil {
		return nil, err
	}
	return svc.GetGoal(ctx, a.TaskID)
})

type swarmArgs struct {
	TaskRef
	Op   string
	Role models.SwarmRole
}

func (a swarmArgs) ref() TaskRef { return a.TaskRef }

// actSwarm returns the started child task ID for reruns.
var actSwarm = defineAction("task.swarm", ownTaskRef[swarmArgs], func(h *Handler, ctx context.Context, a swarmArgs) (string, error) {
	if h.swarmSvc == nil {
		return "", statusErr(http.StatusInternalServerError, "swarm service unavailable")
	}
	switch a.Op {
	case "start":
		return "", h.swarmSvc.StartPlanner(ctx, a.TaskID)
	case "cancel":
		observed, _, err := h.taskSvc.ObserveTaskCancellation(ctx, a.TaskID)
		if err != nil {
			return "", err
		}
		if observed == nil {
			return "", statusErr(http.StatusNotFound, "swarm parent not found")
		}
		err = h.swarmSvc.CancelSwarmObservedWithPending(ctx, observed, nil)
		if errors.Is(err, service.ErrTaskCancellationSuperseded) {
			return "", statusErr(http.StatusConflict, err.Error())
		}
		return "", err
	case "rerun":
		if a.Role != models.SwarmRoleReviewer && a.Role != models.SwarmRoleMerger {
			return "", statusErr(http.StatusBadRequest, "invalid swarm role")
		}
		child, err := h.swarmSvc.RerunRole(ctx, a.TaskID, a.Role)
		if errors.Is(err, service.ErrSwarmRoleActive) {
			return "", statusErr(http.StatusConflict, "swarm role is already running; wait for it to finish before retrying")
		}
		if err != nil {
			return "", err
		}
		if child == nil {
			return "", statusErr(http.StatusNotFound, "swarm role task not found")
		}
		return child.ID, nil
	}
	return "", statusErr(http.StatusBadRequest, "unknown swarm operation")
})

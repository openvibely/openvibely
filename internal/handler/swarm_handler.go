package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

// GetSwarm returns a swarm parent and its child task summary.
// @Summary Get task swarm
// @Tags tasks
// @Produce json
// @Param id path string true "Task ID"
// @Success 200 {object} models.Task
// @Router /api/tasks/{id}/swarm [get]
func (h *Handler) GetSwarm(c echo.Context) error {
	id := c.Param("id")
	parent, err := h.taskRepo.GetByID(c.Request().Context(), id)
	if err != nil {
		return err
	}
	if parent == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	children, err := h.taskRepo.ListSwarmChildren(c.Request().Context(), id)
	if err != nil {
		return err
	}
	parent.SwarmChildren = children
	return c.JSON(http.StatusOK, parent)
}

// StartSwarm starts or resumes the planner for a swarm parent task.
// @Summary Start swarm planner
// @Tags tasks
// @Produce json
// @Param id path string true "Task ID"
// @Success 200 {object} map[string]string
// @Router /api/tasks/{id}/swarm/start [post]
func (h *Handler) StartSwarm(c echo.Context) error {
	if h.swarmSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "swarm service unavailable")
	}
	if err := h.swarmSvc.StartPlanner(c.Request().Context(), c.Param("id")); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "planner_started"})
}

// SwarmFollowup records swarm follow-up routing metadata.
// @Summary Send swarm follow-up
// @Tags tasks
// @Produce json
// @Param id path string true "Task ID"
// @Param message formData string false "Follow-up message"
// @Success 200 {object} map[string]string
// @Router /api/tasks/{id}/swarm/followup [post]
func (h *Handler) SwarmFollowup(c echo.Context) error {
	if h.swarmSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "swarm service unavailable")
	}
	id := c.Param("id")
	task, err := h.taskRepo.GetByID(c.Request().Context(), id)
	if err != nil || task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	message := c.FormValue("message")
	if message == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "message is required")
	}
	if task.SwarmRole == models.SwarmRoleParent {
		if err := h.swarmSvc.HandleParentFollowup(c.Request().Context(), id, message); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "accepted"})
	}
	if models.IsSwarmChildRole(task.SwarmRole) {
		return h.acceptSwarmChildFollowup(c, task, message)
	}
	return echo.NewHTTPError(http.StatusBadRequest, "task is not part of a swarm")
}

func (h *Handler) acceptSwarmChildFollowup(c echo.Context, task *models.Task, message string) error {
	ctx := c.Request().Context()
	agentID := c.FormValue("agent_id")
	agent, err := h.selectAgent(ctx, agentID, message, false)
	if err != nil {
		if task.AgentID != nil {
			agent, _ = h.llmConfigRepo.GetByID(ctx, *task.AgentID)
		}
		if agent == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "no agent available")
		}
	}
	admission, err := h.admitTaskFollowup(ctx, taskFollowupAdmissionRequest{
		Task:                task,
		Agent:               agent,
		Message:             message,
		Source:              models.TaskOriginWeb,
		LogPrefix:           "acceptSwarmChildFollowup",
		FatalQueueCareError: true,
	})
	if err != nil {
		if admissionErr, ok := err.(*taskFollowupAdmissionError); ok {
			switch admissionErr.Op {
			case taskFollowupAdmissionOpActiveCheck:
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to check active task turn")
			case taskFollowupAdmissionOpFirstTurnCheck:
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to check task queue")
			case taskFollowupAdmissionOpQueueUnavailable:
				return echo.NewHTTPError(http.StatusInternalServerError, "thread input queue is unavailable")
			case taskFollowupAdmissionOpQueueCreate:
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to queue follow-up")
			case taskFollowupAdmissionOpBind:
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to bind queued follow-up")
			case taskFollowupAdmissionOpPromotionCheck:
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to check queued follow-up promotion")
			case taskFollowupAdmissionOpDirectAdmission:
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to admit execution")
			case taskFollowupAdmissionOpSwarmChildRouting:
				return admissionErr.Err
			}
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to admit execution")
	}
	if admission.Queued != nil {
		return c.JSON(http.StatusOK, map[string]string{"status": "queued", "queued_input_id": admission.Queued.ID})
	}
	exec := admission.Execution
	task = admission.Task
	if err := h.startStreamingResponse(streamingResponseParams{
		ExecID:           exec.ID,
		TaskID:           task.ID,
		Message:          message,
		Agent:            *agent,
		ProjectID:        task.ProjectID,
		IsTaskFollowup:   true,
		InputOrigin:      models.TaskOriginWeb,
		DeferHistoryLoad: true,
		Task:             task,
	}); err != nil {
		c.Response().Header().Set("Retry-After", "30")
		return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "started", "execution_id": exec.ID})
}

// CancelSwarm cancels a swarm parent and runnable/running children.
// @Summary Cancel swarm
// @Tags tasks
// @Produce json
// @Param id path string true "Task ID"
// @Success 200 {object} map[string]string
// @Router /api/tasks/{id}/swarm/cancel [post]
func (h *Handler) CancelSwarm(c echo.Context) error {
	if h.swarmSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "swarm service unavailable")
	}
	if err := h.swarmSvc.CancelSwarm(c.Request().Context(), c.Param("id")); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "cancelled"})
}

// RerunSwarmReviewer reruns the reviewer child task.
// @Summary Rerun swarm reviewer
// @Tags tasks
// @Produce json
// @Param id path string true "Task ID"
// @Success 200 {object} map[string]string
// @Router /api/tasks/{id}/swarm/rerun-reviewer [post]
func (h *Handler) RerunSwarmReviewer(c echo.Context) error {
	return h.rerunSwarmRole(c, models.SwarmRoleReviewer)
}

// RerunSwarmMerger reruns the merger child task.
// @Summary Rerun swarm merger
// @Tags tasks
// @Produce json
// @Param id path string true "Task ID"
// @Success 200 {object} map[string]string
// @Router /api/tasks/{id}/swarm/rerun-merger [post]
// @Router /api/tasks/{id}/swarm/rerun-integrator [post]
func (h *Handler) RerunSwarmMerger(c echo.Context) error {
	return h.rerunSwarmRole(c, models.SwarmRoleMerger)
}

func (h *Handler) rerunSwarmRole(c echo.Context, role models.SwarmRole) error {
	if h.swarmSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "swarm service unavailable")
	}
	child, err := h.swarmSvc.RerunRole(c.Request().Context(), c.Param("id"), role)
	if err != nil {
		if errors.Is(err, service.ErrSwarmRoleActive) {
			return echo.NewHTTPError(http.StatusConflict, "swarm role is already running; wait for it to finish before retrying")
		}
		return err
	}
	if child == nil {
		return echo.NewHTTPError(http.StatusNotFound, "swarm role task not found")
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "started", "task_id": child.ID})
}

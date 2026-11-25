package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
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
	if _, err := actSwarm(h, c.Request().Context(), swarmArgs{TaskRef: TaskRef{TaskID: c.Param("id")}, Op: "start"}); err != nil {
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
	out, err := actSwarmFollowup(h, h.actionCtx(c), swarmFollowupArgs{
		TaskRef: TaskRef{TaskID: c.Param("id")},
		Message: c.FormValue("message"),
		AgentID: c.FormValue("agent_id"),
	})
	if err != nil {
		var httpErr *echo.HTTPError
		if errors.As(err, &httpErr) && httpErr.Code == http.StatusServiceUnavailable {
			c.Response().Header().Set("Retry-After", "30")
		}
		return err
	}
	return c.JSON(http.StatusOK, out)
}

type swarmFollowupArgs struct {
	TaskRef
	Message string `remote:"raw"`
	AgentID string
}

func (a swarmFollowupArgs) ref() TaskRef { return a.TaskRef }

var actSwarmFollowup = defineAction("swarm.followup", ownTaskRef[swarmFollowupArgs], func(h *Handler, ctx context.Context, a swarmFollowupArgs) (map[string]string, error) {
	if h.swarmSvc == nil {
		return nil, statusErr(http.StatusInternalServerError, "swarm service unavailable")
	}
	task, err := h.taskRepo.GetByID(ctx, a.TaskID)
	if err != nil || task == nil {
		return nil, statusErr(http.StatusNotFound, "task not found")
	}
	if a.Message == "" {
		return nil, statusErr(http.StatusBadRequest, "message is required")
	}
	if task.SwarmRole == models.SwarmRoleParent {
		if err := h.swarmSvc.HandleParentFollowup(ctx, a.TaskID, a.Message); err != nil {
			return nil, err
		}
		return map[string]string{"status": "accepted"}, nil
	}
	if models.IsSwarmChildRole(task.SwarmRole) {
		return h.acceptSwarmChildFollowup(ctx, task, a.Message, a.AgentID)
	}
	return nil, statusErr(http.StatusBadRequest, "task is not part of a swarm")
})

func (h *Handler) acceptSwarmChildFollowup(ctx context.Context, task *models.Task, message, agentID string) (map[string]string, error) {
	agent, err := h.selectAgent(ctx, agentID, message, false)
	if err != nil {
		if task.AgentID != nil {
			agent, _ = h.llmConfigRepo.GetByID(ctx, *task.AgentID)
		}
		if agent == nil {
			return nil, statusErr(http.StatusBadRequest, "no agent available")
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
				return nil, statusErr(http.StatusInternalServerError, "failed to check active task turn")
			case taskFollowupAdmissionOpFirstTurnCheck:
				return nil, statusErr(http.StatusInternalServerError, "failed to check task queue")
			case taskFollowupAdmissionOpQueueUnavailable:
				return nil, statusErr(http.StatusInternalServerError, "thread input queue is unavailable")
			case taskFollowupAdmissionOpQueueCreate:
				return nil, statusErr(http.StatusInternalServerError, "failed to queue follow-up")
			case taskFollowupAdmissionOpBind:
				return nil, statusErr(http.StatusInternalServerError, "failed to bind queued follow-up")
			case taskFollowupAdmissionOpPromotionCheck:
				return nil, statusErr(http.StatusInternalServerError, "failed to check queued follow-up promotion")
			case taskFollowupAdmissionOpDirectAdmission:
				return nil, statusErr(http.StatusInternalServerError, "failed to admit execution")
			case taskFollowupAdmissionOpSwarmChildRouting:
				return nil, admissionErr.Err
			}
		}
		return nil, statusErr(http.StatusInternalServerError, "failed to admit execution")
	}
	if admission.Queued != nil {
		return map[string]string{"status": "queued", "queued_input_id": admission.Queued.ID}, nil
	}
	exec := admission.Execution
	task = admission.Task
	if err := h.startStreamingResponse(streamingResponseParams{
		PrincipalID:      principalIDFromContext(ctx),
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
		return nil, statusErr(http.StatusServiceUnavailable, err.Error())
	}
	return map[string]string{"status": "started", "execution_id": exec.ID}, nil
}

// CancelSwarm cancels a swarm parent and runnable/running children.
// @Summary Cancel swarm
// @Tags tasks
// @Produce json
// @Param id path string true "Task ID"
// @Success 200 {object} map[string]string
// @Router /api/tasks/{id}/swarm/cancel [post]
func (h *Handler) CancelSwarm(c echo.Context) error {
	if _, err := actSwarm(h, c.Request().Context(), swarmArgs{TaskRef: TaskRef{TaskID: c.Param("id")}, Op: "cancel"}); err != nil {
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
	childID, err := actSwarm(h, c.Request().Context(), swarmArgs{TaskRef: TaskRef{TaskID: c.Param("id")}, Op: "rerun", Role: role})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "started", "task_id": childID})
}

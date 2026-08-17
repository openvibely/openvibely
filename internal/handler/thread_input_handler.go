package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/web/templates/components"
)

func (h *Handler) CancelThreadInput(c echo.Context) error {
	inputID := c.Param("inputId")
	if inputID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "input id is required")
	}
	if h.threadInputRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "thread input queue is unavailable")
	}
	cancelled, err := h.threadInputRepo.CancelPending(c.Request().Context(), inputID)
	if err != nil {
		if errors.Is(err, repository.ErrInputNotPending) {
			// The row is already applied, already cancelled, or is a prepared/in-flight steering
			// row currently being consumed by a provider call.  In all cases it is no longer
			// user-removable via this action.  Return the hidden-row fragment so HTMX removes
			// any stale pending row from the composer UI instead of leaving it stuck.
			return render(c, http.StatusOK, components.ChatInputCancelledRow(inputID))
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to cancel queued input")
	}
	h.publishThreadInputCancelledEvent(cancelled)
	return render(c, http.StatusOK, components.ChatInputCancelledRow(inputID))
}

func (h *Handler) publishThreadInputCancelledEvent(input *models.ThreadInput) {
	if input == nil {
		return
	}
	if h.broadcaster != nil && input.TaskID != "" {
		h.broadcaster.Publish(events.TaskEvent{
			Type:           events.TaskThreadInputCancelled,
			ProjectID:      input.ProjectID,
			TaskID:         input.TaskID,
			ExecID:         input.RunExecutionID,
			PendingInputID: input.ID,
		})
	}
	if h.chatBroadcaster != nil && input.Scope == models.ThreadInputScopeChat {
		h.chatBroadcaster.Publish(events.ChatEvent{
			Type:           events.ChatThreadInputCancelled,
			ProjectID:      input.ProjectID,
			ExecID:         input.RunExecutionID,
			TaskID:         input.TaskID,
			PendingInputID: input.ID,
		})
	}
}

// convertQueuedInputToSteering contains the shared logic for both steer handlers:
// expected-turn-ID guard, ConvertQueuedToSteering call, all three error-type branches,
// and nil-steering guard. Callers supply a findActiveExecution callback that resolves
// the surface-specific active execution (chat vs. task thread).
func (h *Handler) convertQueuedInputToSteering(
	ctx context.Context,
	input *models.ThreadInput,
	findActiveExecution func() (*models.Execution, error),
) (*models.ThreadInput, error) {
	active, err := findActiveExecution()
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "failed to check active response")
	}
	if active == nil {
		return nil, echo.NewHTTPError(http.StatusConflict, "no active response to steer")
	}
	expectedTurnID := input.RunExecutionID
	if expectedTurnID == "" {
		return nil, echo.NewHTTPError(http.StatusConflict, "queued input is missing its active turn guard; refresh and queue the message again")
	}
	steering, err := h.threadInputRepo.ConvertQueuedToSteering(ctx, input.ID, active.ID, expectedTurnID)
	if err != nil {
		if errors.Is(err, repository.ErrInputNotPending) {
			return nil, echo.NewHTTPError(http.StatusConflict, "queued input is no longer pending")
		}
		if errors.Is(err, repository.ErrNoActiveTurn) || errors.Is(err, repository.ErrActiveTurnChanged) {
			return nil, echo.NewHTTPError(http.StatusConflict, "active turn changed; refresh and queue the message instead")
		}
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "failed to convert queued input")
	}
	if steering == nil {
		return nil, echo.NewHTTPError(http.StatusConflict, "queued input is no longer pending")
	}
	return steering, nil
}

func (h *Handler) ChatQueuedInputSteer(c echo.Context) error {
	inputID := c.Param("inputId")
	if inputID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "input id is required")
	}
	if h.threadInputRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "thread input queue is unavailable")
	}
	input, err := h.threadInputRepo.GetByID(c.Request().Context(), inputID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load queued input")
	}
	if input == nil || input.InputStatus != models.ThreadInputPending || input.InputMode != models.ThreadInputModeQueued {
		return echo.NewHTTPError(http.StatusConflict, "queued input is no longer pending")
	}
	ctx := c.Request().Context()
	steering, err := h.convertQueuedInputToSteering(ctx, input, func() (*models.Execution, error) {
		return h.execRepo.FindLatestActiveChatExecution(ctx, input.ProjectID)
	})
	if err != nil {
		return err
	}
	if h.chatBroadcaster != nil {
		h.chatBroadcaster.Publish(events.ChatEvent{
			Type:           events.ChatTurnSteered,
			ProjectID:      steering.ProjectID,
			ExecID:         steering.ID,
			Message:        steering.Content,
			Source:         string(steering.Source),
			Steering:       true,
			HasAttachments: steering.AttachmentSessionID != "",
		})
	}
	return render(c, http.StatusOK, components.ChatSteeringInputRow(steering.ID, steering.Content, steering.AttachmentSessionID != ""))
}

func (h *Handler) TaskThreadQueuedInputSteer(c echo.Context) error {
	taskID := c.Param("taskId")
	inputID := c.Param("inputId")
	if taskID == "" || inputID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "task id and input id are required")
	}
	if h.threadInputRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "thread input queue is unavailable")
	}
	input, err := h.threadInputRepo.GetByID(c.Request().Context(), inputID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load queued input")
	}
	if input == nil || input.TaskID != taskID || input.InputStatus != models.ThreadInputPending || input.InputMode != models.ThreadInputModeQueued {
		return echo.NewHTTPError(http.StatusConflict, "queued input is no longer pending")
	}
	ctx := c.Request().Context()
	steering, err := h.convertQueuedInputToSteering(ctx, input, func() (*models.Execution, error) {
		return h.execRepo.FindActiveTaskExecution(ctx, taskID, "")
	})
	if err != nil {
		return err
	}
	if h.broadcaster != nil {
		h.broadcaster.Publish(events.TaskEvent{
			Type:           events.TaskThreadInputSteered,
			ProjectID:      steering.ProjectID,
			TaskID:         steering.TaskID,
			ExecID:         steering.RunExecutionID,
			Message:        steering.Content,
			PendingInputID: steering.ID,
			HasAttachments: steering.AttachmentSessionID != "",
		})
	}
	return render(c, http.StatusOK, components.ChatSteeringInputRowForTask(steering.ID, steering.Content, steering.AttachmentSessionID != "", taskID))
}

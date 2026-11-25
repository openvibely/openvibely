package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/web/templates/components"
)

const threadInputMutationStatusHeader = "X-OpenVibely-Thread-Input-Status"

// EditThreadInput updates only inputs that have not entered consumption.
func (h *Handler) EditThreadInput(c echo.Context) error {
	content := strings.TrimSpace(c.FormValue("content"))
	if content == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Message cannot be empty")
	}
	out, err := actInputMutation(h, h.actionCtx(c), inputMutationArgs{InputID: c.Param("inputId"), Op: "edit", Content: content, EditToken: c.FormValue("edit_token"), Resume: c.FormValue("resume") == "true"})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]string{"content": out.Content})
}

func (h *Handler) CancelThreadInput(c echo.Context) error {
	id := c.Param("inputId")
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "input id is required")
	}
	out, err := actInputMutation(h, h.actionCtx(c), inputMutationArgs{InputID: id, Op: "cancel"})
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) && httpErr.Code == http.StatusNotFound {
		out.Status, err = "not_pending", nil
	}
	if err != nil {
		return err
	}
	c.Response().Header().Set(threadInputMutationStatusHeader, out.Status)
	return render(c, http.StatusOK, components.ChatInputCancelledRow(id))
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
		h.publishChatEvent(events.ChatEvent{
			Type:           events.ChatThreadInputCancelled,
			ProjectID:      input.ProjectID,
			ExecID:         input.RunExecutionID,
			TaskID:         input.TaskID,
			PendingInputID: input.ID,
		})
	}
}

// convertQueuedInputToSteering contains the shared logic for both steer handlers:
// the supplied expected-turn-ID guard, ConvertQueuedToSteering call, all three
// error-type branches, and nil-steering guard. Callers supply a findActiveExecution
// callback that resolves the surface-specific active execution (chat vs. task thread).
func (h *Handler) convertQueuedInputToSteering(
	ctx context.Context,
	input *models.ThreadInput,
	expectedTurnID string,
	findActiveExecution func() (*models.Execution, error),
) (*models.ThreadInput, error) {
	if input.Scope == models.ThreadInputScopeTask {
		binding, done, err := h.beginPageSteering(ctx, input.TaskID)
		if err != nil {
			return nil, err
		}
		if binding != nil {
			defer done()
		}
	}
	active, err := findActiveExecution()
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "failed to check active response")
	}
	if active == nil {
		return nil, echo.NewHTTPError(http.StatusConflict, "no active response to steer")
	}
	expectedTurnID = strings.TrimSpace(expectedTurnID)
	if expectedTurnID == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "expected turn id is required")
	}
	steering, err := h.threadInputRepo.ConvertQueuedToSteering(ctx, input.ID, active.ID, expectedTurnID)
	if err != nil {
		if errors.Is(err, persistence.ErrInputNotPending) {
			return nil, echo.NewHTTPError(http.StatusConflict, "queued input is no longer pending")
		}
		if errors.Is(err, persistence.ErrNoActiveTurn) || errors.Is(err, persistence.ErrActiveTurnChanged) {
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
	a := queuedSteerArgs{InputID: c.Param("inputId"), TaskID: c.Param("taskId"), ExpectedTurnID: c.FormValue("expected_turn_id")}
	steering, err := actQueuedChatSteer(h, h.actionCtx(c), a)
	if err != nil {
		return err
	}
	return render(c, http.StatusOK, components.ChatSteeringInputRow(steering.ID, steering.Content, steering.AttachmentSessionID != "", steering.AuthorName))
}

func (h *Handler) TaskThreadQueuedInputSteer(c echo.Context) error {
	a := queuedSteerArgs{InputID: c.Param("inputId"), TaskID: c.Param("taskId"), ExpectedTurnID: c.FormValue("expected_turn_id")}
	steering, err := actQueuedTaskSteer(h, h.actionCtx(c), a)
	if err != nil {
		return err
	}
	return render(c, http.StatusOK, components.ChatSteeringInputRowForTask(steering.ID, steering.Content, steering.AttachmentSessionID != "", a.TaskID, steering.AuthorName))
}

// Editing holds are renewed by the open editor and expire after disconnects.
func (h *Handler) HoldThreadInputEdit(c echo.Context) error {
	op := "hold"
	if c.FormValue("renew") == "true" {
		op = "renew"
	}
	out, err := actInputMutation(h, h.actionCtx(c), inputMutationArgs{InputID: c.Param("inputId"), Op: op, EditToken: c.FormValue("edit_token"), Hold: c.FormValue("hold") != "false"})
	if err != nil {
		return err
	}
	if op == "renew" {
		return c.NoContent(http.StatusNoContent)
	}
	return c.JSON(http.StatusOK, map[string]string{"content": out.Content})
}

func (h *Handler) resumeEditedInput(input *models.ThreadInput) {
	if input.InputMode != models.ThreadInputModeQueued {
		return
	}
	h.goStartNextQueuedTurnAfter(streamingResponseParams{ProjectID: input.ProjectID, TaskID: input.TaskID, IsTaskFollowup: input.Scope == models.ThreadInputScopeTask}, "")
}

// RecoverExpiredInputEdits also resumes queues whose active turn ended during editing.
func (h *Handler) RecoverExpiredInputEdits(ctx context.Context) {
	if h.threadInputRepo == nil {
		return
	}
	inputs, err := h.threadInputRepo.ReleaseExpiredEditHolds(ctx)
	if err != nil {
		applog.Infof("recover expired input edits: %v", err)
		return
	}
	for _, input := range inputs {
		if input.InputStatus == models.ThreadInputPending {
			h.resumeEditedInput(&input)
		}
	}
}

type queuedSteerArgs struct {
	InputID        string
	TaskID         string
	ExpectedTurnID string
}

func ownQueuedSteer(h *Handler, ctx context.Context, a queuedSteerArgs) (string, authz.Permission, error) {
	project, permission, err := inputOwner(h, ctx, a.InputID)
	var status *remote.StatusError
	if errors.As(err, &status) && status.Status == http.StatusNotFound {
		return "", "", statusErr(http.StatusConflict, "queued input is no longer pending")
	}
	return project, permission, err
}

var actQueuedChatSteer = defineAction("chat.input.steer", ownQueuedSteer, func(h *Handler, ctx context.Context, a queuedSteerArgs) (*models.ThreadInput, error) {
	inputID := a.InputID
	if inputID == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "input id is required")
	}
	if h.threadInputRepo == nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "thread input queue is unavailable")
	}
	input, err := h.threadInputRepo.GetByID(ctx, inputID)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "failed to load queued input")
	}
	if input == nil || input.InputStatus != models.ThreadInputPending || input.InputMode != models.ThreadInputModeQueued {
		return nil, echo.NewHTTPError(http.StatusConflict, "queued input is no longer pending")
	}

	steering, err := h.convertQueuedInputToSteering(ctx, input, input.RunExecutionID, func() (*models.Execution, error) {
		return h.execRepo.FindLatestActiveChatExecution(ctx, input.ProjectID)
	})
	if err != nil {
		return nil, err
	}
	if h.chatBroadcaster != nil {
		h.publishChatEvent(events.ChatEvent{
			Type:           events.ChatTurnSteered,
			ProjectID:      steering.ProjectID,
			ExecID:         steering.ID,
			Message:        steering.Content,
			Source:         string(steering.Source),
			Steering:       true,
			HasAttachments: steering.AttachmentSessionID != "",
		})
	}
	return steering, nil
})
var actQueuedTaskSteer = defineAction("thread.input.steer", ownQueuedSteer, func(h *Handler, ctx context.Context, a queuedSteerArgs) (*models.ThreadInput, error) {
	taskID := a.TaskID
	inputID := a.InputID
	if taskID == "" || inputID == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "task id and input id are required")
	}
	if h.threadInputRepo == nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "thread input queue is unavailable")
	}
	input, err := h.threadInputRepo.GetByID(ctx, inputID)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "failed to load queued input")
	}
	if input == nil || input.TaskID != taskID || input.InputStatus != models.ThreadInputPending || input.InputMode != models.ThreadInputModeQueued {
		return nil, echo.NewHTTPError(http.StatusConflict, "queued input is no longer pending")
	}

	expectedTurnID := a.ExpectedTurnID
	steering, err := h.convertQueuedInputToSteering(ctx, input, expectedTurnID, func() (*models.Execution, error) {
		return h.activeConversationTaskExecution(ctx, taskID)
	})
	if err != nil {
		return nil, err
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
	return steering, nil
})

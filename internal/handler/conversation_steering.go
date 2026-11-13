package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/pageintegration"
	"github.com/openvibely/openvibely/internal/persistence"
)

// All task-scoped conversations share steering; the category selects the active
// execution without changing the model's orchestration/coding mode.
func (h *Handler) activeConversationTaskExecution(ctx context.Context, taskID string) (*models.Execution, error) {
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return nil, err
	}
	if task.Category == models.CategoryPageChat {
		return h.pageChatActiveExecution(ctx, taskID)
	}
	return h.execRepo.FindActiveTaskThreadExecution(ctx, taskID, "")
}

func (h *Handler) beginPageSteering(ctx context.Context, taskID string) (*pageintegration.Conversation, func(), error) {
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return nil, nil, err
	}
	if task == nil || task.Category != models.CategoryPageChat {
		return nil, nil, nil
	}
	binding, err := h.recoverPageConversation(ctx, task)
	if err != nil {
		return nil, nil, err
	}
	_, release, err := binding.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	unlock := persistence.LockTaskLifecycle(binding.LockKey())
	return &binding, func() { unlock(); release() }, nil
}

// Save page provenance before making a steer consumable. Failed provider turns
// can then use the same durable queue recovery as any other page submission.
func (h *Handler) saveConversationSteering(c echo.Context, input *models.ThreadInput, active *models.Execution) error {
	ctx := c.Request().Context()
	if input.Scope != models.ThreadInputScopeTask {
		return h.threadInputRepo.CreateSteeringForActiveExecution(ctx, input, active.ID)
	}
	binding, done, err := h.beginPageSteering(ctx, input.TaskID)
	if err != nil {
		return err
	}
	if binding == nil {
		return h.threadInputRepo.CreateSteeringForActiveExecution(ctx, input, active.ID)
	}
	defer done()
	if input.AttachmentSessionID != "" && !isValidPendingAttachmentSessionID(input.AttachmentSessionID) {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid attachment session ID")
	}
	turn := pageQueuedContext{Principal: h.authPrincipalID(c)}
	if snap, ok := binding.Contexts.Get(turn.Principal, binding.Page); ok {
		turn.Snapshot = &snap
	}
	input.AgentConfigID = active.AgentConfigID
	input.Source = models.TaskOriginWeb
	expected := input.ExpectedTurnID
	input.InputMode = models.ThreadInputModeQueued
	if err = h.threadInputRepo.CreateQueued(ctx, input); err != nil {
		return err
	}
	raw, err := json.Marshal(turn)
	if err == nil {
		err = binding.Store.SaveQueuedContext(ctx, input.ID, string(raw))
	}
	if err == nil {
		var converted *models.ThreadInput
		converted, err = h.threadInputRepo.ConvertQueuedToSteering(ctx, input.ID, active.ID, expected)
		if err == nil {
			*input = *converted
			return nil
		}
	}
	_, _ = h.threadInputRepo.CancelPending(ctx, input.ID)
	return err
}

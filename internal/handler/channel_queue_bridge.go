package handler

import (
	"context"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
)

// PromoteQueuedChatInput starts the next pending queued chat input for a project
// if no chat response is currently running. Channel services call this after
// completing their own chat-origin runs so channel/web/API follow-ups queued
// behind that run are delivered by the shared chat runner.
func (h *Handler) PromoteQueuedChatInput(projectID string) {
	if projectID == "" {
		return
	}
	h.startNextQueuedTurnAfter(context.Background(), streamingResponseParams{
		ProjectID: projectID,
		ChatMode:  models.ChatModeOrchestrate,
	}, "")
}

// PromoteQueuedTaskThreadInput starts the next pending queued task-thread input
// after a worker-managed task execution reaches a terminal state.
func (h *Handler) PromoteQueuedTaskThreadInput(taskID string) {
	if taskID == "" || h.taskRepo == nil {
		return
	}
	task, err := h.taskRepo.GetByID(context.Background(), taskID)
	if err != nil {
		applog.Infof("[handler] PromoteQueuedTaskThreadInput task=%s load error: %v", taskID, err)
		return
	}
	if task == nil {
		applog.Infof("[handler] PromoteQueuedTaskThreadInput task=%s not found", taskID)
		return
	}
	applog.Infof("[handler] PromoteQueuedTaskThreadInput task=%s checking queue", taskID)
	h.startNextQueuedTurnAfter(context.Background(), streamingResponseParams{
		ProjectID:      task.ProjectID,
		TaskID:         task.ID,
		IsTaskFollowup: true,
	}, "")
}

func (h *Handler) RecoverQueuedTaskThreadInputs(ctx context.Context) {
	if h.threadInputRepo == nil {
		return
	}
	const batchSize = 100
	afterTaskID := ""
	for {
		ids, err := h.threadInputRepo.ListRecoverableQueuedTaskIDsAfter(ctx, afterTaskID, batchSize)
		if err != nil {
			applog.Infof("[handler] RecoverQueuedTaskThreadInputs list error: %v", err)
			return
		}
		for _, taskID := range ids {
			applog.Infof("[handler] RecoverQueuedTaskThreadInputs promoting stranded queued input task=%s", taskID)
			h.PromoteQueuedTaskThreadInput(taskID)
		}
		if len(ids) < batchSize {
			return
		}
		afterTaskID = ids[len(ids)-1]
	}
}

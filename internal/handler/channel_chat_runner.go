package handler

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

// StartChannelChatRun hands a channel-origin chat turn to the same streaming
// processor used by web/API Chat so pending steering and queued promotion are
// applied consistently across all chat surfaces.
func (h *Handler) StartChannelChatRun(ctx context.Context, req service.ChannelChatRunRequest) {
	if req.ExecID == "" || req.TaskID == "" || req.ProjectID == "" {
		return
	}
	h.startStreamingResponse(streamingResponseParams{
		ExecID:                      req.ExecID,
		TaskID:                      req.TaskID,
		Message:                     req.Message,
		Agent:                       req.Agent,
		ChatHistory:                 filterChatHistory(req.ChatHistory, req.ExecID),
		ProjectID:                   req.ProjectID,
		SystemContext:               req.SystemContext,
		WorkDir:                     req.WorkDir,
		ImageAttachments:            req.ImageAttachments,
		IsTaskFollowup:              false,
		ChatMode:                    models.ChatModeOrchestrate,
		Surface:                     req.Surface,
		TelegramInitialAckMessageID: req.InitialAckMessageID,
		ChannelReply:                req.ReplyContext,
		InputOrigin:                 req.ReplyContext.Source,
		RuntimeTools:                req.RuntimeTools,
	})
}

func (h *Handler) StartChannelTaskRun(ctx context.Context, req service.ChannelTaskRunRequest) {
	if req.ExecID == "" || req.TaskID == "" || req.ProjectID == "" {
		return
	}
	task, err := h.taskRepo.GetByID(ctx, req.TaskID)
	if err != nil || task == nil {
		msg := "task not found"
		if err != nil {
			msg = err.Error()
		}
		h.completeWithFailure(context.Background(), req.ExecID, req.TaskID, msg, 0, req.ReplyContext)
		return
	}
	if err := h.applySwarmChildFollowupStart(ctx, task, req.Message); err != nil {
		h.completeWithFailure(context.Background(), req.ExecID, req.TaskID, err.Error(), 0, req.ReplyContext)
		return
	}
	workDir, worktreeContext, workDirErr := h.resolveWorktreeWorkDir(ctx, task)
	if workDirErr != nil {
		h.completeWithFailure(context.Background(), req.ExecID, req.TaskID, workDirErr.Error(), 0, req.ReplyContext)
		go h.startNextQueuedTurnAfter(context.Background(), streamingResponseParams{ProjectID: req.ProjectID, TaskID: req.TaskID, IsTaskFollowup: true}, req.ExecID)
		return
	}
	h.resumeUserStoppedGoalForManualStart(ctx, req.TaskID, req.ReplyContext.Source, "")
	h.reactivateAchievedGoalForManualFollowup(ctx, req.TaskID, req.ReplyContext.Source, "")
	agentDef := h.resolveTaskAgentDefinitionForTask(ctx, req.TaskID, req.AgentDefinition)
	systemContext := combineContexts(combineContexts(req.SystemContext, h.taskGoalContext(ctx, req.TaskID, agentDef)), worktreeContext)
	h.startStreamingResponse(streamingResponseParams{
		ExecID:          req.ExecID,
		TaskID:          req.TaskID,
		Message:         req.Message,
		Agent:           req.Agent,
		AgentDefinition: agentDef,
		ChatHistory:     filterChatHistory(req.ChatHistory, req.ExecID),
		ProjectID:       req.ProjectID,
		SystemContext:   systemContext,
		WorkDir:         workDir,
		IsTaskFollowup:  true,
		ChatMode:        models.ChatModeOrchestrate,
		Surface:         req.Surface,
		ChannelReply:    req.ReplyContext,
		InputOrigin:     req.ReplyContext.Source,
	})
}

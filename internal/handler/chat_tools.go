package handler

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
)

// executeChatTaskEdits applies typed task-edit requests and returns a summary.
// Delegates to the shared service.ExecuteTaskEdits with attachment support.
func executeChatTaskEdits(ctx context.Context, requests []service.TaskEditRequest, projectID string, taskSvc *service.TaskService, attachmentRepo persistence.AttachmentStore, uploadsDir string) string {
	return service.ExecuteTaskEdits(ctx, requests, projectID, taskSvc, attachmentRepo, uploadsDir)
}

// executeChatTaskExecutions executes tasks matching typed action requests and returns a summary.
// Delegates to the shared service.ExecuteTaskExecutions.
func executeChatTaskExecutions(ctx context.Context, requests []service.TaskExecutionRequest, projectID string, taskSvc *service.TaskService) string {
	return service.ExecuteTaskExecutions(ctx, requests, projectID, taskSvc)
}

// executeChatTaskCreationsWithAttachments creates tasks and returns indexed results
// so attachment activation remains tied to the originating request.
func (h *Handler) executeChatTaskCreationsWithAttachments(ctx context.Context, requests []service.TaskCreationRequest, projectID string, executionID string, agents []models.LLMConfig) ([]service.TaskCreationResult, string) {
	return service.ExecuteTaskCreationsWithIndexedReturn(ctx, requests, projectID, h.taskSvc, agents)
}

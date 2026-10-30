package handler

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
)

// executeChatTaskCreations creates tasks from typed action requests and returns a summary.
// Delegates to the shared service.ExecuteTaskCreations.
// If agents is provided, auto-selects an agent for each task based on prompt complexity.
func executeChatTaskCreations(ctx context.Context, requests []service.TaskCreationRequest, projectID string, taskSvc *service.TaskService, agents ...[]models.LLMConfig) string {
	_, summary := service.ExecuteTaskCreationsWithReturn(ctx, requests, projectID, taskSvc, agents...)
	return summary
}

// executeChatTaskEdits applies typed task-edit requests and returns a summary.
// Delegates to the shared service.ExecuteTaskEdits with attachment support.
func executeChatTaskEdits(ctx context.Context, requests []service.TaskEditRequest, projectID string, taskSvc *service.TaskService, attachmentRepo persistence.AttachmentStore, uploadsDir string) string {
	return service.ExecuteTaskEdits(ctx, requests, projectID, taskSvc, attachmentRepo, uploadsDir)
}

// buildTaskContextString creates a summary of existing tasks for the chat system prompt.
// Delegates to the shared service.BuildTaskContextString.
func buildTaskContextString(tasks []models.Task) string {
	return service.BuildTaskContextString(tasks)
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

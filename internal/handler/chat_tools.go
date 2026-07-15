package handler

import (
	"context"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
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
func executeChatTaskEdits(ctx context.Context, requests []service.TaskEditRequest, projectID string, taskSvc *service.TaskService, attachmentRepo *repository.AttachmentRepo, uploadsDir string) string {
	return service.ExecuteTaskEdits(ctx, requests, projectID, taskSvc, attachmentRepo, uploadsDir)
}

// buildTaskContextString creates a summary of existing tasks for the chat system prompt.
// Delegates to the shared service.BuildTaskContextString.
func buildTaskContextString(tasks []models.Task) string {
	return service.BuildTaskContextString(tasks)
}

// buildTaskContextWithModels creates a summary of existing tasks including their assigned model.
func buildTaskContextWithModels(tasks []models.Task, modelMap map[string]models.LLMConfig) string {
	return service.BuildTaskContextWithModels(tasks, modelMap)
}

// buildModelContextString creates a summary of available model configs for the chat system prompt.
func buildModelContextString(configs []models.LLMConfig) string {
	return service.BuildModelContextString(configs)
}

// buildScheduleContextString creates a summary of scheduled tasks with schedule details for the chat system prompt.
func buildScheduleContextString(tasks []models.Task, scheduleMap map[string][]models.Schedule, now time.Time) string {
	return service.BuildScheduleContextString(tasks, scheduleMap, now)
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

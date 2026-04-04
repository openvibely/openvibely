package handler

import (
	"context"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
)

// parseChatTaskCreations extracts task creation requests from AI chat output.
// Delegates to the shared service.ParseTaskCreations.
func parseChatTaskCreations(output string) []service.TaskCreationRequest {
	return service.ParseTaskCreations(output)
}

// executeChatTaskCreations creates tasks from parsed requests and returns a summary.
// Delegates to the shared service.ExecuteTaskCreations.
// If agents is provided, auto-selects an agent for each task based on prompt complexity.
func executeChatTaskCreations(ctx context.Context, requests []service.TaskCreationRequest, projectID string, taskSvc *service.TaskService, agents ...[]models.LLMConfig) string {
	_, summary := service.ExecuteTaskCreationsWithReturn(ctx, requests, projectID, taskSvc, agents...)
	return summary
}

// parseChatTaskEdits extracts task edit requests from AI chat output.
// Delegates to the shared service.ParseTaskEdits.
func parseChatTaskEdits(output string) []service.TaskEditRequest {
	return service.ParseTaskEdits(output)
}

// executeChatTaskEdits applies task edits from parsed requests and returns a summary.
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

// parseChatTaskExecutions extracts task execution requests from AI chat output.
// Delegates to the shared service.ParseTaskExecutions.
func parseChatTaskExecutions(output string) []service.TaskExecutionRequest {
	return service.ParseTaskExecutions(output)
}

// executeChatTaskExecutions executes tasks matching the parsed execution requests and returns a summary.
// Delegates to the shared service.ExecuteTaskExecutions.
func executeChatTaskExecutions(ctx context.Context, requests []service.TaskExecutionRequest, projectID string, taskSvc *service.TaskService) string {
	return service.ExecuteTaskExecutions(ctx, requests, projectID, taskSvc)
}

// executeChatTaskCreationsWithAttachments creates tasks and returns both the created tasks and a summary.
// Used when we need to copy attachments after task creation.
func (h *Handler) executeChatTaskCreationsWithAttachments(ctx context.Context, requests []service.TaskCreationRequest, projectID string, executionID string, agents []models.LLMConfig) ([]models.Task, string) {
	return service.ExecuteTaskCreationsWithReturn(ctx, requests, projectID, h.taskSvc, agents)
}

// parseViewThread extracts view thread requests from AI chat output.
func parseViewThread(output string) []service.ViewThreadRequest {
	return service.ParseViewThread(output)
}

// parseChatSendToTask extracts send-to-task requests from AI chat output.
func parseChatSendToTask(output string) []service.SendToTaskRequest {
	return service.ParseSendToTask(output)
}

// parseChatScheduleTask extracts schedule task requests from AI chat output.
func parseChatScheduleTask(output string) []service.ScheduleTaskRequest {
	return service.ParseScheduleTask(output)
}

// parseChatDeleteSchedule extracts delete schedule requests from AI chat output.
func parseChatDeleteSchedule(output string) []service.DeleteScheduleRequest {
	return service.ParseDeleteSchedule(output)
}

// parseChatModifySchedule extracts modify schedule requests from AI chat output.
func parseChatModifySchedule(output string) []service.ModifyScheduleRequest {
	return service.ParseModifySchedule(output)
}

// parseChatCreateAlert extracts create alert requests from AI chat output.
func parseChatCreateAlert(output string) []service.CreateAlertRequest {
	return service.ParseCreateAlert(output)
}

// parseChatDeleteAlert extracts delete alert requests from AI chat output.
func parseChatDeleteAlert(output string) []service.DeleteAlertRequest {
	return service.ParseDeleteAlert(output)
}

// parseChatToggleAlert extracts toggle alert requests from AI chat output.
func parseChatToggleAlert(output string) []service.ToggleAlertRequest {
	return service.ParseToggleAlert(output)
}

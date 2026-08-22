package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// RuntimeTaskCreationCreateFunc lets surfaces preserve their creation-specific
// behavior while sharing ordinary create_task action preflight and response handling.
type RuntimeTaskCreationCreateFunc func(context.Context, TaskCreationRequest, []models.LLMConfig) ([]models.Task, string, bool, error)

// RuntimeTaskCreationOptions contains the surface hooks for the ordinary
// create_task runtime action.
type RuntimeTaskCreationOptions struct {
	ProjectID           string
	TaskSvc             *TaskService
	LLMConfigRepo       *repository.LLMConfigRepo
	PrepareTaskCreation func(context.Context, *TaskCreationRequest) error
	CreateTask          RuntimeTaskCreationCreateFunc
	OnTasksCreated      func(context.Context, []TaskCreationRequest, []models.Task) error
	AddCreatedSummary   func(string)
	RequireCreated      bool
	VerifyCreated       func(context.Context, string, []models.Task) error
}

// ExecuteCreateTaskRuntimeAction executes the shared ordinary create_task runtime
// action contract: decode, prepare, validate, default, load model configs,
// create, run callbacks, collect the summary, and return a trimmed response.
func ExecuteCreateTaskRuntimeAction(ctx context.Context, input json.RawMessage, opts RuntimeTaskCreationOptions) (string, []models.Task, error) {
	if opts.TaskSvc == nil {
		return "", nil, fmt.Errorf("create_task: task service unavailable")
	}
	var req TaskCreationRequest
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", nil, err
	}
	if opts.PrepareTaskCreation != nil {
		if err := opts.PrepareTaskCreation(ctx, &req); err != nil {
			return "", nil, err
		}
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Prompt) == "" {
		return "", nil, fmt.Errorf("create_task requires title and prompt")
	}
	if req.Priority == 0 {
		req.Priority = 2
	}

	agents := []models.LLMConfig(nil)
	if opts.LLMConfigRepo != nil {
		if loaded, err := opts.LLMConfigRepo.List(ctx); err == nil {
			agents = loaded
		}
	}

	createdTasks := []models.Task(nil)
	summary := ""
	creationHandled := false
	if opts.CreateTask != nil {
		var err error
		createdTasks, summary, creationHandled, err = opts.CreateTask(ctx, req, agents)
		if err != nil {
			return "", nil, err
		}
	}
	if !creationHandled {
		createdTasks, summary = ExecuteTaskCreationsWithReturn(ctx, []TaskCreationRequest{req}, opts.ProjectID, opts.TaskSvc, agents)
	}
	if !creationHandled && opts.OnTasksCreated != nil && len(createdTasks) > 0 {
		if err := opts.OnTasksCreated(ctx, []TaskCreationRequest{req}, createdTasks); err != nil {
			return "", createdTasks, err
		}
	}
	if opts.VerifyCreated != nil {
		if err := opts.VerifyCreated(ctx, summary, createdTasks); err != nil {
			return strings.TrimSpace(summary), createdTasks, err
		}
	}
	if opts.RequireCreated && len(createdTasks) == 0 {
		return strings.TrimSpace(summary), createdTasks, fmt.Errorf("create_task: no tasks were persisted (see summary for details)")
	}
	if opts.AddCreatedSummary != nil {
		opts.AddCreatedSummary(summary)
	}
	return strings.TrimSpace(summary), createdTasks, nil
}

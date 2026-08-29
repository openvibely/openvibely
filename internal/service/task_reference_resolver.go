package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// TaskReferenceResolutionOptions controls the project-scope rule used while
// resolving an explicit task ID. Non-empty project IDs are always checked;
// AllowUnscopedProject preserves internal callers that may omit project scope.
type TaskReferenceResolutionOptions struct {
	AllowUnscopedProject bool
}

// ResolveTaskReference finds a task by ID or title while preserving the shared
// lookup order and user-facing errors used by task action runtimes.
func ResolveTaskReference(ctx context.Context, taskRepo *repository.TaskRepo, projectID, taskID, title string, opts TaskReferenceResolutionOptions) (*models.Task, error) {
	if taskRepo == nil {
		return nil, fmt.Errorf("task repository not configured")
	}
	if taskID = strings.TrimSpace(taskID); taskID != "" {
		task, err := taskRepo.GetByID(ctx, taskID)
		if err != nil {
			return nil, fmt.Errorf("error looking up task %s: %w", taskID, err)
		}
		if task == nil {
			return nil, fmt.Errorf("task %s not found", taskID)
		}
		if (projectID != "" || !opts.AllowUnscopedProject) && task.ProjectID != projectID {
			return nil, fmt.Errorf("task %s belongs to a different project", taskID)
		}
		return task, nil
	}
	if title = strings.TrimSpace(title); title != "" {
		tasks, err := taskRepo.SearchByTitle(ctx, projectID, title)
		if err != nil {
			return nil, fmt.Errorf("error searching for task %q: %w", title, err)
		}
		if len(tasks) == 0 {
			return nil, fmt.Errorf("no task found matching %q", title)
		}
		return &tasks[0], nil
	}
	return nil, fmt.Errorf("no task_id or title provided")
}

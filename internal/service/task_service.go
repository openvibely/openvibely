package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

var ErrDuplicateTask = errors.New("task with this name already exists in this project")

type TaskService struct {
	repo           *repository.TaskRepo
	attachmentRepo *repository.AttachmentRepo
	workerSvc      *WorkerService
}

func NewTaskService(repo *repository.TaskRepo, attachmentRepo *repository.AttachmentRepo, workerSvc *WorkerService) *TaskService {
	return &TaskService{
		repo:           repo,
		attachmentRepo: attachmentRepo,
		workerSvc:      workerSvc,
	}
}

func (s *TaskService) ListByProject(ctx context.Context, projectID, category string) ([]models.Task, error) {
	return s.ListByProjectWithSort(ctx, projectID, category, "")
}

func (s *TaskService) ListByProjectWithSort(ctx context.Context, projectID, category string, sortBy string) ([]models.Task, error) {
	return s.ListByProjectWithCategorySorts(ctx, projectID, category, sortBy, "")
}

func (s *TaskService) ListByProjectWithCategorySorts(ctx context.Context, projectID, category string, backlogSort string, completedSort string) ([]models.Task, error) {
	log.Printf("[task-svc] ListByProjectWithCategorySorts project=%s category=%q backlog_sort=%s completed_sort=%s",
		projectID, category, backlogSort, completedSort)
	tasks, err := s.repo.ListByProjectWithCategorySorts(ctx, projectID, category, backlogSort, completedSort)
	if err != nil {
		log.Printf("[task-svc] ListByProjectWithCategorySorts error: %v", err)
		return nil, err
	}
	log.Printf("[task-svc] ListByProjectWithCategorySorts returned %d tasks", len(tasks))
	return tasks, nil
}

func (s *TaskService) GetByID(ctx context.Context, id string) (*models.Task, error) {
	log.Printf("[task-svc] GetByID id=%s", id)
	return s.repo.GetByID(ctx, id)
}

func (s *TaskService) Create(ctx context.Context, t *models.Task) error {
	if t.Status == "" {
		t.Status = models.StatusPending
	}
	if t.Category == "" {
		t.Category = models.CategoryActive
	}
	log.Printf("[task-svc] Create title=%q category=%s status=%s project=%s",
		t.Title, t.Category, t.Status, t.ProjectID)

	if err := s.repo.Create(ctx, t); err != nil {
		if errors.Is(err, repository.ErrDuplicateTask) {
			log.Printf("[task-svc] Create duplicate task title=%q", t.Title)
			return ErrDuplicateTask
		}
		log.Printf("[task-svc] Create error: %v", err)
		return err
	}
	log.Printf("[task-svc] Create success id=%s", t.ID)

	// Auto-submit if created in Active category
	if t.Category == models.CategoryActive {
		log.Printf("[task-svc] Create auto-submitting active task id=%s to worker pool", t.ID)
		s.workerSvc.Submit(*t)
	}
	return nil
}

func (s *TaskService) Update(ctx context.Context, t *models.Task) error {
	log.Printf("[task-svc] Update id=%s title=%q category=%s", t.ID, t.Title, t.Category)
	if err := s.repo.Update(ctx, t); err != nil {
		if errors.Is(err, repository.ErrDuplicateTask) {
			log.Printf("[task-svc] Update duplicate task title=%q", t.Title)
			return ErrDuplicateTask
		}
		return err
	}
	return nil
}

func (s *TaskService) UpdateCategory(ctx context.Context, id string, category models.TaskCategory) error {
	log.Printf("[task-svc] UpdateCategory id=%s -> %s", id, category)
	if err := s.repo.UpdateCategory(ctx, id, category); err != nil {
		log.Printf("[task-svc] UpdateCategory error: %v", err)
		return err
	}
	task, err := s.repo.GetByID(ctx, id)
	if err != nil {
		log.Printf("[task-svc] UpdateCategory error fetching task: %v", err)
		return err
	}
	if task == nil {
		return nil
	}

	// If moved AWAY from Active while running, cancel the running execution
	// to release the project concurrency slot.
	if category != models.CategoryActive && task.Status == models.StatusRunning {
		log.Printf("[task-svc] UpdateCategory cancelling running task id=%s (moved to %s)", id, category)
		if s.workerSvc != nil {
			s.workerSvc.CancelRunningTask(id)
		}
		s.repo.UpdateStatus(ctx, id, models.StatusCancelled)
		// Move cancelled tasks to backlog (same behavior as CancelTask)
		if err := s.repo.UpdateCategory(ctx, id, models.CategoryBacklog); err != nil {
			log.Printf("[task-svc] UpdateCategory error moving cancelled task to backlog: %v", err)
		} else {
			log.Printf("[task-svc] UpdateCategory moved cancelled task to backlog id=%s", id)
		}
	}

	// If moved to Active, always reset status to pending and auto-submit.
	// ClaimTask provides atomic guard against double execution.
	if category == models.CategoryActive {
		log.Printf("[task-svc] UpdateCategory resetting status to pending and auto-submitting id=%s (was %s)", id, task.Status)
		s.repo.UpdateStatus(ctx, id, models.StatusPending)
		task.Status = models.StatusPending
		s.workerSvc.Submit(*task)
	}
	return nil
}

func (s *TaskService) UpdateStatus(ctx context.Context, id string, status models.TaskStatus) error {
	log.Printf("[task-svc] UpdateStatus id=%s -> %s", id, status)
	if err := s.repo.UpdateStatus(ctx, id, status); err != nil {
		log.Printf("[task-svc] UpdateStatus error: %v", err)
		return err
	}
	// If status is pending and task is in Active category, auto-submit for execution
	if status == models.StatusPending {
		task, err := s.repo.GetByID(ctx, id)
		if err != nil {
			log.Printf("[task-svc] UpdateStatus error fetching task: %v", err)
			return err
		}
		if task != nil && task.Category == models.CategoryActive {
			log.Printf("[task-svc] UpdateStatus auto-submitting active task id=%s", id)
			s.workerSvc.Submit(*task)
		}
	}
	return nil
}

func (s *TaskService) Delete(ctx context.Context, id string) error {
	log.Printf("[task-svc] Delete id=%s", id)

	// Get all attachments for this task
	attachments, err := s.attachmentRepo.ListByTask(ctx, id)
	if err != nil {
		log.Printf("[task-svc] Delete error listing attachments: %v", err)
		return fmt.Errorf("listing attachments for deletion: %w", err)
	}

	// Delete physical files for all attachments
	for _, att := range attachments {
		if err := os.Remove(att.FilePath); err != nil && !os.IsNotExist(err) {
			log.Printf("[task-svc] Delete warning: failed to delete file %s: %v", att.FilePath, err)
			// Continue even if file deletion fails - the file might already be gone
		} else {
			log.Printf("[task-svc] Delete removed file %s", att.FilePath)
		}
	}

	// Delete attachment records from database
	if err := s.attachmentRepo.DeleteByTask(ctx, id); err != nil {
		log.Printf("[task-svc] Delete error deleting attachments: %v", err)
		return fmt.Errorf("deleting attachments: %w", err)
	}

	// Delete the task itself
	if err := s.repo.Delete(ctx, id); err != nil {
		log.Printf("[task-svc] Delete error deleting task: %v", err)
		return err
	}

	log.Printf("[task-svc] Delete success id=%s (deleted %d attachments)", id, len(attachments))
	return nil
}

func (s *TaskService) RunTask(ctx context.Context, id string) error {
	log.Printf("[task-svc] RunTask id=%s", id)
	task, err := s.repo.GetByID(ctx, id)
	if err != nil {
		log.Printf("[task-svc] RunTask error fetching: %v", err)
		return err
	}
	if task == nil {
		log.Printf("[task-svc] RunTask not found id=%s", id)
		return fmt.Errorf("task not found: %s", id)
	}

	// Move to active category if not already active (e.g., task is in backlog).
	// This must happen before submission so the UI reflects the move immediately.
	if task.Category != models.CategoryActive {
		if err := s.repo.UpdateCategory(ctx, id, models.CategoryActive); err != nil {
			log.Printf("[task-svc] RunTask error updating category: %v", err)
			return fmt.Errorf("update category: %w", err)
		}
		task.Category = models.CategoryActive
	}

	updated, err := s.repo.SetPendingIfNotRunningOrQueued(ctx, id)
	if err != nil {
		log.Printf("[task-svc] RunTask guarded pending update error: %v", err)
		return fmt.Errorf("set pending with guard: %w", err)
	}
	if !updated {
		log.Printf("[task-svc] RunTask no-op id=%s current_status=%s (already running/queued)", id, task.Status)
		return nil
	}

	log.Printf("[task-svc] RunTask submitting id=%s title=%q", id, task.Title)
	task.Status = models.StatusPending
	s.workerSvc.Submit(*task)
	return nil
}

func (s *TaskService) CancelTask(ctx context.Context, id string) error {
	log.Printf("[task-svc] CancelTask id=%s", id)
	task, err := s.repo.GetByID(ctx, id)
	if err != nil {
		log.Printf("[task-svc] CancelTask error fetching: %v", err)
		return err
	}
	if task == nil {
		log.Printf("[task-svc] CancelTask not found id=%s", id)
		return fmt.Errorf("task not found: %s", id)
	}
	if task.Status != models.StatusRunning {
		log.Printf("[task-svc] CancelTask task not running id=%s status=%s", id, task.Status)
		return fmt.Errorf("task is not running")
	}

	// Kill the running CLI process by cancelling its context.
	// This must happen BEFORE updating the DB status so the worker sees
	// context.Canceled and marks the execution as cancelled (not failed).
	if s.workerSvc != nil {
		s.workerSvc.CancelRunningTask(id)
	}

	log.Printf("[task-svc] CancelTask setting status=cancelled, category=backlog id=%s title=%q", id, task.Title)

	// Move cancelled tasks to backlog so they remain visible in the kanban board
	// and can be re-run later. Status stays "cancelled" to reflect what happened.
	if err := s.repo.UpdateStatus(ctx, id, models.StatusCancelled); err != nil {
		log.Printf("[task-svc] CancelTask error updating status: %v", err)
		return err
	}
	if err := s.repo.UpdateCategory(ctx, id, models.CategoryBacklog); err != nil {
		log.Printf("[task-svc] CancelTask error moving to backlog: %v", err)
		// Non-fatal - the task is still cancelled
	} else {
		log.Printf("[task-svc] CancelTask moved to backlog id=%s", id)
	}

	log.Printf("[task-svc] CancelTask success id=%s", id)
	return nil
}

func (s *TaskService) CountByProjectAndCategory(ctx context.Context, projectID string) (map[string]int, error) {
	return s.repo.CountByProjectAndCategory(ctx, projectID)
}

func (s *TaskService) MoveCompletedActiveToCompleted(ctx context.Context) (int, error) {
	log.Printf("[task-svc] MoveCompletedActiveToCompleted called")
	count, err := s.repo.MoveCompletedActiveToCompleted(ctx)
	if err != nil {
		log.Printf("[task-svc] MoveCompletedActiveToCompleted error: %v", err)
		return 0, err
	}
	log.Printf("[task-svc] MoveCompletedActiveToCompleted moved %d tasks", count)
	return count, nil
}

func (s *TaskService) DeleteAllCompleted(ctx context.Context, projectID string) (int, error) {
	log.Printf("[task-svc] DeleteAllCompleted called for project=%s", projectID)

	// Get all completed tasks for this project to delete their attachments
	tasks, err := s.repo.ListByProject(ctx, projectID, string(models.CategoryCompleted))
	if err != nil {
		log.Printf("[task-svc] DeleteAllCompleted error listing completed tasks: %v", err)
		return 0, fmt.Errorf("listing completed tasks: %w", err)
	}

	// Delete attachments for each completed task
	totalAttachments := 0
	for _, task := range tasks {
		attachments, err := s.attachmentRepo.ListByTask(ctx, task.ID)
		if err != nil {
			log.Printf("[task-svc] DeleteAllCompleted error listing attachments for task %s: %v", task.ID, err)
			continue
		}

		// Delete physical files
		for _, att := range attachments {
			if err := os.Remove(att.FilePath); err != nil && !os.IsNotExist(err) {
				log.Printf("[task-svc] DeleteAllCompleted warning: failed to delete file %s: %v", att.FilePath, err)
			}
		}

		// Delete attachment records
		if err := s.attachmentRepo.DeleteByTask(ctx, task.ID); err != nil {
			log.Printf("[task-svc] DeleteAllCompleted error deleting attachments for task %s: %v", task.ID, err)
		} else {
			totalAttachments += len(attachments)
		}
	}

	// Delete the tasks themselves
	count, err := s.repo.DeleteAllCompleted(ctx, projectID)
	if err != nil {
		log.Printf("[task-svc] DeleteAllCompleted error: %v", err)
		return 0, err
	}
	log.Printf("[task-svc] DeleteAllCompleted deleted %d tasks and %d attachments for project %s", count, totalAttachments, projectID)
	return count, nil
}

func (s *TaskService) DeleteAllBacklog(ctx context.Context, projectID string) (int, error) {
	log.Printf("[task-svc] DeleteAllBacklog called for project=%s", projectID)

	// Get all backlog tasks for this project to delete their attachments
	tasks, err := s.repo.ListByProject(ctx, projectID, string(models.CategoryBacklog))
	if err != nil {
		log.Printf("[task-svc] DeleteAllBacklog error listing backlog tasks: %v", err)
		return 0, fmt.Errorf("listing backlog tasks: %w", err)
	}

	// Delete attachments for each backlog task
	totalAttachments := 0
	for _, task := range tasks {
		attachments, err := s.attachmentRepo.ListByTask(ctx, task.ID)
		if err != nil {
			log.Printf("[task-svc] DeleteAllBacklog error listing attachments for task %s: %v", task.ID, err)
			continue
		}

		// Delete physical files
		for _, att := range attachments {
			if err := os.Remove(att.FilePath); err != nil && !os.IsNotExist(err) {
				log.Printf("[task-svc] DeleteAllBacklog warning: failed to delete file %s: %v", att.FilePath, err)
			}
		}

		// Delete attachment records
		if err := s.attachmentRepo.DeleteByTask(ctx, task.ID); err != nil {
			log.Printf("[task-svc] DeleteAllBacklog error deleting attachments for task %s: %v", task.ID, err)
		} else {
			totalAttachments += len(attachments)
		}
	}

	// Delete the tasks themselves
	count, err := s.repo.DeleteAllBacklog(ctx, projectID)
	if err != nil {
		log.Printf("[task-svc] DeleteAllBacklog error: %v", err)
		return 0, err
	}
	log.Printf("[task-svc] DeleteAllBacklog deleted %d tasks and %d attachments for project %s", count, totalAttachments, projectID)
	return count, nil
}

func (s *TaskService) DeleteAllChat(ctx context.Context, projectID string) (int, error) {
	log.Printf("[task-svc] DeleteAllChat called for project=%s", projectID)
	count, err := s.repo.DeleteAllChat(ctx, projectID)
	if err != nil {
		log.Printf("[task-svc] DeleteAllChat error: %v", err)
		return 0, err
	}
	log.Printf("[task-svc] DeleteAllChat deleted %d tasks for project %s", count, projectID)
	return count, nil
}

func (s *TaskService) ActivateAllBacklog(ctx context.Context, projectID string) (int, error) {
	log.Printf("[task-svc] ActivateAllBacklog called for project=%s", projectID)

	// Update all backlog tasks to active category with pending status
	count, err := s.repo.ActivateAllBacklog(ctx, projectID)
	if err != nil {
		log.Printf("[task-svc] ActivateAllBacklog error: %v", err)
		return 0, err
	}

	// Submit the activated tasks to the worker pool
	if count > 0 {
		activeTasks, err := s.repo.ListByProject(ctx, projectID, string(models.CategoryActive))
		if err != nil {
			log.Printf("[task-svc] ActivateAllBacklog error listing active tasks: %v", err)
			// Don't fail the operation if we can't list tasks for submission
		} else {
			for _, task := range activeTasks {
				if task.Status == models.StatusPending {
					s.workerSvc.Submit(task)
				}
			}
		}
	}

	log.Printf("[task-svc] ActivateAllBacklog activated %d tasks for project %s", count, projectID)
	return count, nil
}

func (s *TaskService) GetTasksWithSchedulesByProject(ctx context.Context, projectID string) ([]repository.TaskWithSchedule, error) {
	log.Printf("[task-svc] GetTasksWithSchedulesByProject project=%s", projectID)
	tasks, err := s.repo.ListWithSchedulesByProject(ctx, projectID)
	if err != nil {
		log.Printf("[task-svc] GetTasksWithSchedulesByProject error: %v", err)
		return nil, err
	}
	log.Printf("[task-svc] GetTasksWithSchedulesByProject returned %d tasks", len(tasks))
	return tasks, nil
}

func (s *TaskService) ReorderTask(ctx context.Context, taskID string, newPosition int) error {
	log.Printf("[task-svc] ReorderTask id=%s position=%d", taskID, newPosition)
	if err := s.repo.ReorderTask(ctx, taskID, newPosition); err != nil {
		log.Printf("[task-svc] ReorderTask error: %v", err)
		return err
	}
	log.Printf("[task-svc] ReorderTask success id=%s", taskID)
	return nil
}

// ExecuteBacklogTasks activates and executes backlog tasks, optionally filtered by priority.
// Tasks are moved to active category and submitted to the worker pool.
// If priority is 0, all eligible backlog tasks are executed.
// Returns the list of tasks submitted and the count.
func (s *TaskService) ExecuteBacklogTasks(ctx context.Context, projectID string, priority int) ([]models.Task, int, error) {
	log.Printf("[task-svc] ExecuteBacklogTasks project=%s priority=%d", projectID, priority)

	tasks, err := s.repo.ListBacklogByPriority(ctx, projectID, priority)
	if err != nil {
		log.Printf("[task-svc] ExecuteBacklogTasks error listing tasks: %v", err)
		return nil, 0, fmt.Errorf("listing backlog tasks: %w", err)
	}

	if len(tasks) == 0 {
		log.Printf("[task-svc] ExecuteBacklogTasks no eligible tasks found")
		return []models.Task{}, 0, nil
	}

	log.Printf("[task-svc] ExecuteBacklogTasks found %d eligible tasks", len(tasks))

	submitted := 0
	for _, task := range tasks {
		// Move to active category
		if err := s.repo.UpdateCategory(ctx, task.ID, models.CategoryActive); err != nil {
			log.Printf("[task-svc] ExecuteBacklogTasks error updating category for task %s: %v", task.ID, err)
			continue
		}
		task.Category = models.CategoryActive

		// Reset status to pending if needed
		if task.Status != models.StatusPending {
			if err := s.repo.UpdateStatus(ctx, task.ID, models.StatusPending); err != nil {
				log.Printf("[task-svc] ExecuteBacklogTasks error updating status for task %s: %v", task.ID, err)
				continue
			}
			task.Status = models.StatusPending
		}

		// Submit to worker pool
		s.workerSvc.Submit(task)
		submitted++
	}

	log.Printf("[task-svc] ExecuteBacklogTasks submitted %d tasks for execution", submitted)
	return tasks, submitted, nil
}

// CountBacklogByPriority returns priority -> count for eligible backlog tasks.
func (s *TaskService) CountBacklogByPriority(ctx context.Context, projectID string) (map[int]int, error) {
	return s.repo.CountBacklogByPriority(ctx, projectID)
}

// ExecuteTasksByTags activates and executes tasks matching the specified tags and/or priority filters.
// Tags may be empty to match all tasks (priority-only filtering).
// Completed tasks are excluded by default unless includeCompleted is true.
// Returns the list of tasks that were activated and the count of tasks submitted.
func (s *TaskService) ExecuteTasksByTags(ctx context.Context, tags []models.TaskTag, projectID string, minPriority int, includeCompleted bool) ([]models.Task, int, error) {
	log.Printf("[task-svc] ExecuteTasksByTags tags=%v project=%s minPriority=%d includeCompleted=%v", tags, projectID, minPriority, includeCompleted)

	// Find matching tasks in backlog/active by default.
	// Completed status/category is opt-in only to avoid accidental mass re-runs.
	var allTasks []models.Task
	categoriesToSearch := []models.TaskCategory{models.CategoryBacklog, models.CategoryActive}
	statusesToSearch := []models.TaskStatus{models.StatusPending, models.StatusFailed, models.StatusCancelled}
	if includeCompleted {
		categoriesToSearch = append(categoriesToSearch, models.CategoryCompleted)
		statusesToSearch = append(statusesToSearch, models.StatusCompleted)
	}
	for _, status := range statusesToSearch {
		for _, category := range categoriesToSearch {
			tasks, err := s.repo.ListByTags(ctx, tags, projectID, category, minPriority, status)
			if err != nil {
				log.Printf("[task-svc] ExecuteTasksByTags error listing %s tasks: %v", category, err)
				return nil, 0, fmt.Errorf("listing %s tasks: %w", category, err)
			}
			allTasks = append(allTasks, tasks...)
		}
	}

	if len(allTasks) == 0 {
		log.Printf("[task-svc] ExecuteTasksByTags no matching tasks found")
		return []models.Task{}, 0, nil
	}

	log.Printf("[task-svc] ExecuteTasksByTags found %d matching tasks", len(allTasks))

	// Move backlog tasks to active and reset status to pending
	submitted := 0
	for _, task := range allTasks {
		needsStatusUpdate := task.Status != models.StatusPending
		needsCategoryUpdate := task.Category != models.CategoryActive

		// Update category if needed
		if needsCategoryUpdate {
			if err := s.repo.UpdateCategory(ctx, task.ID, models.CategoryActive); err != nil {
				log.Printf("[task-svc] ExecuteTasksByTags error updating category for task %s: %v", task.ID, err)
				continue
			}
			task.Category = models.CategoryActive
			log.Printf("[task-svc] ExecuteTasksByTags moved task %s to active", task.ID)
		}

		// Update status if needed (reset failed/cancelled tasks to pending)
		if needsStatusUpdate {
			if err := s.repo.UpdateStatus(ctx, task.ID, models.StatusPending); err != nil {
				log.Printf("[task-svc] ExecuteTasksByTags error updating status for task %s: %v", task.ID, err)
				continue
			}
			task.Status = models.StatusPending
			log.Printf("[task-svc] ExecuteTasksByTags reset status to pending for task %s", task.ID)
		}

		// Submit to worker pool
		s.workerSvc.Submit(task)
		submitted++
	}

	log.Printf("[task-svc] ExecuteTasksByTags submitted %d tasks for execution", submitted)
	return allTasks, submitted, nil
}

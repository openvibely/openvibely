package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/attachmentsession"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

var ErrDuplicateTask = errors.New("task with this name already exists in this project")
var ErrTaskTitleRequired = errors.New("task title is required")
var ErrTaskPromptRequired = errors.New("task prompt is required")
var ErrInvalidTaskPriority = errors.New("task priority must be between 1 and 4")

type TaskService struct {
	repo                              *repository.TaskRepo
	uploadsDir                        string
	workerSvc                         *WorkerService
	agentRepo                         *repository.AgentRepo
	goalSvc                           *TaskGoalService
	swarmSvc                          *SwarmService
	queuedTaskThreadFollowupHook      func(context.Context, string) (bool, error)
	failedTaskThreadFollowupRetryHook func(context.Context, string) (bool, error)
	updateCategoryTaskLoader          func(context.Context, string) (*models.Task, error)
	beforePendingSessionRemoval       func(string)
}

func NewTaskService(repo *repository.TaskRepo, _ *repository.AttachmentRepo, workerSvc *WorkerService) *TaskService {
	return &TaskService{
		repo:      repo,
		workerSvc: workerSvc,
	}
}

func (s *TaskService) SetDeletionUploadsDir(uploadsDir string) {
	s.uploadsDir = uploadsDir
}

func (s *TaskService) SetAgentRepo(agentRepo *repository.AgentRepo) {
	s.agentRepo = agentRepo
}

func (s *TaskService) SetTaskGoalService(goalSvc *TaskGoalService) {
	s.goalSvc = goalSvc
}

func (s *TaskService) SetSwarmService(swarmSvc *SwarmService) {
	s.swarmSvc = swarmSvc
}

func (s *TaskService) resumeGoalStoppedByUser(ctx context.Context, taskID string, actor string) {
	if s.goalSvc == nil || taskID == "" {
		return
	}
	goal, err := s.goalSvc.ResumeGoalStoppedByUser(ctx, taskID, actor)
	if err != nil {
		applog.Infof("[task-svc] task=%s error resuming user-stopped goal for start: %v", taskID, err)
		return
	}
	if goal != nil {
		applog.Infof("[task-svc] task=%s goal=%s resumed user-stopped goal for start", taskID, goal.GoalID)
	}
}

func (s *TaskService) submitActivatedTask(ctx context.Context, task models.Task, actor string) error {
	if s.workerSvc != nil {
		s.workerSvc.ClearCancellationRequested(task.ID)
	}
	s.resumeGoalStoppedByUser(ctx, task.ID, actor)
	if task.SwarmRole == models.SwarmRoleParent {
		if s.swarmSvc == nil {
			return errors.New("swarm service unavailable")
		}
		applog.Infof("[task-svc] activating swarm parent id=%s via planner start", task.ID)
		return s.swarmSvc.StartPlanner(ctx, task.ID)
	}
	if s.workerSvc == nil {
		return errors.New("worker service unavailable")
	}
	s.workerSvc.Submit(task)
	return nil
}

func (s *TaskService) SetQueuedTaskThreadFollowupHook(hook func(context.Context, string) (bool, error)) {
	s.queuedTaskThreadFollowupHook = hook
}

func (s *TaskService) SetFailedTaskThreadFollowupRetryHook(hook func(context.Context, string) (bool, error)) {
	s.failedTaskThreadFollowupRetryHook = hook
}

func (s *TaskService) ListByProject(ctx context.Context, projectID, category string) ([]models.Task, error) {
	return s.ListByProjectWithSort(ctx, projectID, category, "")
}

func (s *TaskService) ListByProjectWithSort(ctx context.Context, projectID, category string, sortBy string) ([]models.Task, error) {
	return s.ListByProjectWithCategorySorts(ctx, projectID, category, sortBy, "")
}

func (s *TaskService) ListByProjectWithCategorySorts(ctx context.Context, projectID, category string, backlogSort string, completedSort string) ([]models.Task, error) {
	applog.Infof("[task-svc] ListByProjectWithCategorySorts project=%s category=%q backlog_sort=%s completed_sort=%s",
		projectID, category, backlogSort, completedSort)
	tasks, err := s.repo.ListByProjectWithCategorySorts(ctx, projectID, category, backlogSort, completedSort)
	if err != nil {
		applog.Infof("[task-svc] ListByProjectWithCategorySorts error: %v", err)
		return nil, err
	}
	if moved, moveErr := s.normalizeActiveTerminalTasks(ctx, tasks); moveErr != nil {
		applog.Infof("[task-svc] ListByProjectWithCategorySorts error normalizing active terminal tasks: %v", moveErr)
		return nil, moveErr
	} else if moved > 0 {
		tasks, err = s.repo.ListByProjectWithCategorySorts(ctx, projectID, category, backlogSort, completedSort)
		if err != nil {
			applog.Infof("[task-svc] ListByProjectWithCategorySorts error reloading after normalization: %v", err)
			return nil, err
		}
	}
	applog.Infof("[task-svc] ListByProjectWithCategorySorts returned %d tasks", len(tasks))
	return tasks, nil
}

func (s *TaskService) ListBoardByProjectWithCategorySorts(ctx context.Context, projectID, category string, backlogSort string, completedSort string) ([]models.Task, error) {
	applog.Infof("[task-svc] ListBoardByProjectWithCategorySorts project=%s category=%q backlog_sort=%s completed_sort=%s",
		projectID, category, backlogSort, completedSort)
	tasks, err := s.repo.ListBoardByProjectWithCategorySorts(ctx, projectID, category, backlogSort, completedSort)
	if err != nil {
		applog.Infof("[task-svc] ListBoardByProjectWithCategorySorts error: %v", err)
		return nil, err
	}
	if moved, moveErr := s.normalizeActiveTerminalTasks(ctx, tasks); moveErr != nil {
		applog.Infof("[task-svc] ListBoardByProjectWithCategorySorts error normalizing active terminal tasks: %v", moveErr)
		return nil, moveErr
	} else if moved > 0 {
		tasks, err = s.repo.ListBoardByProjectWithCategorySorts(ctx, projectID, category, backlogSort, completedSort)
		if err != nil {
			applog.Infof("[task-svc] ListBoardByProjectWithCategorySorts error reloading after normalization: %v", err)
			return nil, err
		}
	}
	applog.Infof("[task-svc] ListBoardByProjectWithCategorySorts returned %d tasks", len(tasks))
	return tasks, nil
}

func (s *TaskService) normalizeActiveTerminalTasks(ctx context.Context, tasks []models.Task) (int, error) {
	moved := 0
	for _, task := range tasks {
		if task.Category != models.CategoryActive {
			continue
		}
		if task.Status != models.StatusFailed && task.Status != models.StatusCancelled {
			continue
		}
		if err := s.repo.UpdateCategory(ctx, task.ID, models.CategoryBacklog); err != nil {
			return moved, fmt.Errorf("moving terminal active task %s to backlog: %w", task.ID, err)
		}
		moved++
	}
	return moved, nil
}

func (s *TaskService) GetByID(ctx context.Context, id string) (*models.Task, error) {
	applog.Infof("[task-svc] GetByID id=%s", id)
	return s.repo.GetByID(ctx, id)
}

func (s *TaskService) GetThreadRenderMetadata(ctx context.Context, id string) (*models.Task, error) {
	return s.repo.GetThreadRenderMetadata(ctx, id)
}

func (s *TaskService) Create(ctx context.Context, t *models.Task) error {
	return s.CreateWithGoal(ctx, t, "")
}

// SubmitSavedAutomationTask admits a runnable root only after its saved graph
// and Automation provenance are durable.
func (s *TaskService) SubmitSavedAutomationTask(task models.Task) {
	if s == nil || s.workerSvc == nil || task.Category != models.CategoryActive || task.Status == models.StatusBlocked || task.ParentTaskID != nil {
		return
	}
	s.workerSvc.Submit(task)
}

const defaultTaskPriority = 2

func normalizeTaskTitleAndPrompt(t *models.Task) error {
	t.Title = strings.TrimSpace(t.Title)
	t.Prompt = strings.TrimSpace(t.Prompt)
	if t.Title == "" {
		return ErrTaskTitleRequired
	}
	if t.Prompt == "" {
		return ErrTaskPromptRequired
	}
	return nil
}

func validateTaskPriority(priority int) error {
	if priority < 1 || priority > 4 {
		return ErrInvalidTaskPriority
	}
	return nil
}

func normalizeTaskCreatePriority(t *models.Task) {
	if err := validateTaskPriority(t.Priority); err != nil {
		t.Priority = defaultTaskPriority
	}
}

func (s *TaskService) CreateWithGoal(ctx context.Context, t *models.Task, objective string) error {
	if err := normalizeTaskTitleAndPrompt(t); err != nil {
		return err
	}
	normalizeTaskCreatePriority(t)
	if t.Status == "" {
		t.Status = models.StatusPending
	}
	if t.Category == "" {
		t.Category = models.CategoryActive
	}
	objective = strings.TrimSpace(objective)
	if objective != "" && len(objective) > MaxTaskGoalLength {
		return ErrTaskGoalTooLong
	}
	applog.Infof("[task-svc] Create title=%q category=%s status=%s project=%s goal=%v",
		t.Title, t.Category, t.Status, t.ProjectID, objective != "")

	var goal *models.TaskGoal
	if objective != "" {
		goal = &models.TaskGoal{
			GoalID:    repository.NewID(),
			Objective: objective,
			Status:    models.TaskGoalStatusActive,
			Reason:    "set at task creation",
		}
	}
	if err := s.repo.CreateWithGoal(ctx, t, goal); err != nil {
		if errors.Is(err, repository.ErrDuplicateTask) {
			applog.Infof("[task-svc] Create duplicate task title=%q", t.Title)
			return ErrDuplicateTask
		}
		applog.Infof("[task-svc] Create error: %v", err)
		return err
	}
	applog.Infof("[task-svc] Create success id=%s", t.ID)
	if goal != nil && s.goalSvc != nil {
		s.goalSvc.publishGoalEvent(events.TaskGoalUpdated, goal)
	}

	// Auto-submit if created in Active category (blocked tasks wait for parent to activate them)
	if t.Category == models.CategoryActive && t.Status != models.StatusBlocked && s.workerSvc != nil {
		applog.Infof("[task-svc] Create auto-submitting active task id=%s to worker pool", t.ID)
		s.workerSvc.Submit(*t)
	}
	return nil
}

func (s *TaskService) Update(ctx context.Context, t *models.Task) error {
	if err := normalizeTaskTitleAndPrompt(t); err != nil {
		return err
	}
	if err := validateTaskPriority(t.Priority); err != nil {
		return err
	}
	applog.Infof("[task-svc] Update id=%s title=%q category=%s", t.ID, t.Title, t.Category)
	if err := s.repo.Update(ctx, t); err != nil {
		if errors.Is(err, repository.ErrDuplicateTask) {
			applog.Infof("[task-svc] Update duplicate task title=%q", t.Title)
			return ErrDuplicateTask
		}
		return err
	}
	return nil
}

func (s *TaskService) UpdateCategory(ctx context.Context, id string, category models.TaskCategory) error {
	applog.Infof("[task-svc] UpdateCategory id=%s -> %s", id, category)
	var previousTask *models.Task
	if category == models.CategoryActive {
		var err error
		previousTask, err = s.repo.GetByID(ctx, id)
		if err != nil {
			applog.Infof("[task-svc] UpdateCategory error fetching previous task state: %v", err)
			return err
		}
	}
	rollbackActivation := func(activationErr error) error {
		if previousTask == nil {
			return activationErr
		}
		rollbackCtx := context.WithoutCancel(ctx)
		if err := s.repo.UpdateCategory(rollbackCtx, id, previousTask.Category); err != nil {
			return errors.Join(activationErr, fmt.Errorf("rolling back task category to %s: %w", previousTask.Category, err))
		}
		if err := s.repo.UpdateStatus(rollbackCtx, id, previousTask.Status); err != nil {
			return errors.Join(activationErr, fmt.Errorf("rolling back task status to %s: %w", previousTask.Status, err))
		}
		applog.Infof("[task-svc] UpdateCategory rolled back failed activation id=%s category=%s status=%s", id, previousTask.Category, previousTask.Status)
		return activationErr
	}
	if err := s.repo.UpdateCategory(ctx, id, category); err != nil {
		applog.Infof("[task-svc] UpdateCategory error: %v", err)
		return err
	}
	taskLoader := s.repo.GetByID
	if s.updateCategoryTaskLoader != nil {
		taskLoader = s.updateCategoryTaskLoader
	}
	task, err := taskLoader(ctx, id)
	if err != nil {
		applog.Infof("[task-svc] UpdateCategory error fetching task: %v", err)
		return rollbackActivation(err)
	}
	if task == nil {
		return nil
	}
	if category == models.CategoryActive && previousTask != nil && previousTask.Category == models.CategoryActive &&
		(previousTask.Status == models.StatusPending || previousTask.Status == models.StatusQueued || previousTask.Status == models.StatusRunning) {
		applog.Infof("[task-svc] UpdateCategory active no-op id=%s status=%s", id, previousTask.Status)
		return nil
	}

	// If moved AWAY from Active while running or queued, cancel the execution
	// to release the project concurrency slot or abort the queued wait.
	// Pending swarm children are also submitted runnable work; moving them out
	// of Active must notify swarm orchestration instead of letting the worker
	// queue silently prune them.
	isCancellableActiveWork := task.Status == models.StatusRunning || task.Status == models.StatusQueued || (task.Status == models.StatusPending && models.IsSwarmChildRole(task.SwarmRole))
	if category != models.CategoryActive && isCancellableActiveWork {
		applog.Infof("[task-svc] UpdateCategory cancelling active task id=%s status=%s (moved to %s)", id, task.Status, category)
		if s.workerSvc != nil {
			s.workerSvc.MarkCancellationRequested(id)
		}
		if s.goalSvc != nil {
			if err := s.goalSvc.PauseActiveGoalStoppedByUser(ctx, id); err != nil && !errors.Is(err, ErrTaskGoalNotFound) {
				applog.Infof("[task-svc] UpdateCategory error pausing active goal after user stop id=%s: %v", id, err)
			}
		}
		if s.workerSvc != nil {
			s.workerSvc.CancelRunningTask(id)
		}
		if err := s.repo.UpdateStatus(ctx, id, models.StatusCancelled); err != nil {
			applog.Infof("[task-svc] UpdateCategory error marking active task cancelled id=%s: %v", id, err)
			return err
		}
		if s.swarmSvc != nil && models.IsSwarmChildRole(task.SwarmRole) {
			if err := s.swarmSvc.OnChildCompleted(ctx, id); err != nil {
				applog.Infof("[task-svc] UpdateCategory error notifying swarm child cancellation id=%s: %v", id, err)
				return err
			}
		}
		applog.Infof("[task-svc] UpdateCategory cancelled active task id=%s and kept requested category=%s", id, category)
	}

	// If moved to Active, prefer a pending task-thread follow-up over rerunning the original prompt.
	// ClaimTask provides atomic guard against double execution for normal task runs.
	if category == models.CategoryActive {
		s.resumeGoalStoppedByUser(ctx, id, "user")
		if s.queuedTaskThreadFollowupHook != nil {
			handled, err := s.queuedTaskThreadFollowupHook(ctx, id)
			if err != nil {
				applog.Infof("[task-svc] UpdateCategory queued task-thread follow-up promotion failed id=%s: %v", id, err)
				return rollbackActivation(err)
			}
			if handled {
				if s.workerSvc != nil {
					s.workerSvc.ClearCancellationRequested(id)
				}
				applog.Infof("[task-svc] UpdateCategory promoted queued task-thread follow-up id=%s", id)
				return nil
			}
		}
		if s.failedTaskThreadFollowupRetryHook != nil {
			handled, err := s.failedTaskThreadFollowupRetryHook(ctx, id)
			if err != nil {
				applog.Infof("[task-svc] UpdateCategory failed task-thread follow-up retry failed id=%s: %v", id, err)
				return rollbackActivation(err)
			}
			if handled {
				if s.workerSvc != nil {
					s.workerSvc.ClearCancellationRequested(id)
				}
				applog.Infof("[task-svc] UpdateCategory retried failed task-thread follow-up id=%s", id)
				return nil
			}
		}
		applog.Infof("[task-svc] UpdateCategory resetting status to pending and activating id=%s (was %s)", id, task.Status)
		if err := s.repo.UpdateStatus(ctx, id, models.StatusPending); err != nil {
			return rollbackActivation(err)
		}
		task.Status = models.StatusPending
		if err := s.submitActivatedTask(ctx, *task, "user"); err != nil {
			return rollbackActivation(err)
		}
		return nil
	}
	return nil
}

func (s *TaskService) UpdateStatus(ctx context.Context, id string, status models.TaskStatus) error {
	applog.Infof("[task-svc] UpdateStatus id=%s -> %s", id, status)
	if status == models.StatusRunning {
		applog.Infof("[task-svc] UpdateStatus routing running request through worker admission id=%s", id)
		return s.RunTask(ctx, id)
	}
	if err := s.repo.UpdateStatus(ctx, id, status); err != nil {
		applog.Infof("[task-svc] UpdateStatus error: %v", err)
		return err
	}
	if status == models.StatusPending {
		task, err := s.repo.GetByID(ctx, id)
		if err != nil {
			applog.Infof("[task-svc] UpdateStatus error fetching task: %v", err)
			return err
		}
		if task != nil && (task.Category == models.CategoryActive || task.Category == models.CategoryScheduled) && s.workerSvc != nil {
			s.workerSvc.ClearCancellationRequested(id)
		}
		if task != nil && task.Category == models.CategoryActive {
			if status == models.StatusPending {
				applog.Infof("[task-svc] UpdateStatus activating pending active task id=%s", id)
				return s.submitActivatedTask(ctx, *task, "user")
			}
			s.resumeGoalStoppedByUser(ctx, id, "user")
		}
	}
	return nil
}

func (s *TaskService) Delete(ctx context.Context, id string) error {
	applog.Infof("[task-svc] Delete id=%s", id)
	_, err := s.deleteTask(ctx, id, "", "")
	return err
}

func (s *TaskService) DeleteProjectTasks(ctx context.Context, projectID string) error {
	for {
		tasks, err := s.repo.ListByProject(ctx, projectID, "")
		if err != nil {
			return fmt.Errorf("listing project tasks for deletion: %w", err)
		}
		if len(tasks) == 0 {
			return nil
		}
		deletedAny := false
		for _, task := range tasks {
			deleted, err := s.deleteTask(ctx, task.ID, projectID, task.Category)
			if err != nil {
				return err
			}
			deletedAny = deletedAny || deleted
		}
		if !deletedAny {
			return fmt.Errorf("project tasks changed during deletion")
		}
	}
}

func (s *TaskService) deleteTask(ctx context.Context, id, projectID string, category models.TaskCategory) (bool, error) {
	prepareDelete := func(manifest repository.TaskDeletionManifest) error {
		if len(manifest.PendingUploadSessionIDs) > 0 && strings.TrimSpace(s.uploadsDir) == "" {
			return errors.New("uploads directory is not configured for pending upload cleanup")
		}
		if s.workerSvc != nil {
			s.workerSvc.CancelRunningTask(id)
			for _, childID := range manifest.SwarmChildTaskIDs {
				s.workerSvc.CancelRunningTask(childID)
			}
		}
		return nil
	}
	var (
		manifest repository.TaskDeletionManifest
		deleted  bool
		err      error
	)
	if projectID == "" && category == "" {
		manifest, deleted, err = s.repo.DeleteWithCleanupManifest(ctx, id, prepareDelete)
	} else {
		manifest, deleted, err = s.repo.DeleteWithCleanupManifestIfCategory(ctx, id, projectID, category, prepareDelete)
	}
	if err != nil || !deleted {
		return deleted, err
	}

	// The transaction reserves the sole database connection from cleanup capture
	// through deletion. A concurrent upload metadata write therefore either lands
	// in the manifest first or fails after the task rows disappear and rolls back
	// its newly published file. Filesystem and git work remain outside SQLite.
	var cleanupErrors []error
	removeFile := func(kind, path string) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			applog.Infof("[task-svc] Delete error removing %s %s after durable deletion: %v", kind, path, err)
			cleanupErrors = append(cleanupErrors, fmt.Errorf("task deleted but removing %s %s: %w", kind, path, err))
		}
	}
	for _, path := range manifest.TaskAttachmentPaths {
		removeFile("attachment", path)
	}
	for _, path := range manifest.ExecutionAttachmentPaths {
		removeFile("execution attachment", path)
	}
	for _, sessionID := range manifest.PendingUploadSessionIDs {
		path := filepath.Join(s.uploadsDir, "chat", "pending", sessionID)
		unlockSession := attachmentsession.Lock(sessionID)
		if s.beforePendingSessionRemoval != nil {
			s.beforePendingSessionRemoval(sessionID)
		}
		removeErr := os.RemoveAll(path)
		unlockSession()
		if removeErr != nil {
			applog.Infof("[task-svc] Delete error removing pending uploads %s after durable deletion: %v", path, removeErr)
			cleanupErrors = append(cleanupErrors, fmt.Errorf("task deleted but removing pending uploads %s: %w", path, removeErr))
		}
	}
	if err := errors.Join(cleanupErrors...); err != nil {
		return true, err
	}

	applog.Infof("[task-svc] Delete success id=%s (deleted %d task attachments, %d execution attachments, %d pending upload sessions)", id, len(manifest.TaskAttachmentPaths), len(manifest.ExecutionAttachmentPaths), len(manifest.PendingUploadSessionIDs))
	return true, nil
}

func (s *TaskService) RunTask(ctx context.Context, id string) error {
	applog.Infof("[task-svc] RunTask id=%s", id)
	task, err := s.repo.GetByID(ctx, id)
	if err != nil {
		applog.Infof("[task-svc] RunTask error fetching: %v", err)
		return err
	}
	if task == nil {
		applog.Infof("[task-svc] RunTask not found id=%s", id)
		return fmt.Errorf("task not found: %s", id)
	}
	s.resumeGoalStoppedByUser(ctx, id, "user")

	if s.queuedTaskThreadFollowupHook != nil {
		handled, err := s.queuedTaskThreadFollowupHook(ctx, id)
		if err != nil {
			applog.Infof("[task-svc] RunTask queued task-thread follow-up promotion failed id=%s: %v", id, err)
			return err
		}
		if handled {
			if s.workerSvc != nil {
				s.workerSvc.ClearCancellationRequested(id)
			}
			applog.Infof("[task-svc] RunTask promoted queued task-thread follow-up id=%s", id)
			return nil
		}
	}
	if s.failedTaskThreadFollowupRetryHook != nil {
		handled, err := s.failedTaskThreadFollowupRetryHook(ctx, id)
		if err != nil {
			applog.Infof("[task-svc] RunTask failed task-thread follow-up retry failed id=%s: %v", id, err)
			return err
		}
		if handled {
			if s.workerSvc != nil {
				s.workerSvc.ClearCancellationRequested(id)
			}
			applog.Infof("[task-svc] RunTask retried failed task-thread follow-up id=%s", id)
			return nil
		}
	}

	// Move to active category if not already active (e.g., task is in backlog).
	// This must happen before submission so the UI reflects the move immediately.
	if task.Category != models.CategoryActive {
		if err := s.repo.UpdateCategory(ctx, id, models.CategoryActive); err != nil {
			applog.Infof("[task-svc] RunTask error updating category: %v", err)
			return fmt.Errorf("update category: %w", err)
		}
		task.Category = models.CategoryActive
	}

	updated, err := s.repo.SetPendingIfNotRunningOrQueued(ctx, id)
	if err != nil {
		applog.Infof("[task-svc] RunTask guarded pending update error: %v", err)
		return fmt.Errorf("set pending with guard: %w", err)
	}
	if !updated {
		applog.Infof("[task-svc] RunTask no-op id=%s current_status=%s (already running/queued)", id, task.Status)
		return nil
	}

	applog.Infof("[task-svc] RunTask activating id=%s title=%q", id, task.Title)
	task.Status = models.StatusPending
	return s.submitActivatedTask(ctx, *task, "user")
}

func (s *TaskService) CancelTask(ctx context.Context, id string) error {
	applog.Infof("[task-svc] CancelTask id=%s", id)
	task, err := s.repo.GetByID(ctx, id)
	if err != nil {
		applog.Infof("[task-svc] CancelTask error fetching: %v", err)
		return err
	}
	if task == nil {
		applog.Infof("[task-svc] CancelTask not found id=%s", id)
		return fmt.Errorf("task not found: %s", id)
	}
	if task.Status != models.StatusRunning && task.Status != models.StatusQueued && !(task.Status == models.StatusPending && task.Category == models.CategoryActive) {
		applog.Infof("[task-svc] CancelTask task not cancellable id=%s status=%s category=%s", id, task.Status, task.Category)
		return fmt.Errorf("task is not running, queued, or active pending")
	}

	if s.workerSvc != nil {
		s.workerSvc.MarkCancellationRequested(id)
	}

	if s.goalSvc != nil {
		if err := s.goalSvc.PauseActiveGoalStoppedByUser(ctx, id); err != nil && !errors.Is(err, ErrTaskGoalNotFound) {
			applog.Infof("[task-svc] CancelTask error pausing active goal after user stop id=%s: %v", id, err)
		}
	}

	// Kill the running CLI process or cancel a queued follow-up waiting for a worker slot.
	// This must happen BEFORE updating the DB status so the worker/handler sees
	// context.Canceled and marks the execution as cancelled (not failed).
	if s.workerSvc != nil {
		s.workerSvc.CancelRunningTask(id)
	}

	applog.Infof("[task-svc] CancelTask setting status=cancelled, category=backlog id=%s title=%q", id, task.Title)

	// Move cancelled tasks to backlog so they remain visible in the kanban board
	// and can be re-run later. Status stays "cancelled" to reflect what happened.
	if err := s.repo.UpdateStatus(ctx, id, models.StatusCancelled); err != nil {
		applog.Infof("[task-svc] CancelTask error updating status: %v", err)
		return err
	}
	if err := s.repo.UpdateCategory(ctx, id, models.CategoryBacklog); err != nil {
		applog.Infof("[task-svc] CancelTask error moving to backlog: %v", err)
		return fmt.Errorf("move cancelled task to backlog: %w", err)
	}
	applog.Infof("[task-svc] CancelTask moved to backlog id=%s", id)

	applog.Infof("[task-svc] CancelTask success id=%s", id)
	return nil
}

func (s *TaskService) CountByProjectAndCategory(ctx context.Context, projectID string) (map[string]int, error) {
	return s.repo.CountByProjectAndCategory(ctx, projectID)
}

func (s *TaskService) MoveCompletedActiveToCompleted(ctx context.Context) (int, error) {
	applog.Infof("[task-svc] MoveCompletedActiveToCompleted called")
	count, err := s.repo.MoveCompletedActiveToCompleted(ctx)
	if err != nil {
		applog.Infof("[task-svc] MoveCompletedActiveToCompleted error: %v", err)
		return 0, err
	}
	applog.Infof("[task-svc] MoveCompletedActiveToCompleted moved %d tasks", count)
	return count, nil
}

func (s *TaskService) DeleteAllCompleted(ctx context.Context, projectID string) (int, error) {
	return s.deleteAllByCategory(ctx, projectID, models.CategoryCompleted)
}

func (s *TaskService) DeleteAllBacklog(ctx context.Context, projectID string) (int, error) {
	return s.deleteAllByCategory(ctx, projectID, models.CategoryBacklog)
}

func (s *TaskService) DeleteAllChat(ctx context.Context, projectID string) (int, error) {
	return s.deleteAllByCategory(ctx, projectID, models.CategoryChat)
}

func (s *TaskService) deleteAllByCategory(ctx context.Context, projectID string, category models.TaskCategory) (int, error) {
	applog.Infof("[task-svc] delete all category=%s project=%s", category, projectID)
	tasks, err := s.repo.ListByProject(ctx, projectID, string(category))
	if err != nil {
		return 0, fmt.Errorf("listing %s tasks: %w", category, err)
	}

	deleted := 0
	var deletionErrors []error
	for _, task := range tasks {
		wasDeleted, deleteErr := s.deleteTask(ctx, task.ID, projectID, category)
		if wasDeleted {
			deleted++
		}
		if deleteErr != nil {
			deletionErrors = append(deletionErrors, fmt.Errorf("deleting task %s: %w", task.ID, deleteErr))
		}
	}
	if err := errors.Join(deletionErrors...); err != nil {
		return deleted, err
	}
	applog.Infof("[task-svc] deleted %d category=%s tasks for project=%s", deleted, category, projectID)
	return deleted, nil
}

func (s *TaskService) ActivateAllBacklog(ctx context.Context, projectID string) (int, error) {
	applog.Infof("[task-svc] ActivateAllBacklog called for project=%s", projectID)

	// Update all backlog tasks to active category with pending status
	count, err := s.repo.ActivateAllBacklog(ctx, projectID)
	if err != nil {
		applog.Infof("[task-svc] ActivateAllBacklog error: %v", err)
		return 0, err
	}

	// Submit the activated tasks to the worker pool
	if count > 0 {
		activeTasks, err := s.repo.ListByProject(ctx, projectID, string(models.CategoryActive))
		if err != nil {
			applog.Infof("[task-svc] ActivateAllBacklog error listing active tasks: %v", err)
			// Don't fail the operation if we can't list tasks for submission
		} else {
			for _, task := range activeTasks {
				if task.Status == models.StatusPending {
					if err := s.submitActivatedTask(ctx, task, "user"); err != nil {
						applog.Infof("[task-svc] ActivateAllBacklog error activating task %s: %v", task.ID, err)
					}
				}
			}
		}
	}

	applog.Infof("[task-svc] ActivateAllBacklog activated %d tasks for project %s", count, projectID)
	return count, nil
}

func (s *TaskService) GetTasksWithSchedulesByProject(ctx context.Context, projectID string) ([]repository.TaskWithSchedule, error) {
	applog.Infof("[task-svc] GetTasksWithSchedulesByProject project=%s", projectID)
	tasks, err := s.repo.ListWithSchedulesByProject(ctx, projectID)
	if err != nil {
		applog.Infof("[task-svc] GetTasksWithSchedulesByProject error: %v", err)
		return nil, err
	}
	applog.Infof("[task-svc] GetTasksWithSchedulesByProject returned %d tasks", len(tasks))
	return tasks, nil
}

func (s *TaskService) ReorderTask(ctx context.Context, taskID string, newPosition int) error {
	applog.Infof("[task-svc] ReorderTask id=%s position=%d", taskID, newPosition)
	if err := s.repo.ReorderTask(ctx, taskID, newPosition); err != nil {
		applog.Infof("[task-svc] ReorderTask error: %v", err)
		return err
	}
	applog.Infof("[task-svc] ReorderTask success id=%s", taskID)
	return nil
}

// ExecuteBacklogTasks activates and executes backlog tasks, optionally filtered by priority.
// Tasks are moved to active category and submitted to the worker pool.
// If priority is 0, all eligible backlog tasks are executed.
// Returns the list of tasks submitted and the count.
func (s *TaskService) ExecuteBacklogTasks(ctx context.Context, projectID string, priority int) ([]models.Task, int, error) {
	applog.Infof("[task-svc] ExecuteBacklogTasks project=%s priority=%d", projectID, priority)

	tasks, err := s.repo.ListBacklogByPriority(ctx, projectID, priority)
	if err != nil {
		applog.Infof("[task-svc] ExecuteBacklogTasks error listing tasks: %v", err)
		return nil, 0, fmt.Errorf("listing backlog tasks: %w", err)
	}

	if len(tasks) == 0 {
		applog.Infof("[task-svc] ExecuteBacklogTasks no eligible tasks found")
		return []models.Task{}, 0, nil
	}

	applog.Infof("[task-svc] ExecuteBacklogTasks found %d eligible tasks", len(tasks))

	submitted := 0
	for _, task := range tasks {
		// Move to active category
		if err := s.repo.UpdateCategory(ctx, task.ID, models.CategoryActive); err != nil {
			applog.Infof("[task-svc] ExecuteBacklogTasks error updating category for task %s: %v", task.ID, err)
			continue
		}
		task.Category = models.CategoryActive

		// Reset status to pending if needed
		if task.Status != models.StatusPending {
			if err := s.repo.UpdateStatus(ctx, task.ID, models.StatusPending); err != nil {
				applog.Infof("[task-svc] ExecuteBacklogTasks error updating status for task %s: %v", task.ID, err)
				continue
			}
			task.Status = models.StatusPending
		}

		// Activate task execution or swarm planning.
		if err := s.submitActivatedTask(ctx, task, "user"); err != nil {
			applog.Infof("[task-svc] ExecuteBacklogTasks error activating task %s: %v", task.ID, err)
			continue
		}
		submitted++
	}

	applog.Infof("[task-svc] ExecuteBacklogTasks submitted %d tasks for execution", submitted)
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
	applog.Infof("[task-svc] ExecuteTasksByTags tags=%v project=%s minPriority=%d includeCompleted=%v", tags, projectID, minPriority, includeCompleted)

	// Find matching tasks in backlog/active by default.
	// Completed status/category is opt-in only to avoid accidental mass re-runs.
	var allTasks []models.Task
	categoriesToSearch := []models.TaskCategory{models.CategoryBacklog, models.CategoryActive}
	statusesToSearch := []models.TaskStatus{models.StatusPending, models.StatusFailed, models.StatusCancelled, models.StatusBlocked}
	if includeCompleted {
		categoriesToSearch = append(categoriesToSearch, models.CategoryCompleted)
		statusesToSearch = append(statusesToSearch, models.StatusCompleted)
	}
	for _, status := range statusesToSearch {
		for _, category := range categoriesToSearch {
			tasks, err := s.repo.ListByTags(ctx, tags, projectID, category, minPriority, status)
			if err != nil {
				applog.Infof("[task-svc] ExecuteTasksByTags error listing %s tasks: %v", category, err)
				return nil, 0, fmt.Errorf("listing %s tasks: %w", category, err)
			}
			allTasks = append(allTasks, tasks...)
		}
	}

	if len(allTasks) == 0 {
		applog.Infof("[task-svc] ExecuteTasksByTags no matching tasks found")
		return []models.Task{}, 0, nil
	}

	applog.Infof("[task-svc] ExecuteTasksByTags found %d matching tasks", len(allTasks))

	// Move backlog tasks to active and reset status to pending
	submitted := 0
	for _, task := range allTasks {
		if task.Status == models.StatusBlocked && task.SwarmRole != models.SwarmRoleParent {
			continue
		}
		needsStatusUpdate := task.Status != models.StatusPending
		needsCategoryUpdate := task.Category != models.CategoryActive

		// Update category if needed
		if needsCategoryUpdate {
			if err := s.repo.UpdateCategory(ctx, task.ID, models.CategoryActive); err != nil {
				applog.Infof("[task-svc] ExecuteTasksByTags error updating category for task %s: %v", task.ID, err)
				continue
			}
			task.Category = models.CategoryActive
			applog.Infof("[task-svc] ExecuteTasksByTags moved task %s to active", task.ID)
		}

		// Update status if needed (reset failed/cancelled tasks to pending)
		if needsStatusUpdate {
			if err := s.repo.UpdateStatus(ctx, task.ID, models.StatusPending); err != nil {
				applog.Infof("[task-svc] ExecuteTasksByTags error updating status for task %s: %v", task.ID, err)
				continue
			}
			task.Status = models.StatusPending
			applog.Infof("[task-svc] ExecuteTasksByTags reset status to pending for task %s", task.ID)
		}

		// Activate task execution or swarm planning.
		if err := s.submitActivatedTask(ctx, task, "user"); err != nil {
			applog.Infof("[task-svc] ExecuteTasksByTags error activating task %s: %v", task.ID, err)
			continue
		}
		submitted++
	}

	applog.Infof("[task-svc] ExecuteTasksByTags submitted %d tasks for execution", submitted)
	return allTasks, submitted, nil
}

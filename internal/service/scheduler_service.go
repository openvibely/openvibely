package service

import (
	"context"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/update"
)

// staleQueuedTaskTimeout is how long a task can stay in "queued" status before
// the scheduler considers it orphaned and resets it to "pending". The "queued"
// status is set by thread follow-ups; if the goroutine handling the follow-up
// crashes, the task would be stuck in "queued" forever without this recovery.
const staleQueuedTaskTimeout = 10 * time.Minute

// SchedulerService manages scheduled task execution.
// On startup, it immediately checks for missed schedules (tasks that were scheduled
// to run while the app was down) and executes them. For repeating schedules, only
// one execution occurs on startup, and the next_run is calculated from the current
// time (not catching up on all missed occurrences).
type SwarmPlannerStarter interface {
	StartPlanner(ctx context.Context, parentTaskID string) error
	StartPlannerForScheduledRun(ctx context.Context, parentTaskID string, startsNewContext bool) error
}

type SchedulerService struct {
	scheduleRepo   *repository.ScheduleRepo
	taskRepo       *repository.TaskRepo
	automationRepo *repository.AutomationRepo
	workerSvc      *WorkerService
	worktreeSvc    *WorktreeService
	swarmStarter   SwarmPlannerStarter
	interval       time.Duration
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	lastCleanupAt  time.Time
	updateTracker  *update.WorkTracker
}

func NewSchedulerService(scheduleRepo *repository.ScheduleRepo, taskRepo *repository.TaskRepo, workerSvc *WorkerService) *SchedulerService {
	return &SchedulerService{
		scheduleRepo: scheduleRepo,
		taskRepo:     taskRepo,
		workerSvc:    workerSvc,
		interval:     5 * time.Second,
	}
}

// SetWorktreeService sets the worktree service for automatic cleanup.
func (s *SchedulerService) SetAutomationRepo(repo *repository.AutomationRepo) {
	s.automationRepo = repo
}

func (s *SchedulerService) SetUpdateWorkTracker(tracker *update.WorkTracker) {
	s.updateTracker = tracker
}

func (s *SchedulerService) SetWorktreeService(wts *WorktreeService) {
	s.worktreeSvc = wts
}

func (s *SchedulerService) SetSwarmPlannerStarter(starter SwarmPlannerStarter) {
	s.swarmStarter = starter
}

func (s *SchedulerService) Start(ctx context.Context) {
	schedulerCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run(schedulerCtx)
	}()
	applog.Infof("[scheduler] started, checking every %s", s.interval)
}

func (s *SchedulerService) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	applog.Infof("[scheduler] stopped")
}

func (s *SchedulerService) run(ctx context.Context) {
	// Check immediately on startup to catch any missed schedules
	applog.Infof("[scheduler] initial check on startup (catching up on any missed schedules)")
	s.checkDueTasks(ctx)
	s.checkActiveTasks(ctx)
	s.checkWorktreeCleanup(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			applog.Infof("[scheduler] context cancelled, exiting run loop")
			return
		case t := <-ticker.C:
			applog.Infof("[scheduler] tick at %s, checking due tasks and active tasks", t.Format("15:04:05"))
			s.checkDueTasks(ctx)
			s.checkActiveTasks(ctx)

			// Run worktree cleanup every 5 minutes to avoid excessive checks
			if s.worktreeSvc != nil && time.Since(s.lastCleanupAt) >= 5*time.Minute {
				s.checkWorktreeCleanup(ctx)
			}
		}
	}
}

// checkDueTasks finds scheduled tasks whose next_run has passed and submits them.
func (s *SchedulerService) checkDueTasks(ctx context.Context) {
	if s.updateTracker != nil {
		done, err := s.updateTracker.Start(update.WorkAutomation)
		if err != nil {
			return
		}
		defer done()
	}
	now := time.Now().UTC()
	applog.Infof("[scheduler] checkDueTasks now=%s", now.Format("2006-01-02 15:04:05"))

	schedules, err := s.scheduleRepo.ListDue(ctx, now)
	if err != nil {
		applog.Infof("[scheduler] checkDueTasks error listing due schedules: %v", err)
		return
	}
	applog.Infof("[scheduler] checkDueTasks found %d due schedules", len(schedules))

	for _, sched := range schedules {
		if err := models.ValidateScheduleRepeatInterval(sched.RepeatInterval); err != nil {
			applog.Infof("[scheduler] skipping invalid schedule %s: %v", sched.ID, err)
			continue
		}
		if s.automationRepo != nil {
			owner, ownerErr := s.automationRepo.GetTriggerOwner(ctx, sched.ID)
			if ownerErr != nil {
				applog.Infof("[scheduler] automation trigger owner lookup failed schedule=%s: %v", sched.ID, ownerErr)
				continue
			}
			if owner != nil {
				nextRun := sched.ComputeNextRun(now)
				invocation, dispatch, claimErr := s.automationRepo.ClaimScheduledOccurrence(ctx, sched, now, nextRun)
				if claimErr != nil {
					if claimErr != repository.ErrAutomationScheduleChanged {
						applog.Infof("[scheduler] automation occurrence claim failed schedule=%s automation=%s: %v", sched.ID, owner.AutomationID, claimErr)
					}
					continue
				}
				if invocation != nil {
					applog.Infof("[scheduler] automation occurrence claimed automation=%s invocation=%s status=%s dispatch=%v", owner.AutomationID, invocation.ID, invocation.Status, dispatch != nil)
				}
				continue
			}
		}
		task, err := s.taskRepo.GetByID(ctx, sched.TaskID)
		if err != nil || task == nil {
			applog.Infof("[scheduler] checkDueTasks error getting task %s: %v", sched.TaskID, err)
			continue
		}

		// Skip chat tasks - they bypass the worker pool and are handled in real-time
		if task.Category == "chat" {
			applog.Infof("[scheduler] checkDueTasks skipping chat task %s (chat tasks bypass scheduler)", task.ID)
			continue
		}

		// Skip if task is already running
		if task.Status == "running" {
			applog.Infof("[scheduler] checkDueTasks skipping task %s (already running)", task.ID)
			continue
		}

		// For non-recurring schedules (RepeatOnce), skip completed/failed tasks.
		// These represent one-time schedules that shouldn't auto-reset when rescheduled.
		// For recurring schedules, we DO want to reset and re-execute.
		if sched.RepeatType == models.RepeatOnce && (task.Status == models.StatusCompleted || task.Status == models.StatusFailed) {
			applog.Infof("[scheduler] checkDueTasks skipping one-time schedule task %s (status=%s, drag/drop reschedule should not trigger execution)", task.ID, task.Status)
			continue
		}

		// Reset task status to pending so ClaimTask can pick it up
		if task.Status != "pending" {
			if err := s.taskRepo.UpdateStatus(ctx, task.ID, "pending"); err != nil {
				applog.Infof("[scheduler] checkDueTasks error resetting task %s status to pending: %v", task.ID, err)
				continue
			}
			task.Status = "pending"
		}

		// Reset category to "scheduled" if needed — worker prunes tasks whose
		// category is not "active" or "scheduled". A recurring task that completed
		// its last run will have category "completed", so we must restore it.
		if task.Category != models.CategoryActive && task.Category != models.CategoryScheduled {
			prevCategory := task.Category
			if err := s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryScheduled); err != nil {
				applog.Infof("[scheduler] checkDueTasks error resetting task %s category to scheduled: %v", task.ID, err)
				continue
			}
			task.Category = models.CategoryScheduled
			applog.Infof("[scheduler] checkDueTasks reset task %s category from %q to %q for recurring schedule", task.ID, prevCategory, models.CategoryScheduled)
		}

		// A clear-context schedule starts a new replay segment without deleting
		// earlier executions, lifecycle records, goals, or audit history.
		task.StartsNewContext = sched.ClearContextOnStart

		// Log if this is a missed schedule (next_run is significantly in the past)
		if sched.NextRun != nil && sched.NextRun.Before(now.Add(-1*time.Minute)) {
			timeSinceDue := now.Sub(*sched.NextRun)
			applog.Infof("[scheduler] checkDueTasks MISSED SCHEDULE: task id=%s title=%q was due %s ago, executing now",
				task.ID, task.Title, timeSinceDue.Round(time.Second))
		} else {
			applog.Infof("[scheduler] checkDueTasks submitting scheduled task id=%s title=%q schedule=%s repeat=%s",
				task.ID, task.Title, sched.ID, sched.RepeatType)
		}
		if task.SwarmRole == models.SwarmRoleParent && s.swarmStarter != nil {
			if err := s.swarmStarter.StartPlannerForScheduledRun(ctx, task.ID, sched.ClearContextOnStart); err != nil {
				applog.Infof("[scheduler] checkDueTasks error starting swarm planner task=%s: %v", task.ID, err)
				continue
			}
		} else {
			s.workerSvc.Submit(*task)
		}

		// Compute next run
		nextRun := sched.ComputeNextRun(now)
		if nextRun != nil {
			applog.Infof("[scheduler] checkDueTasks next_run for schedule %s: %s", sched.ID, nextRun.Format("2006-01-02 15:04:05"))
		} else {
			applog.Infof("[scheduler] checkDueTasks schedule %s has no next run (one-time, completed)", sched.ID)
		}
		if err := s.scheduleRepo.MarkRan(ctx, sched.ID, now, nextRun); err != nil {
			applog.Infof("[scheduler] checkDueTasks error updating schedule %s: %v", sched.ID, err)
		}
	}
}

// checkActiveTasks finds tasks in the Active category that are pending and auto-submits them.
// Also recovers stale "queued" tasks (orphaned by crashed thread follow-up goroutines).
func (s *SchedulerService) checkActiveTasks(ctx context.Context) {
	tasks, err := s.taskRepo.ListActivePending(ctx)
	if err != nil {
		applog.Infof("[scheduler] checkActiveTasks error: %v", err)
		return
	}

	if len(tasks) > 0 {
		applog.Infof("[scheduler] checkActiveTasks found %d pending active tasks", len(tasks))
	}

	for _, task := range tasks {
		if task.SwarmRole == models.SwarmRoleParent && s.swarmStarter != nil {
			applog.Infof("[scheduler] checkActiveTasks starting swarm planner task id=%s title=%q project=%s",
				task.ID, task.Title, task.ProjectID)
			if err := s.swarmStarter.StartPlanner(ctx, task.ID); err != nil {
				applog.Infof("[scheduler] checkActiveTasks error starting swarm planner task=%s: %v", task.ID, err)
			}
			continue
		}
		applog.Infof("[scheduler] checkActiveTasks auto-submitting task id=%s title=%q project=%s",
			task.ID, task.Title, task.ProjectID)
		s.workerSvc.Submit(task)
	}

	// Recover stale queued tasks. The "queued" status is set by TaskThreadSend
	// for thread follow-ups that block waiting for worker slots. If the goroutine
	// crashes or times out without updating the status, the task is orphaned.
	// Reset these to "pending" so the scheduler can re-submit them.
	staleTasks, err := s.taskRepo.ListStaleQueuedTasks(ctx, staleQueuedTaskTimeout)
	if err != nil {
		applog.Infof("[scheduler] checkActiveTasks error listing stale queued tasks: %v", err)
		return
	}
	for _, task := range staleTasks {
		claimed, err := s.taskRepo.ReclaimStaleQueuedTask(ctx, task.ID, staleQueuedTaskTimeout)
		if err != nil {
			applog.Infof("[scheduler] checkActiveTasks error reclaiming stale task %s: %v", task.ID, err)
			continue
		}
		if !claimed {
			continue
		}
		applog.Infof("[scheduler] checkActiveTasks recovering stale queued task id=%s title=%q (queued for >%s)",
			task.ID, task.Title, staleQueuedTaskTimeout)
		task.Status = models.StatusPending
		if task.SwarmRole == models.SwarmRoleParent && s.swarmStarter != nil {
			applog.Infof("[scheduler] checkActiveTasks starting swarm planner for recovered stale queued task id=%s title=%q project=%s",
				task.ID, task.Title, task.ProjectID)
			if err := s.swarmStarter.StartPlanner(ctx, task.ID); err != nil {
				applog.Infof("[scheduler] checkActiveTasks error starting swarm planner task=%s: %v", task.ID, err)
			}
			continue
		}
		s.workerSvc.Submit(task)
	}
}

// checkWorktreeCleanup scans for merged worktrees and cleans them up automatically.
// This handles cases where branches are manually merged outside of auto-merge.
func (s *SchedulerService) checkWorktreeCleanup(ctx context.Context) {
	if s.worktreeSvc == nil {
		return
	}

	s.lastCleanupAt = time.Now()
	if err := s.worktreeSvc.CleanupMergedWorktrees(ctx); err != nil {
		applog.Infof("[scheduler] checkWorktreeCleanup error: %v", err)
	}
}

package service

import (
	"context"
	"log"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
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
type SchedulerService struct {
	scheduleRepo  *repository.ScheduleRepo
	taskRepo      *repository.TaskRepo
	workerSvc     *WorkerService
	worktreeSvc   *WorktreeService
	interval      time.Duration
	cancel        context.CancelFunc
	lastCleanupAt time.Time
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
func (s *SchedulerService) SetWorktreeService(wts *WorktreeService) {
	s.worktreeSvc = wts
}

func (s *SchedulerService) Start(ctx context.Context) {
	schedulerCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	go s.run(schedulerCtx)
	log.Printf("[scheduler] started, checking every %s", s.interval)
}

func (s *SchedulerService) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	log.Println("[scheduler] stopped")
}

func (s *SchedulerService) run(ctx context.Context) {
	// Check immediately on startup to catch any missed schedules
	log.Printf("[scheduler] initial check on startup (catching up on any missed schedules)")
	s.checkDueTasks(ctx)
	s.checkActiveTasks(ctx)
	s.checkWorktreeCleanup(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[scheduler] context cancelled, exiting run loop")
			return
		case t := <-ticker.C:
			log.Printf("[scheduler] tick at %s, checking due tasks and active tasks", t.Format("15:04:05"))
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
	now := time.Now().UTC()
	log.Printf("[scheduler] checkDueTasks now=%s", now.Format("2006-01-02 15:04:05"))

	schedules, err := s.scheduleRepo.ListDue(ctx, now)
	if err != nil {
		log.Printf("[scheduler] checkDueTasks error listing due schedules: %v", err)
		return
	}
	log.Printf("[scheduler] checkDueTasks found %d due schedules", len(schedules))

	for _, sched := range schedules {
		task, err := s.taskRepo.GetByID(ctx, sched.TaskID)
		if err != nil || task == nil {
			log.Printf("[scheduler] checkDueTasks error getting task %s: %v", sched.TaskID, err)
			continue
		}

		// Skip chat tasks - they bypass the worker pool and are handled in real-time
		if task.Category == "chat" {
			log.Printf("[scheduler] checkDueTasks skipping chat task %s (chat tasks bypass scheduler)", task.ID)
			continue
		}

		// Skip if task is already running
		if task.Status == "running" {
			log.Printf("[scheduler] checkDueTasks skipping task %s (already running)", task.ID)
			continue
		}

		// For non-recurring schedules (RepeatOnce), skip completed/failed tasks.
		// These represent one-time schedules that shouldn't auto-reset when rescheduled.
		// For recurring schedules, we DO want to reset and re-execute.
		if sched.RepeatType == models.RepeatOnce && (task.Status == models.StatusCompleted || task.Status == models.StatusFailed) {
			log.Printf("[scheduler] checkDueTasks skipping one-time schedule task %s (status=%s, drag/drop reschedule should not trigger execution)", task.ID, task.Status)
			continue
		}

		// Reset task status to pending so ClaimTask can pick it up
		if task.Status != "pending" {
			if err := s.taskRepo.UpdateStatus(ctx, task.ID, "pending"); err != nil {
				log.Printf("[scheduler] checkDueTasks error resetting task %s status to pending: %v", task.ID, err)
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
				log.Printf("[scheduler] checkDueTasks error resetting task %s category to scheduled: %v", task.ID, err)
				continue
			}
			task.Category = models.CategoryScheduled
			log.Printf("[scheduler] checkDueTasks reset task %s category from %q to %q for recurring schedule", task.ID, prevCategory, models.CategoryScheduled)
		}

		// Log if this is a missed schedule (next_run is significantly in the past)
		if sched.NextRun != nil && sched.NextRun.Before(now.Add(-1*time.Minute)) {
			timeSinceDue := now.Sub(*sched.NextRun)
			log.Printf("[scheduler] checkDueTasks MISSED SCHEDULE: task id=%s title=%q was due %s ago, executing now",
				task.ID, task.Title, timeSinceDue.Round(time.Second))
		} else {
			log.Printf("[scheduler] checkDueTasks submitting scheduled task id=%s title=%q schedule=%s repeat=%s",
				task.ID, task.Title, sched.ID, sched.RepeatType)
		}
		s.workerSvc.Submit(*task)

		// Compute next run
		nextRun := sched.ComputeNextRun(now)
		if nextRun != nil {
			log.Printf("[scheduler] checkDueTasks next_run for schedule %s: %s", sched.ID, nextRun.Format("2006-01-02 15:04:05"))
		} else {
			log.Printf("[scheduler] checkDueTasks schedule %s has no next run (one-time, completed)", sched.ID)
		}
		if err := s.scheduleRepo.MarkRan(ctx, sched.ID, now, nextRun); err != nil {
			log.Printf("[scheduler] checkDueTasks error updating schedule %s: %v", sched.ID, err)
		}
	}
}

// checkActiveTasks finds tasks in the Active category that are pending and auto-submits them.
// Also recovers stale "queued" tasks (orphaned by crashed thread follow-up goroutines).
func (s *SchedulerService) checkActiveTasks(ctx context.Context) {
	tasks, err := s.taskRepo.ListActivePending(ctx)
	if err != nil {
		log.Printf("[scheduler] checkActiveTasks error: %v", err)
		return
	}

	if len(tasks) > 0 {
		log.Printf("[scheduler] checkActiveTasks found %d pending active tasks", len(tasks))
	}

	for _, task := range tasks {
		log.Printf("[scheduler] checkActiveTasks auto-submitting task id=%s title=%q project=%s",
			task.ID, task.Title, task.ProjectID)
		s.workerSvc.Submit(task)
	}

	// Recover stale queued tasks. The "queued" status is set by TaskThreadSend
	// for thread follow-ups that block waiting for worker slots. If the goroutine
	// crashes or times out without updating the status, the task is orphaned.
	// Reset these to "pending" so the scheduler can re-submit them.
	staleTasks, err := s.taskRepo.ListStaleQueuedTasks(ctx, staleQueuedTaskTimeout)
	if err != nil {
		log.Printf("[scheduler] checkActiveTasks error listing stale queued tasks: %v", err)
		return
	}
	for _, task := range staleTasks {
		log.Printf("[scheduler] checkActiveTasks recovering stale queued task id=%s title=%q (queued for >%s)",
			task.ID, task.Title, staleQueuedTaskTimeout)
		if err := s.taskRepo.UpdateStatus(ctx, task.ID, models.StatusPending); err != nil {
			log.Printf("[scheduler] checkActiveTasks error resetting stale task %s to pending: %v", task.ID, err)
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
		log.Printf("[scheduler] checkWorktreeCleanup error: %v", err)
	}
}

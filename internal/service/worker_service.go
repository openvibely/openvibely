package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/lifecycle"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/memory"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/update"
)

var ErrTaskAlreadyQueuedOrRunning = errors.New("task is already queued or running")

type preparedAutomationDispatch struct {
	Envelope    models.AutomationDispatchEnvelope
	ExecutionID string
}

type WorkerService struct {
	llmSvc        *LLMService
	projectRepo   *repository.ProjectRepo
	taskRepo      *repository.TaskRepo
	llmConfigRepo *repository.LLMConfigRepo

	mu         sync.Mutex
	numWorkers int                                   // max parallel tasks (global limit)
	queue      []models.Task                         // FIFO task queue
	pending    map[string]bool                       // task IDs in queue or running (dedup)
	prepared   map[string]preparedAutomationDispatch // prepared Automation dispatches keyed by task ID
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup

	cancelMu    sync.Mutex
	cancelFuncs map[string]context.CancelFunc // taskID -> cancel func for running tasks

	// Per-project concurrency tracking
	projectRunning sync.Map // projectID -> *int32 (atomic counter)
	totalRunning   int32    // atomic counter of total running tasks

	// Per-model concurrency tracking
	modelRunning sync.Map // agentConfigID -> *int32 (atomic counter)

	// Test observability: tasks are sent here on Submit so tests can verify submissions
	submitted chan models.Task

	// onTaskComplete, when set, is invoked after every task completion
	// (success or failure) for low-level post-completion side effects.
	// Agent/model-driven behavior should prefer lifecycle hooks. Errors thrown
	// by the callback are logged but never affect task status.
	onTaskComplete func(task models.Task, executionErr error)

	// lifecycleRunner, when set, runs route_task/before_run/after_complete hook
	// slots around task execution per runbook §Runner Changes.
	lifecycleRunner *lifecycle.Runner

	globalSkillRoot                  string
	agentRepo                        *repository.AgentRepo
	lifecycleRepo                    *repository.LifecycleRepo
	execRepo                         *repository.ExecutionRepo
	skillAnalyticsRepo               *repository.SkillAnalyticsRepo
	mutationRecorder                 func(models.Task) agentlibrary.MutationRecorder
	agentRootSyncService             *AgentLibraryMaintenanceService
	taskGoalSvc                      *TaskGoalService
	automationRepo                   *repository.AutomationRepo
	afterCompleteRuntimeToolProvider func(context.Context, models.Task) *llmcontracts.RuntimeTools
	beforeOrdinaryTaskClaim          func(models.Task) // deterministic pre-claim test barrier
	afterOrdinaryTaskClaim           func(models.Task) // deterministic persisted-claim test barrier
	beforeQueuedAutomationTaskClaim  func(models.Task) // deterministic prepared-dispatch test barrier
	currentCatalog                   atomic.Value      // stores *agentskills.Catalog for hook skill resolution
	admissionOpen                    func() bool
	updateTracker                    *update.WorkTracker
}

// SetLifecycleRunner attaches the lifecycle runner so the worker can invoke
// before_run hooks ahead of task execution and after_complete hooks once
// execution finishes. Optional: when unset the worker behaves exactly as
// before.
func (w *WorkerService) SetLifecycleRunner(r *lifecycle.Runner) {
	w.lifecycleRunner = r
	if r != nil {
		r.SetInputCustomizer(w.lifecycleHookInput)
		r.SetExecutionStartedObserver(w.recordLifecycleHookSkillSelected)
	}
}

func (w *WorkerService) SetLifecycleSkillRoot(root string) {
	w.globalSkillRoot = root
}

func (w *WorkerService) SetLifecycleAgentRepo(repo *repository.AgentRepo) {
	w.agentRepo = repo
}

func (w *WorkerService) SetLifecycleRepo(repo *repository.LifecycleRepo) {
	w.lifecycleRepo = repo
}

func (w *WorkerService) SetExecutionRepo(repo *repository.ExecutionRepo) {
	w.execRepo = repo
}

func (w *WorkerService) SetLifecycleMutationRecorderFactory(fn func(models.Task) agentlibrary.MutationRecorder) {
	w.mutationRecorder = fn
}

func (w *WorkerService) SetAgentRootSyncService(svc *AgentLibraryMaintenanceService) {
	w.agentRootSyncService = svc
}

func (w *WorkerService) SetTaskGoalService(svc *TaskGoalService) {
	w.taskGoalSvc = svc
}

func (w *WorkerService) SetAutomationRepo(repo *repository.AutomationRepo) {
	w.automationRepo = repo
}

func (w *WorkerService) SetAfterCompleteRuntimeToolProvider(fn func(context.Context, models.Task) *llmcontracts.RuntimeTools) {
	w.afterCompleteRuntimeToolProvider = fn
}

func (w *WorkerService) CurrentLifecycleCatalog() *agentskills.Catalog {
	if v := w.currentCatalog.Load(); v != nil {
		if c, ok := v.(*agentskills.Catalog); ok {
			return c
		}
	}
	return nil
}

// SetOnTaskComplete registers a callback invoked after every task completion.
// Prefer lifecycle hooks for agent/model-driven post-completion behavior; use
// this only for low-level side effects that should not block the worker pool.
func (w *WorkerService) SetOnTaskComplete(fn func(task models.Task, executionErr error)) {
	w.onTaskComplete = fn
}

// hasGlobalWorkerCapacity reports whether another task may dispatch. A limit of
// zero is the canonical representation for an unlimited global worker pool.
func hasGlobalWorkerCapacity(maxWorkers, running int) bool {
	return maxWorkers <= 0 || running < maxWorkers
}

func NewWorkerService(llmSvc *LLMService, numWorkers int, projectRepo *repository.ProjectRepo) *WorkerService {
	return &WorkerService{
		llmSvc:      llmSvc,
		projectRepo: projectRepo,
		numWorkers:  numWorkers,
		pending:     make(map[string]bool),
		prepared:    make(map[string]preparedAutomationDispatch),
		cancelFuncs: make(map[string]context.CancelFunc),
		submitted:   make(chan models.Task, 100),
	}
}

// SetTaskRepo sets the task repo for checking task status before re-queuing.
// Called after construction to avoid circular dependencies.
func (w *WorkerService) SetTaskRepo(taskRepo *repository.TaskRepo) {
	w.taskRepo = taskRepo
}

// SetProjectRepo sets the project repo for per-project worker limit lookups.
// Called after construction when the project repo isn't available at construction time.
func (w *WorkerService) SetProjectRepo(projectRepo *repository.ProjectRepo) {
	w.projectRepo = projectRepo
}

// SetLLMConfigRepo sets the LLM config repo for per-model worker pool lookups.
func (w *WorkerService) SetLLMConfigRepo(llmConfigRepo *repository.LLMConfigRepo) {
	w.llmConfigRepo = llmConfigRepo
}

func (w *WorkerService) SetUpdateWorkTracker(tracker *update.WorkTracker) { w.updateTracker = tracker }

func (w *WorkerService) SetAdmissionGate(open func() bool) {
	w.mu.Lock()
	w.admissionOpen = open
	w.mu.Unlock()
}

// ResumeDispatch offers all queued durable work for admission after a drain ends.
func (w *WorkerService) ResumeDispatch() { w.dispatchNext() }

func (w *WorkerService) Start(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.ctx, w.cancel = context.WithCancel(ctx)
	applog.Infof("[worker] started with %d max parallel tasks", w.numWorkers)
}

func (w *WorkerService) Stop() {
	w.mu.Lock()
	if w.cancel != nil {
		w.cancel()
	}
	w.mu.Unlock()

	w.wg.Wait()
	applog.Infof("[worker] all tasks stopped")
}

// Resize changes the max number of parallel tasks.
// If capacity increased, queued tasks are dispatched immediately.
func (w *WorkerService) Resize(n int) {
	w.mu.Lock()
	old := w.numWorkers
	w.numWorkers = n
	w.mu.Unlock()

	applog.Infof("[worker] Resize %d -> %d max parallel tasks", old, n)
	if (n <= 0 && old > 0) || n > old {
		w.dispatchNext()
	}
}

// Submit adds a task to the queue and tries to dispatch it.
// If global/project/model capacity is full, the task waits in the queue
// until a slot opens (triggered by task completion or resize).
func (w *WorkerService) Submit(task models.Task) {
	// Chat tasks bypass the worker pool
	if task.Category == models.CategoryChat {
		applog.Infof("[worker] Submit skipping chat task id=%s (chat tasks bypass worker pool)", task.ID)
		return
	}

	w.mu.Lock()
	if w.pending[task.ID] {
		w.mu.Unlock()
		applog.Infof("[worker] Submit skipping duplicate task id=%s title=%q", task.ID, task.Title)
		return
	}
	w.pending[task.ID] = true
	w.queue = append(w.queue, task)
	w.mu.Unlock()

	// Notify test observers (non-blocking)
	select {
	case w.submitted <- task:
	default:
	}

	w.dispatchNext()
}

// SubmitPrepared adapts a durably claimed Automation dispatch into the existing
// worker queue. It owns no execution, capacity, lifecycle, or completion path of
// its own; those remain in WorkerService and LLMService.
func (w *WorkerService) SubmitPrepared(envelope models.AutomationDispatchEnvelope, executionID string) error {
	if envelope.DispatchID == "" || envelope.Task.ID == "" {
		return fmt.Errorf("complete prepared automation dispatch is required")
	}
	if envelope.Task.Category == models.CategoryChat {
		return fmt.Errorf("automation dispatch cannot target a chat task")
	}
	w.mu.Lock()
	if w.pending[envelope.Task.ID] {
		w.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrTaskAlreadyQueuedOrRunning, envelope.Task.ID)
	}
	w.pending[envelope.Task.ID] = true
	w.prepared[envelope.Task.ID] = preparedAutomationDispatch{Envelope: envelope, ExecutionID: executionID}
	w.queue = append(w.queue, envelope.Task)
	w.mu.Unlock()
	select {
	case w.submitted <- envelope.Task:
	default:
	}
	w.dispatchNext()
	return nil
}

// dispatchNext scans the queue FIFO and dispatches tasks that have available
// global, project, and model slots. Called after Submit, task completion, and Resize.
func (w *WorkerService) dispatchNext() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.ctx == nil {
		return // not started yet
	}
	if w.admissionOpen != nil && !w.admissionOpen() {
		return
	}

	i := 0
	for i < len(w.queue) {
		// Check global capacity
		running := int(atomic.LoadInt32(&w.totalRunning))
		if !hasGlobalWorkerCapacity(w.numWorkers, running) {
			return // globally at capacity, nothing more to dispatch
		}

		task := w.queue[i]
		prepared, isPrepared := w.prepared[task.ID]

		// Prune stale tasks (status/category changed while queued)
		if w.taskRepo != nil {
			dbTask, err := w.taskRepo.GetByID(context.Background(), task.ID)
			validStatus := dbTask != nil && dbTask.Status == models.StatusPending
			if isPrepared && prepared.ExecutionID != "" {
				validStatus = dbTask != nil && dbTask.Status == models.StatusRunning
			}
			if err != nil || dbTask == nil || !validStatus ||
				(dbTask.Category != models.CategoryActive && dbTask.Category != models.CategoryScheduled) {
				w.queue = append(w.queue[:i], w.queue[i+1:]...)
				if isPrepared && prepared.ExecutionID == "" && w.automationRepo != nil {
					if abandonErr := w.automationRepo.AbandonQueuedDispatch(context.Background(), prepared.Envelope.DispatchID, "Automation task was cancelled or is no longer runnable"); abandonErr != nil {
						applog.Infof("[worker] abandoning queued automation dispatch=%s failed: %v", prepared.Envelope.DispatchID, abandonErr)
					}
				}
				delete(w.pending, task.ID)
				delete(w.prepared, task.ID)
				applog.Infof("[worker] pruned stale task=%s %q from queue", task.ID, task.Title)
				continue
			}
			if !isPrepared || prepared.ExecutionID == "" {
				// Queued submissions are admission hints keyed by Task ID. The
				// persisted Task is authoritative at dispatch time so queued work
				// cannot run replaced prompt, Agent, or topology data. The scheduled
				// occurrence's context boundary is runtime-only, so preserve it for
				// ordinary submissions across the persisted task refresh.
				startsNewContext := task.StartsNewContext
				task = *dbTask
				if !isPrepared {
					task.StartsNewContext = startsNewContext
				}
			}

			// Dependency gating: chained tasks must wait for parent to reach terminal state.
			// Swarm child tasks are grouped under an active parent and run independently.
			if dbTask.ParentTaskID != nil && *dbTask.ParentTaskID != "" && !models.IsSwarmChildRole(dbTask.SwarmRole) {
				parentTask, parentErr := w.taskRepo.GetByID(context.Background(), *dbTask.ParentTaskID)
				if parentErr == nil && parentTask != nil && !models.IsTerminalStatus(parentTask.Status) {
					applog.Infof("[worker] dependency gate: task=%s %q waiting on parent=%s (status=%s)",
						task.ID, task.Title, *dbTask.ParentTaskID, parentTask.Status)
					i++ // skip, re-check on next dispatch loop
					continue
				}
			}
		}

		// Check project capacity
		if !w.tryAcquireProjectSlot(task.ProjectID) {
			i++ // skip this task, try next one
			continue
		}

		// Check model capacity
		agentConfigID := w.resolveAgentConfigID(w.ctx, task)
		if !w.tryAcquireModelSlot(agentConfigID) {
			w.releaseProjectSlot(task.ProjectID)
			i++ // skip this task, try next one
			continue
		}

		// Reserve update active-work accounting synchronously before launching the
		// goroutine so a concurrently starting drain cannot observe a false zero.
		workDone := func() {}
		if w.updateTracker != nil {
			var err error
			workDone, err = w.updateTracker.Start(update.WorkTask)
			if err != nil {
				w.releaseProjectSlot(task.ProjectID)
				w.releaseModelSlot(agentConfigID)
				return
			}
		}

		// Remove from queue (shift remaining)
		w.queue = append(w.queue[:i], w.queue[i+1:]...)
		delete(w.prepared, task.ID)

		// Dispatch
		w.wg.Add(1)
		go w.executeTask(task, agentConfigID, prepared, isPrepared, workDone)
	}
}

func (w *WorkerService) executeTask(task models.Task, agentConfigID string, prepared preparedAutomationDispatch, isPrepared bool, workDone func()) {
	defer w.wg.Done()
	defer workDone()

	applog.Infof("[worker] executing task=%s %q (project: %s, model: %s)", task.ID, task.Title, task.ProjectID, agentConfigID)

	taskCtx, taskCancel := context.WithCancel(w.ctx)
	if isPrepared {
		taskCtx = WithAutomationContext(taskCtx, prepared.Envelope.Context)
		if prepared.ExecutionID != "" {
			taskCtx = withPreparedAutomationExecution(taskCtx, prepared.ExecutionID)
			taskCtx = withTaskPreClaimed(taskCtx)
		}
	}
	w.RegisterCancel(task.ID, taskCancel)

	var executionErr error
	var preparedTerminalStatus models.ExecutionStatus
	var preparedTerminalMessage string
	claimed := w.taskRepo == nil || (isPrepared && prepared.ExecutionID != "")
	completionAttempted := w.taskRepo == nil || (isPrepared && prepared.ExecutionID != "")
	logOutcome := true

	defer func() {
		if r := recover(); r != nil {
			executionErr = fmt.Errorf("panic: %v", r)
			completionAttempted = true
			applog.Infof("[worker] task panic task=%s %q: %v", task.ID, task.Title, r)
			if claimed {
				w.failRunningExecutionsAfterPanic(context.Background(), task.ID, executionErr)
			}
			if claimed && w.taskRepo != nil {
				current, getErr := w.taskRepo.GetByID(context.Background(), task.ID)
				if getErr != nil {
					applog.Infof("[worker] task=%s status lookup after panic failed: %v", task.ID, getErr)
				} else if current != nil && current.Status == models.StatusRunning {
					if updateErr := w.taskRepo.UpdateStatus(context.Background(), task.ID, models.StatusFailed); updateErr != nil {
						applog.Infof("[worker] task=%s failed status update after panic: %v", task.ID, updateErr)
					}
				}
			}
		}

		wasCancelled := errors.Is(taskCtx.Err(), context.Canceled)
		w.DeregisterCancel(task.ID)
		taskCancel()

		// Remove from pending AFTER execution so scheduler doesn't re-submit during execution.
		w.mu.Lock()
		delete(w.pending, task.ID)
		w.mu.Unlock()

		// Release slots unconditionally for every path after dispatch acquired them.
		w.releaseProjectSlot(task.ProjectID)
		w.releaseModelSlot(agentConfigID)

		if isPrepared && prepared.ExecutionID != "" && w.automationRepo != nil {
			status := preparedTerminalStatus
			message := preparedTerminalMessage
			if status == "" {
				status = models.ExecFailed
				message = "automation execution did not reach a terminal state"
				if executionErr != nil {
					message = executionErr.Error()
				}
				if wasCancelled {
					status = models.ExecCancelled
					message = "automation task cancelled during execution setup"
				}
				execRepo := w.execRepo
				if execRepo == nil && w.llmSvc != nil {
					execRepo = w.llmSvc.execRepo
				}
				if execRepo != nil {
					if current, err := execRepo.GetByID(context.Background(), prepared.ExecutionID); err == nil && current != nil {
						switch current.Status {
						case models.ExecCompleted, models.ExecFailed, models.ExecCancelled:
							status = current.Status
							message = current.ErrorMessage
						}
					}
				}
			}
			if err := w.automationRepo.CompleteDispatch(context.Background(), prepared.Envelope.DispatchID, prepared.ExecutionID, status, message); err != nil {
				applog.Infof("[worker] automation dispatch completion failed dispatch=%s execution=%s: %v", prepared.Envelope.DispatchID, prepared.ExecutionID, err)
			}
		}

		if logOutcome {
			if executionErr != nil {
				applog.Infof("[worker] task failed task=%s %q: %v", task.ID, task.Title, executionErr)
			} else {
				applog.Infof("[worker] task completed task=%s %q", task.ID, task.Title)
			}
		}

		if completionAttempted && w.onTaskComplete != nil {
			// Run the post-completion callback in a goroutine so heavy side
			// effects never block worker dispatch.
			go func(t models.Task, runErr error) {
				defer func() {
					if r := recover(); r != nil {
						applog.Infof("[worker] onTaskComplete panic for task=%s: %v", t.ID, r)
					}
				}()
				w.onTaskComplete(t, runErr)
			}(task, executionErr)
		}

		// Task finished, slot freed — dispatch next queued task.
		w.dispatchNext()
	}()

	// Claim the task BEFORE running lifecycle hooks so the kanban board
	// reflects the task as "running" while route_task/before_run hooks
	// execute. Without this pre-claim the task would stay visually stuck in
	// the queued sub-zone of the active dropzone during early lifecycle
	// hooks (which may call the LLM and take noticeable time). The atomic
	// dispatch claim publishes a TaskStatusChanged event so Tasks updates live,
	// while returning the exact Task/current-graph state admitted by that claim.
	// If claim fails (task already running/completed/cancelled), skip — the
	// task either was already promoted or shouldn't run.
	if isPrepared && prepared.ExecutionID == "" {
		if w.beforeQueuedAutomationTaskClaim != nil {
			w.beforeQueuedAutomationTaskClaim(task)
		}
		claim, claimErr := w.taskRepo.ClaimQueuedAutomationDispatch(taskCtx, prepared.Envelope.DispatchID)
		if claimErr != nil {
			applog.Infof("[worker] queued automation task=%s claim failed: %v", task.ID, claimErr)
			logOutcome = false
			return
		}
		prepared.ExecutionID = claim.Execution.ID
		claimed = true
		completionAttempted = true
		task = claim.Task
		taskCtx = withPreparedAutomationExecution(taskCtx, claim.Execution.ID)
		taskCtx = withTaskPreClaimed(taskCtx)

		// Capacity was reserved from the queue refresh. If the Task assignment
		// changed before the atomic claim, transfer the model reservation before
		// lifecycle hooks or provider execution use the admitted Task.
		claimedAgentConfigID := w.resolveAgentConfigID(taskCtx, task)
		if claimedAgentConfigID != agentConfigID {
			w.releaseModelSlot(agentConfigID)
			agentConfigID = ""
			if err := w.AcquireModelSlot(taskCtx, claimedAgentConfigID); err != nil {
				executionErr = fmt.Errorf("acquiring claimed automation task model capacity: %w", err)
				preparedTerminalStatus = models.ExecFailed
				preparedTerminalMessage = executionErr.Error()
				if errors.Is(err, context.Canceled) {
					preparedTerminalStatus = models.ExecCancelled
					preparedTerminalMessage = "automation task cancelled during model capacity transfer"
				}
				return
			}
			agentConfigID = claimedAgentConfigID
		}
	}
	if w.taskRepo != nil && !isPrepared {
		if w.beforeOrdinaryTaskClaim != nil {
			w.beforeOrdinaryTaskClaim(task)
		}
		dispatchClaim, claimedTask, claimErr := w.taskRepo.ClaimTaskForDispatch(taskCtx, task.ID)
		if claimErr != nil {
			applog.Infof("[worker] task=%s claim failed: %v", task.ID, claimErr)
			logOutcome = false
			return
		}
		if !claimedTask {
			applog.Infof("[worker] task=%s not admitted at dispatch (already running, terminal, or no longer runnable), skipping", task.ID)
			logOutcome = false
			return
		}
		claimed = true
		completionAttempted = true
		if w.afterOrdinaryTaskClaim != nil {
			w.afterOrdinaryTaskClaim(dispatchClaim.Task)
		}
		startsNewContext := task.StartsNewContext
		task = dispatchClaim.Task
		task.StartsNewContext = startsNewContext
		if len(dispatchClaim.AutomationContext.Bindings) > 0 || dispatchClaim.AutomationContext.OriginTask {
			taskCtx = WithAutomationContext(taskCtx, dispatchClaim.AutomationContext)
		}
		// Capacity was reserved from the preliminary queue snapshot. If Save
		// changed the selected Agent before the atomic claim, transfer that
		// reservation before executing the authoritative Task.
		claimedAgentConfigID := w.resolveAgentConfigID(taskCtx, task)
		if claimedAgentConfigID != agentConfigID {
			w.releaseModelSlot(agentConfigID)
			agentConfigID = ""
			if err := w.AcquireModelSlot(taskCtx, claimedAgentConfigID); err != nil {
				executionErr = fmt.Errorf("acquiring claimed task model capacity: %w", err)
				if updateErr := w.taskRepo.UpdateStatus(context.Background(), task.ID, models.StatusFailed); updateErr != nil {
					applog.Infof("[worker] task=%s failed status update after model capacity error: %v", task.ID, updateErr)
				}
				return
			}
			agentConfigID = claimedAgentConfigID
		}
		// Tag the context so executeTaskWithAgent knows the task has
		// already been claimed and won't skip it as "already running".
		taskCtx = withTaskPreClaimed(taskCtx)
	} else if isPrepared {
		task.Status = models.StatusRunning
	}

	turn := w.PrepareLifecycleTurn(taskCtx, task)
	taskCtx = turn.Ctx

	var chatContext llmcontracts.ChatContext
	_, chatContext, executionErr = w.llmSvc.executeTaskWithChatContext(taskCtx, task)

	turn.AfterComplete(executionErr, chatContext)
}

func (w *WorkerService) failRunningExecutionsAfterPanic(ctx context.Context, taskID string, panicErr error) {
	execRepo := w.execRepo
	if execRepo == nil && w.llmSvc != nil {
		execRepo = w.llmSvc.execRepo
	}
	if execRepo == nil {
		return
	}
	execs, err := execRepo.ListByTask(ctx, taskID)
	if err != nil {
		applog.Infof("[worker] task=%s execution lookup after panic failed: %v", taskID, err)
		return
	}
	errMsg := "panic during task execution"
	if panicErr != nil {
		errMsg = panicErr.Error()
	}
	now := time.Now()
	for _, exec := range execs {
		if exec.Status != models.ExecRunning {
			continue
		}
		durationMs := now.Sub(exec.StartedAt).Milliseconds()
		if durationMs < 0 {
			durationMs = 0
		}
		if completeErr := execRepo.Complete(ctx, exec.ID, models.ExecFailed, "", errMsg, exec.TokensUsed, durationMs); completeErr != nil {
			applog.Infof("[worker] task=%s execution=%s failed completion after panic failed: %v", taskID, exec.ID, completeErr)
		}
	}
}

// runLifecycleSlot dispatches the lifecycle runner for the supplied slot if
// one is configured. Errors are logged; they never affect task status. The
// task struct is mapped into a HookInput shape the runner can consume.
func (w *WorkerService) runLifecycleSlot(ctx context.Context, when models.LifecycleWhen, task models.Task, taskRunID string, runErr error, chatContext llmcontracts.ChatContext) lifecycle.SlotResult {
	return w.runLifecycleSlotFiltered(ctx, when, task, taskRunID, runErr, chatContext, nil)
}

func (w *WorkerService) runLifecycleSlotFiltered(ctx context.Context, when models.LifecycleWhen, task models.Task, taskRunID string, runErr error, chatContext llmcontracts.ChatContext, include func(models.AgentLifecycleHook) bool) lifecycle.SlotResult {
	return w.runLifecycleSlotWithExtras(ctx, when, task, taskRunID, runErr, chatContext, nil, include)
}

func (w *WorkerService) runLifecycleSlotWithExtras(ctx context.Context, when models.LifecycleWhen, task models.Task, taskRunID string, runErr error, chatContext llmcontracts.ChatContext, extras map[string]any, include func(models.AgentLifecycleHook) bool) lifecycle.SlotResult {
	if w.lifecycleRunner == nil {
		return lifecycle.SlotResult{When: when}
	}
	if taskRunID == "" {
		taskRunID = newLifecycleTaskRunID(task.ID)
	}
	baseExtras := copyExtras(extras)
	turn := lifecycleTurnFromContext(ctx)
	taskPrompt := task.Prompt
	if turn.TaskThreadTurn && turn.TurnPrompt != "" {
		taskPrompt = turn.TurnPrompt
		baseExtras = withHookExtra(baseExtras, "original_task_prompt", task.Prompt)
	}
	in := lifecycle.HookInput{
		When:       when,
		TaskID:     task.ID,
		TaskRunID:  taskRunID,
		ProjectID:  task.ProjectID,
		TaskTitle:  promptSafeTaskTitle(task),
		TaskPrompt: taskPrompt,
		WorkDir:    projectRepoPath(ctx, w.projectRepo, task.ProjectID),
		Extras:     baseExtras,
	}
	if task.AgentDefinitionID != nil {
		in.ActiveModeAgent = *task.AgentDefinitionID
	}
	if runErr != nil {
		if in.Extras == nil {
			in.Extras = make(map[string]any)
		}
		in.Extras[lifecycle.ExecutionErrorKey] = runErr.Error()
	}
	if when == models.LifecycleAfterComplete {
		if in.Extras == nil {
			in.Extras = make(map[string]any)
		}
		snapshot := w.buildLearningSnapshot(ctx, task, taskRunID, runErr)
		in.Extras[lifecycle.ConversationTranscriptKey] = chatContext
		in.Extras[lifecycle.LearningSnapshotKey] = snapshot
		in.Extras[lifecycle.AssignedAgentKey] = assignedAgentIdentity(snapshot)
		if goal := w.evaluableTaskGoal(ctx, task.ID); goal != nil {
			in.Extras[lifecycle.TaskGoalKey] = goal
		}
	}
	result, err := w.lifecycleRunner.RunSlotFiltered(ctx, when, in, include)
	if err != nil {
		applog.Infof("[worker] lifecycle %s failed for task=%s: %v", when, task.ID, err)
		return lifecycle.SlotResult{When: when}
	}
	return result
}

func promptSafeTaskTitle(task models.Task) string {
	if task.Category == models.CategoryScheduled {
		switch task.Title {
		case agentLibraryMaintenanceTaskTitle:
			return "Skill Library Maintenance"
		case memoryConsolidationTaskTitle:
			return "Memory Consolidation"
		}
	}
	return task.Title
}

func (w *WorkerService) lifecycleHookInput(ctx context.Context, hook models.AgentLifecycleHook, input lifecycle.HookInput) lifecycle.HookInput {
	switch hook.When {
	case models.LifecycleRouteTask:
		return w.routeHookInput(ctx, hook, input)
	case models.LifecycleAfterComplete:
		return w.afterCompleteHookInput(ctx, hook, input)
	default:
		return input
	}
}

func (w *WorkerService) routeHookInput(ctx context.Context, hook models.AgentLifecycleHook, input lifecycle.HookInput) lifecycle.HookInput {
	input.Extras = withoutRouteIndexes(input.Extras)
	switch hook.OutputContract {
	case models.OutputContractSelectedSkills:
		if turn := lifecycleTurnFromContext(ctx); turn.SkillIndex != "" {
			input.Extras = withHookExtra(input.Extras, "available_skills", turn.SkillIndex)
		}
	case models.OutputContractSelectedMemories:
		input.Extras = withHookExtra(input.Extras, "available_memories", w.availableMemoryIndex(ctx, models.Task{ID: input.TaskID, ProjectID: input.ProjectID}))
	}
	return input
}

// afterCompleteHookInput narrows the shared after-complete extras to the
// context blocks the hook declared in its agent declaration. The slot builds
// one payload for every hook, so without this each hook pays for blocks its
// skill never reads.
//
// A hook that declares no payload receives everything, which is the default
// for user-created agents and for any declaration written before payload
// selection existed. Blocks reporting the outcome of the turn itself are
// delivered even when undeclared (see lifecycle.AlwaysDelivered), so a hook
// can never mistake a failed execution for a successful one.
func (w *WorkerService) afterCompleteHookInput(_ context.Context, hook models.AgentLifecycleHook, input lifecycle.HookInput) lifecycle.HookInput {
	payload := lifecycle.ParseHookPayload(hook.PayloadJSON)
	if payload.SelectsAllBlocks() || len(input.Extras) == 0 {
		return input
	}
	extras := make(map[string]any, len(payload.Blocks))
	for key, value := range input.Extras {
		if payload.Allows(key) {
			extras[key] = value
		}
	}
	input.Extras = extras
	return input
}

func copyExtras(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func withoutRouteIndexes(in map[string]any) map[string]any {
	out := copyExtras(in)
	if out == nil {
		return nil
	}
	delete(out, "available_skills")
	delete(out, "available_memories")
	return out
}

func withHookExtra(extras map[string]any, key string, value any) map[string]any {
	if extras == nil {
		extras = make(map[string]any)
	}
	extras[key] = value
	return extras
}

func (w *WorkerService) availableMemoryIndex(ctx context.Context, task models.Task) string {
	if w == nil || w.projectRepo == nil || task.ProjectID == "" {
		return ""
	}
	repoPath := projectRepoPath(ctx, w.projectRepo, task.ProjectID)
	if repoPath == "" {
		return ""
	}
	path := filepath.Join(repoPath, ".openvibely", memory.MemoryDirName, memory.IndexFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			applog.Infof("[worker] read memory index failed task=%s path=%s: %v", task.ID, path, err)
		}
		return ""
	}
	return string(data)
}

func (w *WorkerService) QueueSize() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.queue)
}

func (w *WorkerService) NumWorkers() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.numWorkers
}

// TotalRunning returns the number of tasks currently being executed across all projects.
func (w *WorkerService) TotalRunning() int {
	return int(atomic.LoadInt32(&w.totalRunning))
}

// ProjectRunning returns the number of tasks currently being executed for a specific project.
func (w *WorkerService) ProjectRunning(projectID string) int {
	if counterI, ok := w.projectRunning.Load(projectID); ok {
		counter := counterI.(*int32)
		return int(atomic.LoadInt32(counter))
	}
	return 0
}

// RegisterCancel stores a cancel function for a running task so it can be
// cancelled later via CancelRunningTask. This is also used by chat tasks
// that bypass the worker pool.
func (w *WorkerService) RegisterCancel(taskID string, cancel context.CancelFunc) {
	w.cancelMu.Lock()
	w.cancelFuncs[taskID] = cancel
	w.cancelMu.Unlock()
}

// DeregisterCancel removes the cancel function for a task after it completes.
func (w *WorkerService) DeregisterCancel(taskID string) {
	w.cancelMu.Lock()
	delete(w.cancelFuncs, taskID)
	w.cancelMu.Unlock()
}

// CancelRunningTask cancels the context for a running task, which kills the
// CLI process via exec.CommandContext. Returns true if the task was found and cancelled.
func (w *WorkerService) CancelRunningTask(taskID string) bool {
	w.cancelMu.Lock()
	cancel, ok := w.cancelFuncs[taskID]
	if ok {
		delete(w.cancelFuncs, taskID)
	}
	w.cancelMu.Unlock()

	if ok {
		applog.Infof("[worker] CancelRunningTask killing task=%s", taskID)
		cancel()
		return true
	}
	applog.Infof("[worker] CancelRunningTask task=%s not found in running tasks", taskID)
	return false
}

// tryAcquireProjectSlot attempts to acquire a concurrency slot for the given project.
// Returns true if the task can execute, false if the project's max_workers limit is reached.
func (w *WorkerService) tryAcquireProjectSlot(projectID string) bool {
	maxWorkers := w.getProjectMaxWorkers(projectID)

	// Get or create counter for this project
	actual, _ := w.projectRunning.LoadOrStore(projectID, new(int32))
	counter := actual.(*int32)

	if maxWorkers > 0 {
		for {
			current := atomic.LoadInt32(counter)
			if int(current) >= maxWorkers {
				return false
			}
			if atomic.CompareAndSwapInt32(counter, current, current+1) {
				atomic.AddInt32(&w.totalRunning, 1)
				return true
			}
		}
	}

	// No per-project limit, just track the counter
	atomic.AddInt32(counter, 1)
	atomic.AddInt32(&w.totalRunning, 1)
	return true
}

// releaseProjectSlot releases a concurrency slot after task completion.
func (w *WorkerService) releaseProjectSlot(projectID string) {
	if counterI, ok := w.projectRunning.Load(projectID); ok {
		counter := counterI.(*int32)
		atomic.AddInt32(counter, -1)
	}
	atomic.AddInt32(&w.totalRunning, -1)
}

// getProjectMaxWorkers returns the max_workers setting for a project.
// Returns 0 if no limit is set (nil max_workers).
func (w *WorkerService) getProjectMaxWorkers(projectID string) int {
	if w.projectRepo == nil {
		return 0
	}
	project, err := w.projectRepo.GetByID(context.Background(), projectID)
	if err != nil || project == nil {
		return 0
	}
	if project.MaxWorkers == nil {
		return 0
	}
	return *project.MaxWorkers
}

// resolveAgentConfigID determines which agent config ID will be used for a task.
// It mirrors the resolution logic in LLMService.ExecuteTask.
func (w *WorkerService) resolveAgentConfigID(ctx context.Context, task models.Task) string {
	// Priority 1: Task's assigned agent
	if task.AgentID != nil && *task.AgentID != "" {
		if w.llmConfigRepo != nil {
			agent, err := w.llmConfigRepo.GetByID(ctx, *task.AgentID)
			if err == nil && agent != nil {
				return agent.ID
			}
		}
	}
	// Priority 2: Project default agent
	if task.ProjectID != "" && w.projectRepo != nil {
		project, err := w.projectRepo.GetByID(ctx, task.ProjectID)
		if err == nil && project != nil && project.DefaultAgentConfigID != nil && *project.DefaultAgentConfigID != "" {
			return *project.DefaultAgentConfigID
		}
	}
	// Priority 3: Global default agent
	if w.llmConfigRepo != nil {
		agent, err := w.llmConfigRepo.GetDefault(ctx)
		if err == nil && agent != nil {
			return agent.ID
		}
	}
	return ""
}

// tryAcquireModelSlot attempts to acquire a concurrency slot for the given model config.
// Returns true if the task can execute, false if the model's max_workers limit is reached.
func (w *WorkerService) tryAcquireModelSlot(agentConfigID string) bool {
	if agentConfigID == "" {
		return true
	}
	maxWorkers := w.getModelMaxWorkers(agentConfigID)
	if maxWorkers <= 0 {
		// No per-model limit, just track the counter
		actual, _ := w.modelRunning.LoadOrStore(agentConfigID, new(int32))
		counter := actual.(*int32)
		atomic.AddInt32(counter, 1)
		return true
	}

	actual, _ := w.modelRunning.LoadOrStore(agentConfigID, new(int32))
	counter := actual.(*int32)

	for {
		current := atomic.LoadInt32(counter)
		if int(current) >= maxWorkers {
			return false
		}
		if atomic.CompareAndSwapInt32(counter, current, current+1) {
			return true
		}
	}
}

// releaseModelSlot releases a concurrency slot after task completion.
func (w *WorkerService) releaseModelSlot(agentConfigID string) {
	if agentConfigID == "" {
		return
	}
	if counterI, ok := w.modelRunning.Load(agentConfigID); ok {
		counter := counterI.(*int32)
		atomic.AddInt32(counter, -1)
	}
}

// getModelMaxWorkers returns the max_workers setting for a model config.
// Returns 0 if no limit is set.
func (w *WorkerService) getModelMaxWorkers(agentConfigID string) int {
	if w.llmConfigRepo == nil || agentConfigID == "" {
		return 0
	}
	agent, err := w.llmConfigRepo.GetByID(context.Background(), agentConfigID)
	if err != nil || agent == nil {
		return 0
	}
	return agent.MaxWorkers
}

// AcquireModelSlot blocks until a per-model concurrency slot is available or
// the context is cancelled. Used by chat-triggered task executions.
func (w *WorkerService) AcquireModelSlot(ctx context.Context, agentConfigID string) error {
	for {
		if w.tryAcquireModelSlot(agentConfigID) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// ReleaseModelSlot releases a per-model concurrency slot after task completion.
// Public wrapper for releaseModelSlot, used by chat-triggered task executions.
func (w *WorkerService) ReleaseModelSlot(agentConfigID string) {
	w.releaseModelSlot(agentConfigID)
}

// TryAcquireModelSlot attempts to acquire a per-model concurrency slot.
// Returns true if the slot was acquired, false if the model's max_workers limit is reached.
// Used by tests to simulate model capacity being full.
func (w *WorkerService) TryAcquireModelSlot(agentConfigID string) bool {
	return w.tryAcquireModelSlot(agentConfigID)
}

// tryAcquireGlobalProjectSlot atomically reserves both global and project
// capacity for execution paths outside dispatchNext, such as task-thread
// follow-ups. dispatchNext already holds w.mu while checking global capacity and
// acquiring its project slot, so it continues to call tryAcquireProjectSlot
// directly.
func (w *WorkerService) tryAcquireGlobalProjectSlot(projectID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.admissionOpen != nil && !w.admissionOpen() {
		return false
	}

	running := int(atomic.LoadInt32(&w.totalRunning))
	if !hasGlobalWorkerCapacity(w.numWorkers, running) {
		return false
	}
	return w.tryAcquireProjectSlot(projectID)
}

// TryAcquireProjectSlot attempts to acquire global and per-project concurrency
// capacity. Used by task execution paths that bypass the worker queue.
func (w *WorkerService) TryAcquireProjectSlot(projectID string) bool {
	return w.tryAcquireGlobalProjectSlot(projectID)
}

// AcquireProjectSlot blocks until global and per-project concurrency capacity
// is available or the context is cancelled. Used by task thread follow-ups that
// queue when workers are at capacity instead of failing fast.
func (w *WorkerService) AcquireProjectSlot(ctx context.Context, projectID string) error {
	for {
		if w.tryAcquireGlobalProjectSlot(projectID) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// ReleaseProjectSlot releases a per-project concurrency slot after task completion.
// Used by chat-triggered task executions that bypass the worker pool.
func (w *WorkerService) ReleaseProjectSlot(projectID string) {
	w.releaseProjectSlot(projectID)
}

// HasProjectCapacity returns true if the project has room for another worker,
// or if no per-project limit is configured. This is a read-only check (does not
// acquire a slot) for early rejection in handlers.
func (w *WorkerService) HasProjectCapacity(projectID string) bool {
	maxWorkers := w.getProjectMaxWorkers(projectID)
	if maxWorkers <= 0 {
		return true
	}
	return w.ProjectRunning(projectID) < maxWorkers
}

// HasModelCapacity returns true if the model has room for another worker,
// or if no per-model limit is configured. This is a read-only check (does not
// acquire a slot) for early rejection in handlers.
func (w *WorkerService) HasModelCapacity(agentConfigID string) bool {
	if agentConfigID == "" {
		return true
	}
	maxWorkers := w.getModelMaxWorkers(agentConfigID)
	if maxWorkers <= 0 {
		return true
	}
	return w.ModelRunning(agentConfigID) < maxWorkers
}

// GetModelWorkerTimeout returns the worker_timeout setting for a model config.
// Returns 0 if no timeout is configured or the config can't be found.
func (w *WorkerService) GetModelWorkerTimeout(agentConfigID string) time.Duration {
	if w.llmConfigRepo == nil || agentConfigID == "" {
		return 0
	}
	agent, err := w.llmConfigRepo.GetByID(context.Background(), agentConfigID)
	if err != nil || agent == nil {
		return 0
	}
	if agent.WorkerTimeout <= 0 {
		return 0
	}
	return time.Duration(agent.WorkerTimeout) * time.Second
}

// Submitted returns a channel that receives tasks as they are submitted.
// Used by tests to verify task submissions.
func (w *WorkerService) Submitted() <-chan models.Task {
	return w.submitted
}

// DispatchNext triggers a dispatch check on the worker queue.
// Called after external slot releases (e.g., thread follow-up completion)
// to promote queued tasks that were blocked by capacity.
func (w *WorkerService) DispatchNext() {
	w.dispatchNext()
}

// ModelRunning returns the number of tasks currently being executed for a specific model.
func (w *WorkerService) ModelRunning(agentConfigID string) int {
	if counterI, ok := w.modelRunning.Load(agentConfigID); ok {
		counter := counterI.(*int32)
		return int(atomic.LoadInt32(counter))
	}
	return 0
}

package service

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type WorkerService struct {
	llmSvc        *LLMService
	projectRepo   *repository.ProjectRepo
	taskRepo      *repository.TaskRepo
	llmConfigRepo *repository.LLMConfigRepo

	mu         sync.Mutex
	numWorkers int            // max parallel tasks (global limit)
	queue      []models.Task  // FIFO task queue
	pending    map[string]bool // task IDs in queue or running (dedup)
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
}

func NewWorkerService(llmSvc *LLMService, numWorkers int, projectRepo *repository.ProjectRepo) *WorkerService {
	return &WorkerService{
		llmSvc:      llmSvc,
		projectRepo: projectRepo,
		numWorkers:  numWorkers,
		pending:     make(map[string]bool),
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

func (w *WorkerService) Start(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.ctx, w.cancel = context.WithCancel(ctx)
	log.Printf("[worker] started with %d max parallel tasks", w.numWorkers)
}

func (w *WorkerService) Stop() {
	w.mu.Lock()
	if w.cancel != nil {
		w.cancel()
	}
	w.mu.Unlock()

	w.wg.Wait()
	log.Println("[worker] all tasks stopped")
}

// Resize changes the max number of parallel tasks.
// If capacity increased, queued tasks are dispatched immediately.
func (w *WorkerService) Resize(n int) {
	w.mu.Lock()
	old := w.numWorkers
	w.numWorkers = n
	w.mu.Unlock()

	log.Printf("[worker] Resize %d -> %d max parallel tasks", old, n)
	if n > old {
		w.dispatchNext()
	}
}

// Submit adds a task to the queue and tries to dispatch it.
// If global/project/model capacity is full, the task waits in the queue
// until a slot opens (triggered by task completion or resize).
func (w *WorkerService) Submit(task models.Task) {
	// Chat tasks bypass the worker pool
	if task.Category == models.CategoryChat {
		log.Printf("[worker] Submit skipping chat task id=%s (chat tasks bypass worker pool)", task.ID)
		return
	}

	w.mu.Lock()
	if w.pending[task.ID] {
		w.mu.Unlock()
		log.Printf("[worker] Submit skipping duplicate task id=%s title=%q", task.ID, task.Title)
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

// dispatchNext scans the queue FIFO and dispatches tasks that have available
// global, project, and model slots. Called after Submit, task completion, and Resize.
func (w *WorkerService) dispatchNext() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.ctx == nil {
		return // not started yet
	}

	i := 0
	for i < len(w.queue) {
		// Check global capacity
		running := int(atomic.LoadInt32(&w.totalRunning))
		if running >= w.numWorkers {
			return // globally at capacity, nothing more to dispatch
		}

		task := w.queue[i]

		// Prune stale tasks (status/category changed while queued)
		if w.taskRepo != nil {
			dbTask, err := w.taskRepo.GetByID(context.Background(), task.ID)
			if err != nil || dbTask == nil ||
				dbTask.Status != models.StatusPending ||
				(dbTask.Category != models.CategoryActive && dbTask.Category != models.CategoryScheduled) {
				w.queue = append(w.queue[:i], w.queue[i+1:]...)
				delete(w.pending, task.ID)
				log.Printf("[worker] pruned stale task=%s %q from queue", task.ID, task.Title)
				continue
			}

			// Dependency gating: chained tasks must wait for parent to reach terminal state
			if dbTask.ParentTaskID != nil && *dbTask.ParentTaskID != "" {
				parentTask, parentErr := w.taskRepo.GetByID(context.Background(), *dbTask.ParentTaskID)
				if parentErr == nil && parentTask != nil && !models.IsTerminalStatus(parentTask.Status) {
					log.Printf("[worker] dependency gate: task=%s %q waiting on parent=%s (status=%s)",
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

		// Remove from queue (shift remaining)
		w.queue = append(w.queue[:i], w.queue[i+1:]...)

		// Dispatch
		w.wg.Add(1)
		go w.executeTask(task, agentConfigID)
	}
}

func (w *WorkerService) executeTask(task models.Task, agentConfigID string) {
	defer w.wg.Done()

	log.Printf("[worker] executing task=%s %q (project: %s, model: %s)", task.ID, task.Title, task.ProjectID, agentConfigID)

	taskCtx, taskCancel := context.WithCancel(w.ctx)
	w.RegisterCancel(task.ID, taskCancel)

	_, err := w.llmSvc.ExecuteTask(taskCtx, task)

	w.DeregisterCancel(task.ID)
	taskCancel()

	// Remove from pending AFTER execution so scheduler doesn't re-submit during execution
	w.mu.Lock()
	delete(w.pending, task.ID)
	w.mu.Unlock()

	// Release slots
	w.releaseProjectSlot(task.ProjectID)
	w.releaseModelSlot(agentConfigID)

	if err != nil {
		log.Printf("[worker] task failed task=%s %q: %v", task.ID, task.Title, err)
	} else {
		log.Printf("[worker] task completed task=%s %q", task.ID, task.Title)
	}

	// Task finished, slot freed — dispatch next queued task
	w.dispatchNext()
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
		log.Printf("[worker] CancelRunningTask killing task=%s", taskID)
		cancel()
		return true
	}
	log.Printf("[worker] CancelRunningTask task=%s not found in running tasks", taskID)
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
// The caller must provide a context with a deadline/timeout to prevent
// indefinite polling.
func (w *WorkerService) AcquireModelSlot(ctx context.Context, agentConfigID string) error {
	if _, ok := ctx.Deadline(); !ok {
		// Safety: enforce a max wait time if the caller didn't set a deadline,
		// to prevent unbounded CPU-burning poll loops.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
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

// TryAcquireProjectSlot attempts to acquire a per-project concurrency slot.
// Returns true if the slot was acquired, false if the project's max_workers limit is reached.
// Used by chat-triggered task executions that bypass the worker pool.
func (w *WorkerService) TryAcquireProjectSlot(projectID string) bool {
	return w.tryAcquireProjectSlot(projectID)
}

// AcquireProjectSlot blocks until a per-project concurrency slot is available or
// the context is cancelled. Used by task thread follow-ups that queue when workers
// are at capacity instead of failing fast.
// The caller must provide a context with a deadline/timeout to prevent
// indefinite polling.
func (w *WorkerService) AcquireProjectSlot(ctx context.Context, projectID string) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	for {
		if w.tryAcquireProjectSlot(projectID) {
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

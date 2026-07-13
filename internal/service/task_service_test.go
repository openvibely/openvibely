package service

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestWorkerService(t *testing.T) *WorkerService {
	t.Helper()
	// Create a worker service with no actual agent service (won't process tasks)
	ws := &WorkerService{
		pending:     make(map[string]bool),
		numWorkers:  0,
		submitted:   make(chan models.Task, 100),
		cancelFuncs: make(map[string]context.CancelFunc),
	}
	return ws
}

func TestTaskService_ListNormalizesActiveTerminalTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, nil)
	ctx := context.Background()

	orphan := &models.Task{
		ProjectID: "default",
		Title:     "Active Cancelled Orphan",
		Category:  models.CategoryActive,
		Status:    models.StatusCancelled,
		Prompt:    "was stopped",
	}
	realQueued := &models.Task{
		ProjectID: "default",
		Title:     "Real Queued Task",
		Category:  models.CategoryActive,
		Status:    models.StatusQueued,
		Prompt:    "queued work",
	}
	require.NoError(t, taskRepo.Create(ctx, orphan))
	require.NoError(t, taskRepo.Create(ctx, realQueued))

	tasks, err := svc.ListByProjectWithCategorySorts(ctx, "default", "", "", "")
	require.NoError(t, err)

	var listedOrphan *models.Task
	var listedQueued *models.Task
	for i := range tasks {
		switch tasks[i].ID {
		case orphan.ID:
			listedOrphan = &tasks[i]
		case realQueued.ID:
			listedQueued = &tasks[i]
		}
	}
	require.NotNil(t, listedOrphan)
	require.Equal(t, models.CategoryBacklog, listedOrphan.Category)
	require.Equal(t, models.StatusCancelled, listedOrphan.Status)
	require.NotNil(t, listedQueued)
	require.Equal(t, models.CategoryActive, listedQueued.Category)
	require.Equal(t, models.StatusQueued, listedQueued.Status)

	storedOrphan, err := taskRepo.GetByID(ctx, orphan.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryBacklog, storedOrphan.Category)
}

func TestTaskService_UpdateCategoryStartsPlannerForBacklogSwarmParent(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	taskSvc := NewTaskService(taskRepo, nil, workerSvc)
	swarmSvc := NewSwarmService(taskSvc, taskRepo, nil, workerSvc)
	taskSvc.SetSwarmService(swarmSvc)
	ctx := context.Background()
	startImmediately := false

	parent, err := swarmSvc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Deferred category swarm", Prompt: "Plan this later", Category: models.CategoryBacklog, MaxWorkers: 2, ReviewerEnabled: true, MergerEnabled: true, StartImmediately: &startImmediately})
	require.NoError(t, err)
	require.Equal(t, models.CategoryBacklog, parent.Category)
	planner, err := taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.Nil(t, planner)

	require.NoError(t, taskSvc.UpdateCategory(ctx, parent.ID, models.CategoryActive))

	planner, err = taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	assert.Equal(t, models.CategoryActive, planner.Category)
	assert.Equal(t, models.StatusPending, planner.Status)
	storedParent, err := taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	assert.Equal(t, models.CategoryActive, storedParent.Category)
	assert.Equal(t, models.StatusBlocked, storedParent.Status)

	select {
	case submitted := <-workerSvc.Submitted():
		assert.Equal(t, planner.ID, submitted.ID)
		assert.NotEqual(t, parent.ID, submitted.ID, "swarm parent must not be submitted as a normal task")
	case <-time.After(time.Second):
		t.Fatal("expected planner to be submitted")
	}
}

func TestTaskService_Create_DefaultsStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test",
		Prompt:    "test",
	}

	if err := svc.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if task.Status != models.StatusPending {
		t.Errorf("expected Status=pending, got %q", task.Status)
	}
	if task.Category != models.CategoryActive {
		t.Errorf("expected Category=active (default), got %q", task.Category)
	}
}

func TestTaskService_Create_ActiveAutoSubmits(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Active Task",
		Category:  models.CategoryActive,
		Prompt:    "do something",
	}

	svc.Create(ctx, task)

	// Check that the task was submitted to the worker channel
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected submitted task ID=%s, got %s", task.ID, submitted.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected task to be auto-submitted to worker channel")
	}
}

func TestTaskService_Create_NonActiveDoesNotSubmit(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Backlog Task",
		Category:  models.CategoryBacklog,
		Prompt:    "do something",
	}

	svc.Create(ctx, task)

	select {
	case <-workerSvc.Submitted():
		t.Error("backlog task should not be auto-submitted")
	case <-time.After(100 * time.Millisecond):
		// Expected - nothing submitted
	}
}

func TestTaskService_RunTask_ResumesGoalPausedByUserStop(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	goalSvc := NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc.SetTaskGoalService(goalSvc)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Stopped Goal Task", Category: models.CategoryBacklog, Status: models.StatusCancelled, Prompt: "continue"}
	require.NoError(t, taskRepo.Create(ctx, task))
	goal, err := goalSvc.SetGoal(ctx, task.ID, "finish the objective", GoalOptions{Actor: "test"})
	require.NoError(t, err)
	require.NoError(t, goalSvc.PauseActiveGoalStoppedByUser(ctx, task.ID))

	require.NoError(t, svc.RunTask(ctx, task.ID))

	resumed, err := goalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, resumed)
	assert.Equal(t, goal.GoalID, resumed.GoalID)
	assert.Equal(t, models.TaskGoalStatusActive, resumed.Status)
	assert.Equal(t, "resumed by user", resumed.Reason)
}

func TestTaskService_RunTask_DoesNotResumeExplicitlyPausedGoal(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	goalSvc := NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc.SetTaskGoalService(goalSvc)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Explicitly Paused Goal Task", Category: models.CategoryBacklog, Status: models.StatusCancelled, Prompt: "continue"}
	require.NoError(t, taskRepo.Create(ctx, task))
	_, err := goalSvc.SetGoal(ctx, task.ID, "finish the objective", GoalOptions{Actor: "test"})
	require.NoError(t, err)
	require.NoError(t, goalSvc.PauseGoal(ctx, task.ID, "user"))

	require.NoError(t, svc.RunTask(ctx, task.ID))

	paused, err := goalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, paused)
	assert.Equal(t, models.TaskGoalStatusPaused, paused.Status)
	assert.Equal(t, "paused by user", paused.Reason)
}

func TestTaskService_UpdateStatus_RunningActiveResumesGoalPausedByUserStop(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	goalSvc := NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc.SetTaskGoalService(goalSvc)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Status Start Task", Category: models.CategoryActive, Status: models.StatusCancelled, Prompt: "continue"}
	require.NoError(t, taskRepo.Create(ctx, task))
	goal, err := goalSvc.SetGoal(ctx, task.ID, "finish the objective", GoalOptions{Actor: "test"})
	require.NoError(t, err)
	require.NoError(t, goalSvc.PauseActiveGoalStoppedByUser(ctx, task.ID))

	require.NoError(t, svc.UpdateStatus(ctx, task.ID, models.StatusRunning))

	resumed, err := goalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, resumed)
	assert.Equal(t, goal.GoalID, resumed.GoalID)
	assert.Equal(t, models.TaskGoalStatusActive, resumed.Status)
	assert.Equal(t, "resumed by user", resumed.Reason)
}

func TestTaskService_UpdateCategory_ToActiveAutoSubmits(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Moving Task",
		Category:  models.CategoryBacklog,
		Prompt:    "do something",
	}
	svc.Create(ctx, task)

	// Drain any submissions from create
	select {
	case <-workerSvc.Submitted():
	case <-time.After(50 * time.Millisecond):
	}

	// Move to active
	if err := svc.UpdateCategory(ctx, task.ID, models.CategoryActive); err != nil {
		t.Fatalf("UpdateCategory: %v", err)
	}

	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected submitted task ID=%s, got %s", task.ID, submitted.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected task to be auto-submitted when moved to active")
	}
}

func TestWorkerService_Submit_DeduplicatesSameTask(t *testing.T) {
	workerSvc := newTestWorkerService(t)

	task := models.Task{
		ID:    "dedup-test-id",
		Title: "Dedup Task",
	}

	// First submit should go through
	workerSvc.Submit(task)

	// Second submit of same task should be skipped
	workerSvc.Submit(task)

	// Only one task should be in the channel
	select {
	case got := <-workerSvc.Submitted():
		if got.ID != task.ID {
			t.Errorf("expected task ID=%s, got %s", task.ID, got.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected one task in channel")
	}

	// Channel should be empty now
	select {
	case got := <-workerSvc.Submitted():
		t.Errorf("expected empty channel, got task %q", got.Title)
	case <-time.After(100 * time.Millisecond):
		// Expected - no duplicate
	}
}

func TestWorkerService_Submit_AllowsResubmitAfterDrain(t *testing.T) {
	workerSvc := newTestWorkerService(t)

	task := models.Task{
		ID:    "resubmit-test-id",
		Title: "Resubmit Task",
	}

	// Submit and drain (simulating worker picking it up)
	workerSvc.Submit(task)
	<-workerSvc.Submitted()
	workerSvc.mu.Lock()
	delete(workerSvc.pending, task.ID)
	workerSvc.queue = nil
	workerSvc.mu.Unlock()

	// Should be able to submit again after drain
	workerSvc.Submit(task)

	select {
	case got := <-workerSvc.Submitted():
		if got.ID != task.ID {
			t.Errorf("expected task ID=%s, got %s", task.ID, got.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected task to be re-submitted after drain")
	}
}

func TestWorkerService_Submit_DifferentTasksAllowed(t *testing.T) {
	workerSvc := newTestWorkerService(t)

	task1 := models.Task{ID: "task-1", Title: "Task 1"}
	task2 := models.Task{ID: "task-2", Title: "Task 2"}
	task3 := models.Task{ID: "task-3", Title: "Task 3"}

	workerSvc.Submit(task1)
	workerSvc.Submit(task2)
	workerSvc.Submit(task3)

	// All three different tasks should be in the channel
	received := 0
	for i := 0; i < 3; i++ {
		select {
		case <-workerSvc.Submitted():
			received++
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("expected 3 tasks, got %d", received)
		}
	}
	if received != 3 {
		t.Errorf("expected 3 tasks, got %d", received)
	}
}

func TestWorkerService_Submit_DedupHoldsDuringExecution(t *testing.T) {
	// Verifies fix for the race condition: when a task is picked up from
	// the channel, the pending map entry should NOT be cleared until after
	// execution completes. This prevents the scheduler from re-submitting
	// the same task between channel drain and ClaimTask.
	workerSvc := newTestWorkerService(t)

	task := models.Task{ID: "race-test-id", Title: "Race Test"}

	// Submit task
	workerSvc.Submit(task)

	// Simulate worker picking up from channel (but NOT calling pending.Delete)
	<-workerSvc.Submitted()

	// The pending entry should still be present (this is the fix:
	// pending.Delete now happens AFTER execution, not when picking up)
	// Try to re-submit — should be deduped
	workerSvc.Submit(task)

	// Channel should be empty (the re-submit was deduped)
	select {
	case got := <-workerSvc.Submitted():
		t.Errorf("expected dedup to prevent re-submission during execution, got task %q", got.Title)
	case <-time.After(100 * time.Millisecond):
		// Expected — dedup prevented re-submission
	}

	// Now simulate execution completing: clear pending
	workerSvc.mu.Lock()
	delete(workerSvc.pending, task.ID)
	workerSvc.queue = nil
	workerSvc.mu.Unlock()

	// After execution completes, re-submit should work
	workerSvc.Submit(task)
	select {
	case got := <-workerSvc.Submitted():
		if got.ID != task.ID {
			t.Errorf("expected task ID=%s, got %s", task.ID, got.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected task to be re-submittable after execution completes")
	}
}

func TestWorkerService_Resize_IncreasesWorkerCount(t *testing.T) {
	ws := NewWorkerService(nil, 1, nil)
	if ws.NumWorkers() != 1 {
		t.Fatalf("expected 1 worker, got %d", ws.NumWorkers())
	}

	// Resize before Start should just update the count
	ws.Resize(3)
	if ws.NumWorkers() != 3 {
		t.Errorf("expected 3 workers after Resize, got %d", ws.NumWorkers())
	}
}

func TestWorkerService_Resize_DecreasesWorkerCount(t *testing.T) {
	ws := NewWorkerService(nil, 5, nil)

	// Resize before Start
	ws.Resize(2)
	if ws.NumWorkers() != 2 {
		t.Errorf("expected 2 workers after Resize, got %d", ws.NumWorkers())
	}
}

func TestTaskService_UpdateCategory_PromotesQueuedTaskThreadFollowupBeforeOriginalPrompt(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	project := &models.Project{Name: "Manual Active Promotion Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent := &models.LLMConfig{Name: "Test Agent", Provider: models.ProviderTest, Model: "test-model", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	workerSvc := NewWorkerService(nil, 0, nil)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	task := &models.Task{ProjectID: project.ID, Title: "Manual Active Promotion Task", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "original prompt", AgentID: &agent.ID}
	require.NoError(t, taskRepo.Create(ctx, task))
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: "queued follow-up"}
	require.NoError(t, threadInputRepo.CreateQueued(ctx, queued))
	svc.SetQueuedTaskThreadFollowupHook(func(ctx context.Context, taskID string) (bool, error) {
		pending, err := threadInputRepo.FindOldestQueuedForTask(ctx, taskID)
		require.NoError(t, err)
		require.NotNil(t, pending)
		exec := &models.Execution{TaskID: taskID, AgentConfigID: pending.AgentConfigID, Status: models.ExecRunning, PromptSent: pending.Content, IsFollowup: true}
		require.NoError(t, threadInputRepo.ClaimQueuedForTaskExecution(ctx, pending.ID, exec))
		require.NoError(t, taskRepo.UpdateStatus(ctx, taskID, models.StatusQueued))
		return true, nil
	})

	require.NoError(t, svc.UpdateCategory(ctx, task.ID, models.CategoryActive))
	execs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1)
	assert.Equal(t, "queued follow-up", execs[0].PromptSent)
	assert.True(t, execs[0].IsFollowup)
	select {
	case submitted := <-workerSvc.Submitted():
		t.Fatalf("expected original task not to be submitted, got %s", submitted.Prompt)
	default:
	}
}

func TestTaskService_UpdateCategory_RollsBackDeferredActivationWhenReloadFails(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), newTestWorkerService(t))
	task := &models.Task{
		ProjectID: "default",
		Title:     "Deferred activation reload failure",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "process attachment",
	}
	require.NoError(t, taskRepo.Create(ctx, task))
	svc.updateCategoryTaskLoader = func(context.Context, string) (*models.Task, error) {
		return nil, assert.AnError
	}

	require.ErrorIs(t, svc.UpdateCategory(ctx, task.ID, models.CategoryActive), assert.AnError)
	got, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, models.CategoryBacklog, got.Category)
	assert.Equal(t, models.StatusPending, got.Status)
}

func TestTaskService_UpdateCategory_QueuedFollowupRetryCreatesOneExecutionAfterActivationFailure(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	workerSvc := newTestWorkerService(t)
	svc := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), workerSvc)
	project := &models.Project{Name: "Deferred queued retry project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent := &models.LLMConfig{Name: "Deferred queued retry model", Provider: models.ProviderTest, Model: "test-model"}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	task := &models.Task{ProjectID: project.ID, Title: "Deferred queued retry", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "original prompt", AgentID: &agent.ID}
	require.NoError(t, taskRepo.Create(ctx, task))
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: "queued follow-up"}
	require.NoError(t, threadInputRepo.CreateQueued(ctx, queued))

	calls := 0
	svc.SetQueuedTaskThreadFollowupHook(func(ctx context.Context, taskID string) (bool, error) {
		calls++
		if calls == 1 {
			return true, assert.AnError
		}
		existing, err := execRepo.ListByTaskChronological(ctx, taskID)
		if err != nil {
			return true, err
		}
		if len(existing) > 0 {
			return true, nil
		}
		exec := &models.Execution{TaskID: taskID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: queued.Content, IsFollowup: true}
		return true, threadInputRepo.ClaimQueuedForTaskExecution(ctx, queued.ID, exec)
	})

	require.ErrorIs(t, svc.UpdateCategory(ctx, task.ID, models.CategoryActive), assert.AnError)
	got, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, models.CategoryBacklog, got.Category)
	assert.Equal(t, models.StatusPending, got.Status)
	execs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, execs)
	pending, err := threadInputRepo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ThreadInputPending, pending.InputStatus)

	require.NoError(t, svc.UpdateCategory(ctx, task.ID, models.CategoryActive))
	require.NoError(t, svc.UpdateCategory(ctx, task.ID, models.CategoryActive))
	execs, err = execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1)
	assert.Equal(t, queued.Content, execs[0].PromptSent)
	assert.True(t, execs[0].IsFollowup)
	pending, err = threadInputRepo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ThreadInputApplied, pending.InputStatus)
	select {
	case submitted := <-workerSvc.Submitted():
		t.Fatalf("original task was submitted during queued follow-up retry: %s", submitted.ID)
	default:
	}
}

func TestTaskService_UpdateCategory_FailedFollowupRetryCreatesOneExecutionAfterActivationFailure(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	workerSvc := newTestWorkerService(t)
	svc := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), workerSvc)
	project := &models.Project{Name: "Deferred failed retry project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent := &models.LLMConfig{Name: "Deferred failed retry model", Provider: models.ProviderTest, Model: "test-model"}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	task := &models.Task{ProjectID: project.ID, Title: "Deferred failed retry", Category: models.CategoryBacklog, Status: models.StatusFailed, Prompt: "original prompt", AgentID: &agent.ID}
	require.NoError(t, taskRepo.Create(ctx, task))
	failed := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecFailed, PromptSent: "failed follow-up", IsFollowup: true}
	require.NoError(t, execRepo.Create(ctx, failed))

	calls := 0
	svc.SetFailedTaskThreadFollowupRetryHook(func(ctx context.Context, taskID string) (bool, error) {
		calls++
		if calls == 1 {
			return true, assert.AnError
		}
		active, err := execRepo.HasActiveTaskExecution(ctx, taskID, "")
		if err != nil || active {
			return active, err
		}
		retry := &models.Execution{TaskID: taskID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: failed.PromptSent, IsFollowup: true}
		return true, execRepo.CreateDirectTaskFollowup(ctx, retry)
	})

	require.ErrorIs(t, svc.UpdateCategory(ctx, task.ID, models.CategoryActive), assert.AnError)
	got, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, models.CategoryBacklog, got.Category)
	assert.Equal(t, models.StatusFailed, got.Status)
	execs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1)

	require.NoError(t, svc.UpdateCategory(ctx, task.ID, models.CategoryActive))
	require.NoError(t, svc.UpdateCategory(ctx, task.ID, models.CategoryActive))
	execs, err = execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 2)
	running := 0
	for _, exec := range execs {
		if exec.Status == models.ExecRunning {
			running++
			assert.Equal(t, failed.PromptSent, exec.PromptSent)
			assert.True(t, exec.IsFollowup)
		}
	}
	assert.Equal(t, 1, running, "retry must create exactly one running follow-up execution")
	select {
	case submitted := <-workerSvc.Submitted():
		t.Fatalf("original task was submitted during failed follow-up retry: %s", submitted.ID)
	default:
	}
}

func TestTaskService_RunTask_PromotesQueuedTaskThreadFollowupBeforeOriginalPrompt(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	project := &models.Project{Name: "Manual Run Promotion Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent := &models.LLMConfig{Name: "Test Agent", Provider: models.ProviderTest, Model: "test-model", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	workerSvc := NewWorkerService(nil, 0, nil)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	task := &models.Task{ProjectID: project.ID, Title: "Manual Run Promotion Task", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "original prompt", AgentID: &agent.ID}
	require.NoError(t, taskRepo.Create(ctx, task))
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: "queued follow-up"}
	require.NoError(t, threadInputRepo.CreateQueued(ctx, queued))
	svc.SetQueuedTaskThreadFollowupHook(func(ctx context.Context, taskID string) (bool, error) {
		pending, err := threadInputRepo.FindOldestQueuedForTask(ctx, taskID)
		require.NoError(t, err)
		if pending == nil {
			return false, nil
		}
		exec := &models.Execution{TaskID: taskID, AgentConfigID: pending.AgentConfigID, Status: models.ExecRunning, PromptSent: pending.Content, IsFollowup: true}
		if err := threadInputRepo.ClaimQueuedForTaskExecution(ctx, pending.ID, exec); err != nil {
			return true, err
		}
		require.NoError(t, taskRepo.UpdateStatus(ctx, taskID, models.StatusQueued))
		return true, nil
	})

	require.NoError(t, svc.RunTask(ctx, task.ID))
	execs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1)
	assert.Equal(t, "queued follow-up", execs[0].PromptSent)
	assert.True(t, execs[0].IsFollowup)
	select {
	case submitted := <-workerSvc.Submitted():
		t.Fatalf("expected original task not to be submitted, got %s", submitted.Prompt)
	default:
	}
}

func TestTaskService_UpdateCategory_RetriesFailedTaskThreadFollowupBeforeOriginalPrompt(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	project := &models.Project{Name: "Manual Active Failed Followup Retry Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	workerSvc := NewWorkerService(nil, 0, nil)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	task := &models.Task{ProjectID: project.ID, Title: "Manual Active Failed Followup Retry Task", Category: models.CategoryBacklog, Status: models.StatusFailed, Prompt: "original prompt"}
	require.NoError(t, taskRepo.Create(ctx, task))
	var retriedTaskID string
	svc.SetFailedTaskThreadFollowupRetryHook(func(ctx context.Context, taskID string) (bool, error) {
		retriedTaskID = taskID
		require.NoError(t, taskRepo.UpdateStatus(ctx, taskID, models.StatusQueued))
		require.NoError(t, taskRepo.UpdateCategory(ctx, taskID, models.CategoryActive))
		return true, nil
	})

	require.NoError(t, svc.UpdateCategory(ctx, task.ID, models.CategoryActive))
	assert.Equal(t, task.ID, retriedTaskID)
	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusQueued, updated.Status)
	assert.Equal(t, models.CategoryActive, updated.Category)
	select {
	case submitted := <-workerSvc.Submitted():
		t.Fatalf("expected original task not to be submitted, got %s", submitted.Prompt)
	default:
	}
}

func TestTaskService_RunTask_DoesNotRerunOriginalPromptWhenQueuedFollowupPromotionFails(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	task := &models.Task{ProjectID: "default", Title: "Promotion Failure", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "original prompt"}
	require.NoError(t, taskRepo.Create(ctx, task))
	svc.SetQueuedTaskThreadFollowupHook(func(ctx context.Context, taskID string) (bool, error) {
		return true, assert.AnError
	})

	err := svc.RunTask(ctx, task.ID)
	require.ErrorIs(t, err, assert.AnError)
	got, getErr := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, getErr)
	assert.Equal(t, models.StatusCompleted, got.Status)
	assert.Equal(t, models.CategoryCompleted, got.Category)
	select {
	case submitted := <-workerSvc.Submitted():
		t.Fatalf("expected original task not to be submitted after promotion failure, got %s", submitted.Prompt)
	default:
	}
}

func TestTaskService_RunTask_ResetsStatusAndSubmits(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Run Test",
		Category:  models.CategoryScheduled,
		Prompt:    "do something",
	}
	svc.Create(ctx, task)

	// Drain create submission
	select {
	case <-workerSvc.Submitted():
	case <-time.After(50 * time.Millisecond):
	}

	// Manually set status to completed
	taskRepo.UpdateStatus(ctx, task.ID, models.StatusCompleted)

	// RunTask should reset to pending and submit
	if err := svc.RunTask(ctx, task.ID); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	got, _ := taskRepo.GetByID(ctx, task.ID)
	if got.Status != models.StatusPending {
		t.Errorf("expected Status=pending after RunTask, got %q", got.Status)
	}

	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected submitted task ID=%s, got %s", task.ID, submitted.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected task to be submitted via RunTask")
	}
}

func TestTaskService_RunTask_MovesBacklogToActive(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	// Create a task in backlog category
	task := &models.Task{
		ProjectID: "default",
		Title:     "Backlog Task",
		Category:  models.CategoryBacklog,
		Prompt:    "do something",
	}
	svc.Create(ctx, task)

	// Drain create submission
	select {
	case <-workerSvc.Submitted():
	case <-time.After(50 * time.Millisecond):
	}

	// RunTask should move to active category and set status to pending
	if err := svc.RunTask(ctx, task.ID); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	got, _ := taskRepo.GetByID(ctx, task.ID)
	if got.Category != models.CategoryActive {
		t.Errorf("expected Category=active after RunTask, got %q", got.Category)
	}
	if got.Status != models.StatusPending {
		t.Errorf("expected Status=pending after RunTask, got %q", got.Status)
	}

	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected submitted task ID=%s, got %s", task.ID, submitted.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected task to be submitted via RunTask")
	}
}

func TestTaskService_RunTask_KeepsActiveCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	// Create task in backlog, then manually move to active via repo
	// (avoids Create auto-submit which causes dedup issues in test)
	task := &models.Task{
		ProjectID: "default",
		Title:     "Active Task",
		Category:  models.CategoryBacklog,
		Prompt:    "do something",
	}
	svc.Create(ctx, task)

	// Drain create submission (backlog tasks don't auto-submit, but drain just in case)
	select {
	case <-workerSvc.Submitted():
	case <-time.After(50 * time.Millisecond):
	}

	// Move to active directly in the DB
	taskRepo.UpdateCategory(ctx, task.ID, models.CategoryActive)

	if err := svc.RunTask(ctx, task.ID); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	got, _ := taskRepo.GetByID(ctx, task.ID)
	if got.Category != models.CategoryActive {
		t.Errorf("expected Category=active (unchanged), got %q", got.Category)
	}

	select {
	case <-workerSvc.Submitted():
	case <-time.After(100 * time.Millisecond):
		t.Error("expected task to be submitted")
	}
}

func TestTaskService_RunTask_RunningNoOpDoesNotSubmit(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Running No-op",
		Category:  models.CategoryBacklog,
		Prompt:    "do something",
	}
	svc.Create(ctx, task)

	if err := taskRepo.UpdateCategory(ctx, task.ID, models.CategoryActive); err != nil {
		t.Fatalf("UpdateCategory: %v", err)
	}
	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusRunning); err != nil {
		t.Fatalf("UpdateStatus running: %v", err)
	}

	if err := svc.RunTask(ctx, task.ID); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	got, _ := taskRepo.GetByID(ctx, task.ID)
	if got.Status != models.StatusRunning {
		t.Errorf("expected Status=running to remain unchanged, got %q", got.Status)
	}

	select {
	case submitted := <-workerSvc.Submitted():
		t.Errorf("expected no submit for running task, got task ID=%s", submitted.ID)
	case <-time.After(100 * time.Millisecond):
		// Expected no-op
	}
}

func TestTaskService_RunTask_QueuedNoOpDoesNotSubmit(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Queued No-op",
		Category:  models.CategoryBacklog,
		Prompt:    "do something",
	}
	svc.Create(ctx, task)

	if err := taskRepo.UpdateCategory(ctx, task.ID, models.CategoryActive); err != nil {
		t.Fatalf("UpdateCategory: %v", err)
	}
	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusQueued); err != nil {
		t.Fatalf("UpdateStatus queued: %v", err)
	}

	if err := svc.RunTask(ctx, task.ID); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	got, _ := taskRepo.GetByID(ctx, task.ID)
	if got.Status != models.StatusQueued {
		t.Errorf("expected Status=queued to remain unchanged, got %q", got.Status)
	}

	select {
	case submitted := <-workerSvc.Submitted():
		t.Errorf("expected no submit for queued task, got task ID=%s", submitted.ID)
	case <-time.After(100 * time.Millisecond):
		// Expected no-op
	}
}

func TestTaskService_MoveCompletedActiveToCompleted(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	// Create tasks in various states
	completedActive := &models.Task{
		ProjectID: "default",
		Title:     "Completed Active",
		Category:  models.CategoryActive,
		Status:    models.StatusCompleted,
		Prompt:    "p",
	}
	pendingActive := &models.Task{
		ProjectID: "default",
		Title:     "Pending Active",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "p",
	}

	taskRepo.Create(ctx, completedActive)
	taskRepo.Create(ctx, pendingActive)

	// Drain any auto-submissions from active category
	for i := 0; i < 2; i++ {
		select {
		case <-workerSvc.Submitted():
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Move completed active tasks
	count, err := svc.MoveCompletedActiveToCompleted(ctx)
	if err != nil {
		t.Fatalf("MoveCompletedActiveToCompleted: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 task moved, got %d", count)
	}

	// Verify the completed task was moved to completed category
	movedTask, _ := taskRepo.GetByID(ctx, completedActive.ID)
	if movedTask.Category != models.CategoryCompleted {
		t.Errorf("expected completed task moved to completed category, got %q", movedTask.Category)
	}

	// Verify the pending task remained in active
	unchangedTask, _ := taskRepo.GetByID(ctx, pendingActive.ID)
	if unchangedTask.Category != models.CategoryActive {
		t.Errorf("expected pending task to remain active, got %q", unchangedTask.Category)
	}
}

func TestTaskService_Create_DuplicateTitle(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	task1 := &models.Task{
		ProjectID: "default",
		Title:     "Duplicate Task",
		Category:  models.CategoryActive,
		Prompt:    "do something",
	}
	if err := svc.Create(ctx, task1); err != nil {
		t.Fatalf("Create first task: %v", err)
	}

	// Drain auto-submission
	select {
	case <-workerSvc.Submitted():
	case <-time.After(50 * time.Millisecond):
	}

	// Attempt to create another task with the same title
	task2 := &models.Task{
		ProjectID: "default",
		Title:     "Duplicate Task",
		Category:  models.CategoryBacklog,
		Prompt:    "do something else",
	}
	err := svc.Create(ctx, task2)
	if err != ErrDuplicateTask {
		t.Errorf("expected ErrDuplicateTask, got %v", err)
	}
}

func TestTaskService_Update_DuplicateTitle(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	task1 := &models.Task{
		ProjectID: "default",
		Title:     "Task 1",
		Category:  models.CategoryActive,
		Prompt:    "do something",
	}
	task2 := &models.Task{
		ProjectID: "default",
		Title:     "Task 2",
		Category:  models.CategoryActive,
		Prompt:    "do something else",
	}
	svc.Create(ctx, task1)
	svc.Create(ctx, task2)

	// Drain auto-submissions
	for i := 0; i < 2; i++ {
		select {
		case <-workerSvc.Submitted():
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Try to update task2 to have the same title as task1
	task2.Title = "Task 1"
	err := svc.Update(ctx, task2)
	if err != ErrDuplicateTask {
		t.Errorf("expected ErrDuplicateTask, got %v", err)
	}
}

func TestTaskService_Delete_DeletesAttachments(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	workerSvc := newTestWorkerService(t)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	// Create a task
	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Task",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Create a temporary file to simulate an attachment
	tmpFile, err := os.CreateTemp("", "test-attachment-*.txt")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	tmpFile.WriteString("test content")
	tmpFile.Close()
	tmpFilePath := tmpFile.Name()

	// Create an attachment
	attachment := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "test.txt",
		FilePath:  tmpFilePath,
		MediaType: "text/plain",
		FileSize:  12,
	}
	if err := attachmentRepo.Create(ctx, attachment); err != nil {
		t.Fatalf("Create attachment: %v", err)
	}

	// Verify attachment exists in DB
	attachments, err := attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask before delete: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment before delete, got %d", len(attachments))
	}

	// Verify file exists
	if _, err := os.Stat(tmpFilePath); os.IsNotExist(err) {
		t.Fatalf("file should exist before deletion")
	}

	// Delete the task
	if err := svc.Delete(ctx, task.ID); err != nil {
		t.Fatalf("Delete task: %v", err)
	}

	// Verify task was deleted
	deletedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID after delete: %v", err)
	}
	if deletedTask != nil {
		t.Error("expected task to be deleted")
	}

	// Verify attachment was deleted from DB
	attachments, err = attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask after delete: %v", err)
	}
	if len(attachments) != 0 {
		t.Errorf("expected 0 attachments after delete, got %d", len(attachments))
	}

	// Verify physical file was deleted
	if _, err := os.Stat(tmpFilePath); !os.IsNotExist(err) {
		t.Error("expected file to be deleted")
	}
}

func TestTaskService_UpdateCategory_FromCompletedToActiveResetsStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	// Create a task in the completed category with completed status
	task := &models.Task{
		ProjectID: "default",
		Title:     "Completed Task",
		Category:  models.CategoryCompleted,
		Status:    models.StatusCompleted,
		Prompt:    "do something",
	}
	taskRepo.Create(ctx, task)

	// Move it to active
	if err := svc.UpdateCategory(ctx, task.ID, models.CategoryActive); err != nil {
		t.Fatalf("UpdateCategory: %v", err)
	}

	// Verify the task still exists
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updatedTask == nil {
		t.Fatal("task should still exist after moving to active")
	}

	// Verify category was updated to active
	if updatedTask.Category != models.CategoryActive {
		t.Errorf("expected Category=active, got %q", updatedTask.Category)
	}

	// Verify status was reset to pending
	if updatedTask.Status != models.StatusPending {
		t.Errorf("expected Status=pending after moving completed task to active, got %q", updatedTask.Status)
	}

	// Verify it was auto-submitted
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected submitted task ID=%s, got %s", task.ID, submitted.ID)
		}
		if submitted.Status != models.StatusPending {
			t.Errorf("expected submitted task status=pending, got %q", submitted.Status)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected task to be auto-submitted when moved from completed to active")
	}
}

func TestTaskService_CancelTask_AllowsActivePendingTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Active Pending Task",
		Prompt:    "test",
		Status:    models.StatusPending,
		Category:  models.CategoryActive,
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	require.NoError(t, svc.CancelTask(ctx, task.ID))

	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, models.StatusCancelled, updated.Status)
	assert.Equal(t, models.CategoryBacklog, updated.Category)
}

func TestTaskService_CancelTask_AllowsQueuedTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Queued Task",
		Prompt:    "test",
		Status:    models.StatusQueued,
		Category:  models.CategoryActive,
	}
	require.NoError(t, taskRepo.Create(ctx, task))

	cancelled := make(chan struct{}, 1)
	workerSvc.RegisterCancel(task.ID, func() { cancelled <- struct{}{} })

	require.NoError(t, svc.CancelTask(ctx, task.ID))

	select {
	case <-cancelled:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected queued task cancel function to be called")
	}

	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, models.StatusCancelled, updated.Status)
	assert.Equal(t, models.CategoryBacklog, updated.Category)
}

func TestTaskService_CancelTask_ReturnsCategoryUpdateFailure(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Category Failure Cancel Task",
		Prompt:    "test",
		Status:    models.StatusQueued,
		Category:  models.CategoryActive,
	}
	require.NoError(t, taskRepo.Create(ctx, task))
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS fail_cancel_backlog_move`) })
	_, err := db.ExecContext(ctx, `
		CREATE TRIGGER fail_cancel_backlog_move
		BEFORE UPDATE OF category ON tasks
		WHEN NEW.title = 'Category Failure Cancel Task' AND NEW.category = 'backlog'
		BEGIN
			SELECT RAISE(FAIL, 'forced category update failure');
		END;
	`)
	require.NoError(t, err)

	err = svc.CancelTask(ctx, task.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "move cancelled task to backlog")

	updated, getErr := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, getErr)
	require.NotNil(t, updated)
	assert.Equal(t, models.StatusCancelled, updated.Status)
	assert.Equal(t, models.CategoryActive, updated.Category)
}

func TestTaskService_UpdateCategory_FromActiveToCompletedCancelsRunning(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	goalSvc := NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc.SetTaskGoalService(goalSvc)
	ctx := context.Background()

	// Create a running task in the active category
	task := &models.Task{
		ProjectID: "default",
		Title:     "Running Active Task",
		Category:  models.CategoryActive,
		Status:    models.StatusRunning,
		Prompt:    "do something",
	}
	taskRepo.Create(ctx, task)
	goal, err := goalSvc.SetGoal(ctx, task.ID, "Keep going until done", GoalOptions{Actor: "test"})
	require.NoError(t, err)

	// Move to completed — should cancel the running execution, pause the goal, and keep the requested category.
	if err := svc.UpdateCategory(ctx, task.ID, models.CategoryCompleted); err != nil {
		t.Fatalf("UpdateCategory: %v", err)
	}

	// Verify status was set to cancelled and category stayed completed.
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updatedTask.Status != models.StatusCancelled {
		t.Errorf("expected Status=cancelled after moving running task away from active, got %q", updatedTask.Status)
	}
	if updatedTask.Category != models.CategoryCompleted {
		t.Errorf("expected Category=completed after dropping running task on completed, got %q", updatedTask.Category)
	}
	paused, err := goalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, paused)
	assert.Equal(t, models.TaskGoalStatusPaused, paused.Status)
	assert.Equal(t, goal.GoalID, paused.GoalID)
	assert.Equal(t, "stopped by user", paused.Reason)
}

func TestTaskService_UpdateCategory_FromActiveToCompletedCancelsQueued(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	goalSvc := NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc.SetTaskGoalService(goalSvc)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Queued Active Task",
		Category:  models.CategoryActive,
		Status:    models.StatusQueued,
		Prompt:    "wait for capacity",
	}
	require.NoError(t, taskRepo.Create(ctx, task))
	goal, err := goalSvc.SetGoal(ctx, task.ID, "Keep going until done", GoalOptions{Actor: "test"})
	require.NoError(t, err)
	cancelled := make(chan struct{}, 1)
	workerSvc.RegisterCancel(task.ID, func() { cancelled <- struct{}{} })

	require.NoError(t, svc.UpdateCategory(ctx, task.ID, models.CategoryCompleted))

	select {
	case <-cancelled:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected queued task cancel function to be called")
	}
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedTask)
	assert.Equal(t, models.StatusCancelled, updatedTask.Status)
	assert.Equal(t, models.CategoryCompleted, updatedTask.Category)
	paused, err := goalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, paused)
	assert.Equal(t, models.TaskGoalStatusPaused, paused.Status)
	assert.Equal(t, goal.GoalID, paused.GoalID)
	assert.Equal(t, "stopped by user", paused.Reason)
}

func TestTaskService_UpdateCategory_FromActiveToBacklogCancelsQueued(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	goalSvc := NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc.SetTaskGoalService(goalSvc)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Queued Active Task To Backlog",
		Category:  models.CategoryActive,
		Status:    models.StatusQueued,
		Prompt:    "wait for capacity",
	}
	require.NoError(t, taskRepo.Create(ctx, task))
	goal, err := goalSvc.SetGoal(ctx, task.ID, "Keep going until done", GoalOptions{Actor: "test"})
	require.NoError(t, err)
	cancelled := make(chan struct{}, 1)
	workerSvc.RegisterCancel(task.ID, func() { cancelled <- struct{}{} })

	require.NoError(t, svc.UpdateCategory(ctx, task.ID, models.CategoryBacklog))

	select {
	case <-cancelled:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected queued task cancel function to be called")
	}
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedTask)
	assert.Equal(t, models.StatusCancelled, updatedTask.Status)
	assert.Equal(t, models.CategoryBacklog, updatedTask.Category)
	paused, err := goalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, paused)
	assert.Equal(t, models.TaskGoalStatusPaused, paused.Status)
	assert.Equal(t, goal.GoalID, paused.GoalID)
	assert.Equal(t, "stopped by user", paused.Reason)
}

func TestTaskService_UpdateCategory_ToActiveResumesGoalPausedByUserStop(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	goalSvc := NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc.SetTaskGoalService(goalSvc)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Drag Back To Active Task",
		Category:  models.CategoryCompleted,
		Status:    models.StatusCancelled,
		Prompt:    "continue",
	}
	require.NoError(t, taskRepo.Create(ctx, task))
	goal, err := goalSvc.SetGoal(ctx, task.ID, "finish the objective", GoalOptions{Actor: "test"})
	require.NoError(t, err)
	require.NoError(t, goalSvc.PauseActiveGoalStoppedByUser(ctx, task.ID))

	require.NoError(t, svc.UpdateCategory(ctx, task.ID, models.CategoryActive))

	resumed, err := goalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, resumed)
	assert.Equal(t, goal.GoalID, resumed.GoalID)
	assert.Equal(t, models.TaskGoalStatusActive, resumed.Status)
	assert.Equal(t, "resumed by user", resumed.Reason)
}

func TestTaskService_UpdateCategory_FromRunningToActiveResetsStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	// Create a task with stale "running" status (e.g., prior execution finished without resetting)
	task := &models.Task{
		ProjectID: "default",
		Title:     "Stale Running Task",
		Category:  models.CategoryBacklog,
		Status:    models.StatusRunning,
		Prompt:    "do something",
	}
	taskRepo.Create(ctx, task)

	// Move it to active — should reset status and auto-submit despite "running" status
	if err := svc.UpdateCategory(ctx, task.ID, models.CategoryActive); err != nil {
		t.Fatalf("UpdateCategory: %v", err)
	}

	// Verify status was reset to pending
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updatedTask.Status != models.StatusPending {
		t.Errorf("expected Status=pending after moving running task to active, got %q", updatedTask.Status)
	}

	// Verify it was auto-submitted
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected submitted task ID=%s, got %s", task.ID, submitted.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected task to be auto-submitted when moved from running to active")
	}
}

func TestTaskService_ActiveTaskWithCompletedStatusIsNotDeleted(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create a task in the active category
	task := &models.Task{
		ProjectID: "default",
		Title:     "Active Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "do something",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Manually set status to completed (simulating a race condition where task completes quickly)
	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusCompleted); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	// Verify the task still exists and is in active category with completed status
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updatedTask == nil {
		t.Fatal("task should exist")
	}
	if updatedTask.Category != models.CategoryActive {
		t.Errorf("expected Category=active, got %q", updatedTask.Category)
	}
	if updatedTask.Status != models.StatusCompleted {
		t.Errorf("expected Status=completed, got %q", updatedTask.Status)
	}

	// Verify it appears when listing tasks in the active category
	activeTasks, err := taskRepo.ListByProject(ctx, "default", "active")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}

	found := false
	for _, at := range activeTasks {
		if at.ID == task.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("task with category=active and status=completed should appear in active category listing")
	}
}

func TestTaskService_DeleteAllCompleted(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	// Create test projects
	project1 := &models.Project{Name: "Test Project 1"}
	project2 := &models.Project{Name: "Test Project 2"}
	if err := projectRepo.Create(ctx, project1); err != nil {
		t.Fatalf("failed to create project1: %v", err)
	}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("failed to create project2: %v", err)
	}

	// Create tasks in various categories for project1
	completedTask1 := &models.Task{
		ProjectID: project1.ID,
		Title:     "Completed Task 1",
		Category:  models.CategoryCompleted,
		Status:    models.StatusCompleted,
		Prompt:    "p",
	}
	completedTask2 := &models.Task{
		ProjectID: project1.ID,
		Title:     "Completed Task 2",
		Category:  models.CategoryCompleted,
		Status:    models.StatusCompleted,
		Prompt:    "p",
	}
	activeTask := &models.Task{
		ProjectID: project1.ID,
		Title:     "Active Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "p",
	}

	// Create completed task in different project (should NOT be deleted)
	completedTaskProject2 := &models.Task{
		ProjectID: project2.ID,
		Title:     "Completed Task Project 2",
		Category:  models.CategoryCompleted,
		Status:    models.StatusCompleted,
		Prompt:    "p",
	}

	taskRepo.Create(ctx, completedTask1)
	taskRepo.Create(ctx, completedTask2)
	taskRepo.Create(ctx, completedTaskProject2)
	svc.Create(ctx, activeTask)

	// Drain any auto-submissions
	select {
	case <-workerSvc.Submitted():
	case <-time.After(50 * time.Millisecond):
	}

	// Delete all completed tasks for project1
	count, err := svc.DeleteAllCompleted(ctx, project1.ID)
	if err != nil {
		t.Fatalf("DeleteAllCompleted: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 tasks deleted, got %d", count)
	}

	// Verify completed tasks from project1 were deleted
	task1, _ := taskRepo.GetByID(ctx, completedTask1.ID)
	if task1 != nil {
		t.Error("expected completed task 1 to be deleted")
	}

	task2, _ := taskRepo.GetByID(ctx, completedTask2.ID)
	if task2 != nil {
		t.Error("expected completed task 2 to be deleted")
	}

	// Verify active task still exists
	activeTaskResult, _ := taskRepo.GetByID(ctx, activeTask.ID)
	if activeTaskResult == nil {
		t.Error("expected active task to still exist")
	}

	// Verify completed task from project2 was NOT deleted
	taskProject2, _ := taskRepo.GetByID(ctx, completedTaskProject2.ID)
	if taskProject2 == nil {
		t.Error("expected completed task from project2 to still exist")
	}
}

func TestTaskService_DeleteAllBacklog(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	// Create test projects
	project1 := &models.Project{Name: "Test Project 1"}
	project2 := &models.Project{Name: "Test Project 2"}
	if err := projectRepo.Create(ctx, project1); err != nil {
		t.Fatalf("failed to create project1: %v", err)
	}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("failed to create project2: %v", err)
	}

	// Create tasks in various categories for project1
	backlogTask1 := &models.Task{
		ProjectID: project1.ID,
		Title:     "Backlog Task 1",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	backlogTask2 := &models.Task{
		ProjectID: project1.ID,
		Title:     "Backlog Task 2",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	activeTask := &models.Task{
		ProjectID: project1.ID,
		Title:     "Active Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "p",
	}

	// Create backlog task in different project (should NOT be deleted)
	backlogTaskProject2 := &models.Task{
		ProjectID: project2.ID,
		Title:     "Backlog Task Project 2",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "p",
	}

	taskRepo.Create(ctx, backlogTask1)
	taskRepo.Create(ctx, backlogTask2)
	taskRepo.Create(ctx, backlogTaskProject2)
	svc.Create(ctx, activeTask)

	// Drain any auto-submissions
	select {
	case <-workerSvc.Submitted():
	case <-time.After(50 * time.Millisecond):
	}

	// Delete all backlog tasks for project1
	count, err := svc.DeleteAllBacklog(ctx, project1.ID)
	if err != nil {
		t.Fatalf("DeleteAllBacklog: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 tasks deleted, got %d", count)
	}

	// Verify backlog tasks from project1 were deleted
	task1, _ := taskRepo.GetByID(ctx, backlogTask1.ID)
	if task1 != nil {
		t.Error("expected backlog task 1 to be deleted")
	}

	task2, _ := taskRepo.GetByID(ctx, backlogTask2.ID)
	if task2 != nil {
		t.Error("expected backlog task 2 to be deleted")
	}

	// Verify active task still exists
	activeTaskResult, _ := taskRepo.GetByID(ctx, activeTask.ID)
	if activeTaskResult == nil {
		t.Error("expected active task to still exist")
	}

	// Verify backlog task from project2 was NOT deleted
	taskProject2, _ := taskRepo.GetByID(ctx, backlogTaskProject2.ID)
	if taskProject2 == nil {
		t.Error("expected backlog task from project2 to still exist")
	}
}

func TestTaskService_ActivateAllBacklog(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	// Create test projects
	project1 := &models.Project{Name: "Test Project 1"}
	if err := projectRepo.Create(ctx, project1); err != nil {
		t.Fatalf("failed to create project1: %v", err)
	}

	// Create backlog tasks for project1
	backlogTask1 := &models.Task{
		ProjectID: project1.ID,
		Title:     "Backlog Task 1",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	backlogTask2 := &models.Task{
		ProjectID: project1.ID,
		Title:     "Backlog Task 2",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "p",
	}

	taskRepo.Create(ctx, backlogTask1)
	taskRepo.Create(ctx, backlogTask2)

	// Activate all backlog tasks for project1
	count, err := svc.ActivateAllBacklog(ctx, project1.ID)
	if err != nil {
		t.Fatalf("ActivateAllBacklog: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 tasks activated, got %d", count)
	}

	// Verify tasks are now active with pending status
	task1, _ := taskRepo.GetByID(ctx, backlogTask1.ID)
	if task1 == nil {
		t.Fatal("expected backlog task 1 to exist")
	}
	if task1.Category != models.CategoryActive {
		t.Errorf("expected task 1 category to be active, got %s", task1.Category)
	}
	if task1.Status != models.StatusPending {
		t.Errorf("expected task 1 status to be pending, got %s", task1.Status)
	}

	task2, _ := taskRepo.GetByID(ctx, backlogTask2.ID)
	if task2 == nil {
		t.Fatal("expected backlog task 2 to exist")
	}
	if task2.Category != models.CategoryActive {
		t.Errorf("expected task 2 category to be active, got %s", task2.Category)
	}
	if task2.Status != models.StatusPending {
		t.Errorf("expected task 2 status to be pending, got %s", task2.Status)
	}

	// Verify tasks were submitted to worker
	submittedTasks := make(map[string]bool)
	timeout := time.After(100 * time.Millisecond)
	for i := 0; i < 2; i++ {
		select {
		case task := <-workerSvc.Submitted():
			submittedTasks[task.ID] = true
		case <-timeout:
			break
		}
	}

	if !submittedTasks[backlogTask1.ID] {
		t.Error("expected task 1 to be submitted to worker")
	}
	if !submittedTasks[backlogTask2.ID] {
		t.Error("expected task 2 to be submitted to worker")
	}
}

func TestTaskService_ActivateAllBacklog_ResumesGoalPausedByUserStop(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	goalSvc := NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc.SetTaskGoalService(goalSvc)
	ctx := context.Background()

	project := &models.Project{Name: "Stopped Goal Activation Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Stopped Backlog Goal",
		Category:  models.CategoryBacklog,
		Status:    models.StatusCancelled,
		Prompt:    "continue",
	}
	require.NoError(t, taskRepo.Create(ctx, task))
	goal, err := goalSvc.SetGoal(ctx, task.ID, "finish the objective", GoalOptions{Actor: "test"})
	require.NoError(t, err)
	require.NoError(t, goalSvc.PauseActiveGoalStoppedByUser(ctx, task.ID))

	count, err := svc.ActivateAllBacklog(ctx, project.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, models.CategoryActive, updated.Category)
	assert.Equal(t, models.StatusPending, updated.Status)

	select {
	case submitted := <-workerSvc.Submitted():
		assert.Equal(t, task.ID, submitted.ID)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected activated task to be submitted to worker")
	}

	resumed, err := goalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, resumed)
	assert.Equal(t, goal.GoalID, resumed.GoalID)
	assert.Equal(t, models.TaskGoalStatusActive, resumed.Status)
	assert.Equal(t, "resumed by user", resumed.Reason)
}

func TestWorkerService_Submit_SkipsChatTasks(t *testing.T) {
	workerSvc := newTestWorkerService(t)

	chatTask := models.Task{
		ID:       "chat-task-id",
		Title:    "Chat: hello",
		Category: models.CategoryChat,
		Status:   models.StatusPending,
		Prompt:   "hello",
	}

	// Submit chat task - should be rejected
	workerSvc.Submit(chatTask)

	// Verify it was NOT submitted to the channel
	select {
	case <-workerSvc.Submitted():
		t.Error("chat task should NOT be submitted to worker pool")
	case <-time.After(100 * time.Millisecond):
		// Expected - nothing submitted
	}
}

func TestTaskService_ExecuteBacklogTasks_All(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	// Create backlog tasks with different priorities
	task1 := &models.Task{
		ProjectID: "default",
		Title:     "Backlog Low",
		Category:  models.CategoryBacklog,
		Priority:  1,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	task2 := &models.Task{
		ProjectID: "default",
		Title:     "Backlog High",
		Category:  models.CategoryBacklog,
		Priority:  3,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	task3 := &models.Task{
		ProjectID: "default",
		Title:     "Backlog Urgent",
		Category:  models.CategoryBacklog,
		Priority:  4,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	taskRepo.Create(ctx, task1)
	taskRepo.Create(ctx, task2)
	taskRepo.Create(ctx, task3)

	// Execute all backlog tasks (priority=0 means all)
	tasks, submitted, err := svc.ExecuteBacklogTasks(ctx, "default", 0)
	if err != nil {
		t.Fatalf("ExecuteBacklogTasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Errorf("expected 3 tasks, got %d", len(tasks))
	}
	if submitted != 3 {
		t.Errorf("expected 3 submitted, got %d", submitted)
	}

	// Verify all tasks moved to active with pending status
	for _, taskID := range []string{task1.ID, task2.ID, task3.ID} {
		task, _ := taskRepo.GetByID(ctx, taskID)
		if task.Category != models.CategoryActive {
			t.Errorf("task %s: expected category=active, got %s", taskID, task.Category)
		}
		if task.Status != models.StatusPending {
			t.Errorf("task %s: expected status=pending, got %s", taskID, task.Status)
		}
	}

	// Drain submitted tasks from worker channel
	submittedIDs := make(map[string]bool)
	for i := 0; i < 3; i++ {
		select {
		case task := <-workerSvc.Submitted():
			submittedIDs[task.ID] = true
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("expected 3 tasks in channel, got %d", len(submittedIDs))
		}
	}
	if !submittedIDs[task1.ID] || !submittedIDs[task2.ID] || !submittedIDs[task3.ID] {
		t.Error("not all tasks were submitted to worker channel")
	}
}

func TestTaskService_ExecuteBacklogTasks_ResumesGoalPausedByUserStop(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	goalSvc := NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	svc.SetTaskGoalService(goalSvc)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Stopped Backlog Goal",
		Category:  models.CategoryBacklog,
		Priority:  3,
		Status:    models.StatusCancelled,
		Prompt:    "continue",
	}
	require.NoError(t, taskRepo.Create(ctx, task))
	goal, err := goalSvc.SetGoal(ctx, task.ID, "finish the objective", GoalOptions{Actor: "test"})
	require.NoError(t, err)
	require.NoError(t, goalSvc.PauseActiveGoalStoppedByUser(ctx, task.ID))

	_, submitted, err := svc.ExecuteBacklogTasks(ctx, "default", 0)
	require.NoError(t, err)
	assert.Equal(t, 1, submitted)
	resumed, err := goalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, resumed)
	assert.Equal(t, goal.GoalID, resumed.GoalID)
	assert.Equal(t, models.TaskGoalStatusActive, resumed.Status)
	assert.Equal(t, "resumed by user", resumed.Reason)
}

func TestTaskService_ExecuteBacklogTasks_FilterByPriority(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	// Create backlog tasks with different priorities
	taskLow := &models.Task{
		ProjectID: "default",
		Title:     "Low Priority",
		Category:  models.CategoryBacklog,
		Priority:  1,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	taskHigh := &models.Task{
		ProjectID: "default",
		Title:     "High Priority",
		Category:  models.CategoryBacklog,
		Priority:  3,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	taskUrgent := &models.Task{
		ProjectID: "default",
		Title:     "Urgent Priority",
		Category:  models.CategoryBacklog,
		Priority:  4,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	taskRepo.Create(ctx, taskLow)
	taskRepo.Create(ctx, taskHigh)
	taskRepo.Create(ctx, taskUrgent)

	// Execute only priority 3 (high) tasks
	tasks, submitted, err := svc.ExecuteBacklogTasks(ctx, "default", 3)
	if err != nil {
		t.Fatalf("ExecuteBacklogTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(tasks))
	}
	if submitted != 1 {
		t.Errorf("expected 1 submitted, got %d", submitted)
	}

	// Verify only the high priority task was moved
	high, _ := taskRepo.GetByID(ctx, taskHigh.ID)
	if high.Category != models.CategoryActive {
		t.Errorf("high priority task: expected category=active, got %s", high.Category)
	}

	// Low and urgent should remain in backlog
	low, _ := taskRepo.GetByID(ctx, taskLow.ID)
	if low.Category != models.CategoryBacklog {
		t.Errorf("low priority task: expected category=backlog, got %s", low.Category)
	}
	urgent, _ := taskRepo.GetByID(ctx, taskUrgent.ID)
	if urgent.Category != models.CategoryBacklog {
		t.Errorf("urgent priority task: expected category=backlog, got %s", urgent.Category)
	}

	// Drain submitted task
	select {
	case task := <-workerSvc.Submitted():
		if task.ID != taskHigh.ID {
			t.Errorf("expected submitted task ID=%s, got %s", taskHigh.ID, task.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected task to be submitted to worker channel")
	}
}

func TestTaskService_ExecuteBacklogTasks_ReExecutesFailedTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	// Create a failed backlog task
	task := &models.Task{
		ProjectID: "default",
		Title:     "Failed Task",
		Category:  models.CategoryBacklog,
		Priority:  2,
		Status:    models.StatusFailed,
		Prompt:    "p",
	}
	taskRepo.Create(ctx, task)

	// Execute all backlog tasks - should include failed tasks
	tasks, submitted, err := svc.ExecuteBacklogTasks(ctx, "default", 0)
	if err != nil {
		t.Fatalf("ExecuteBacklogTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task (failed), got %d", len(tasks))
	}
	if submitted != 1 {
		t.Errorf("expected 1 submitted, got %d", submitted)
	}

	// Verify task was moved and status reset
	updated, _ := taskRepo.GetByID(ctx, task.ID)
	if updated.Category != models.CategoryActive {
		t.Errorf("expected category=active, got %s", updated.Category)
	}
	if updated.Status != models.StatusPending {
		t.Errorf("expected status=pending, got %s", updated.Status)
	}
}

func TestTaskService_ExecuteBacklogTasks_SkipsRunningTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	// Create a running backlog task (edge case - shouldn't normally happen, but protects against it)
	task := &models.Task{
		ProjectID: "default",
		Title:     "Running Backlog Task",
		Category:  models.CategoryBacklog,
		Priority:  2,
		Status:    models.StatusRunning,
		Prompt:    "p",
	}
	taskRepo.Create(ctx, task)

	// Execute all - running tasks should NOT be included
	tasks, submitted, err := svc.ExecuteBacklogTasks(ctx, "default", 0)
	if err != nil {
		t.Fatalf("ExecuteBacklogTasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks (running excluded), got %d", len(tasks))
	}
	if submitted != 0 {
		t.Errorf("expected 0 submitted, got %d", submitted)
	}
}

func TestTaskService_ExecuteBacklogTasks_EmptyBacklog(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	// Execute with no backlog tasks
	tasks, submitted, err := svc.ExecuteBacklogTasks(ctx, "default", 0)
	if err != nil {
		t.Fatalf("ExecuteBacklogTasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}
	if submitted != 0 {
		t.Errorf("expected 0 submitted, got %d", submitted)
	}
}

func TestTaskService_ExecuteBacklogTasks_ReExecutesCompletedTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	// Create a completed task in backlog with urgent priority
	completedTask := &models.Task{
		ProjectID: "default",
		Title:     "Completed Urgent",
		Prompt:    "previously completed",
		Category:  models.CategoryBacklog,
		Status:    models.StatusCompleted,
		Priority:  4,
	}
	if err := taskRepo.Create(ctx, completedTask); err != nil {
		t.Fatalf("Create: %v", err)
	}

	tasks, submitted, err := svc.ExecuteBacklogTasks(ctx, "default", 4)
	if err != nil {
		t.Fatalf("ExecuteBacklogTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if submitted != 1 {
		t.Errorf("expected 1 submitted, got %d", submitted)
	}

	// Verify task was moved to active and reset to pending
	updated, _ := taskRepo.GetByID(ctx, completedTask.ID)
	if updated.Category != models.CategoryActive {
		t.Errorf("expected category active, got %q", updated.Category)
	}
	if updated.Status != models.StatusPending {
		t.Errorf("expected status pending, got %q", updated.Status)
	}
}

func TestTaskRepo_CountBacklogByPriority(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create backlog tasks with different priorities
	tasks := []*models.Task{
		{ProjectID: "default", Title: "Low 1", Category: models.CategoryBacklog, Priority: 1, Status: models.StatusPending, Prompt: "p"},
		{ProjectID: "default", Title: "Low 2", Category: models.CategoryBacklog, Priority: 1, Status: models.StatusPending, Prompt: "p"},
		{ProjectID: "default", Title: "Normal 1", Category: models.CategoryBacklog, Priority: 2, Status: models.StatusPending, Prompt: "p"},
		{ProjectID: "default", Title: "High 1", Category: models.CategoryBacklog, Priority: 3, Status: models.StatusFailed, Prompt: "p"},
		{ProjectID: "default", Title: "Running Skip", Category: models.CategoryBacklog, Priority: 3, Status: models.StatusRunning, Prompt: "p"},
		{ProjectID: "default", Title: "Active Skip", Category: models.CategoryActive, Priority: 4, Status: models.StatusPending, Prompt: "p"},
	}
	for _, task := range tasks {
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("Create task %s: %v", task.Title, err)
		}
	}

	counts, err := taskRepo.CountBacklogByPriority(ctx, "default")
	if err != nil {
		t.Fatalf("CountBacklogByPriority: %v", err)
	}

	if counts[1] != 2 {
		t.Errorf("expected 2 low priority tasks, got %d", counts[1])
	}
	if counts[2] != 1 {
		t.Errorf("expected 1 normal priority task, got %d", counts[2])
	}
	if counts[3] != 1 {
		t.Errorf("expected 1 high priority task (failed), got %d", counts[3])
	}
	if counts[4] != 0 {
		t.Errorf("expected 0 urgent priority tasks (active excluded), got %d", counts[4])
	}
}

func TestTaskService_Create_ChatTaskDoesNotAutoSubmit(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	ctx := context.Background()

	chatTask := &models.Task{
		ProjectID: "default",
		Title:     "Chat: test message",
		Category:  models.CategoryChat,
		Prompt:    "test message",
	}

	if err := svc.Create(ctx, chatTask); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify chat task was NOT auto-submitted to worker pool
	select {
	case <-workerSvc.Submitted():
		t.Error("chat task should NOT be auto-submitted to worker pool")
	case <-time.After(100 * time.Millisecond):
		// Expected - nothing submitted
	}
}

func TestWorkerService_ProjectConcurrency_Tracking(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	// Create a project with max_workers = 2
	maxW := 2
	p := &models.Project{
		Name:       "Limited Project",
		MaxWorkers: &maxW,
	}
	if err := projectRepo.Create(ctx, p); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	ws := NewWorkerService(nil, 0, projectRepo)

	// Initial state: 0 running
	if got := ws.TotalRunning(); got != 0 {
		t.Errorf("expected TotalRunning=0, got %d", got)
	}
	if got := ws.ProjectRunning(p.ID); got != 0 {
		t.Errorf("expected ProjectRunning=0, got %d", got)
	}

	// Acquire first slot - should succeed
	if !ws.tryAcquireProjectSlot(p.ID) {
		t.Error("expected first slot acquisition to succeed")
	}
	if got := ws.ProjectRunning(p.ID); got != 1 {
		t.Errorf("expected ProjectRunning=1, got %d", got)
	}
	if got := ws.TotalRunning(); got != 1 {
		t.Errorf("expected TotalRunning=1, got %d", got)
	}

	// Acquire second slot - should succeed (limit is 2)
	if !ws.tryAcquireProjectSlot(p.ID) {
		t.Error("expected second slot acquisition to succeed")
	}
	if got := ws.ProjectRunning(p.ID); got != 2 {
		t.Errorf("expected ProjectRunning=2, got %d", got)
	}

	// Acquire third slot - should FAIL (limit is 2)
	if ws.tryAcquireProjectSlot(p.ID) {
		t.Error("expected third slot acquisition to FAIL (project at capacity)")
	}
	if got := ws.ProjectRunning(p.ID); got != 2 {
		t.Errorf("expected ProjectRunning still 2, got %d", got)
	}

	// Release a slot
	ws.releaseProjectSlot(p.ID)
	if got := ws.ProjectRunning(p.ID); got != 1 {
		t.Errorf("expected ProjectRunning=1 after release, got %d", got)
	}
	if got := ws.TotalRunning(); got != 1 {
		t.Errorf("expected TotalRunning=1 after release, got %d", got)
	}

	// Now acquire should succeed again
	if !ws.tryAcquireProjectSlot(p.ID) {
		t.Error("expected slot acquisition to succeed after release")
	}
}

func TestWorkerService_ProjectConcurrency_NoLimit(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)

	ws := NewWorkerService(nil, 0, projectRepo)

	// Default project has no max_workers limit
	// Should always succeed
	for i := 0; i < 10; i++ {
		if !ws.tryAcquireProjectSlot("default") {
			t.Errorf("expected slot %d to succeed for unlimited project", i)
		}
	}
	if got := ws.ProjectRunning("default"); got != 10 {
		t.Errorf("expected ProjectRunning=10, got %d", got)
	}
	if got := ws.TotalRunning(); got != 10 {
		t.Errorf("expected TotalRunning=10, got %d", got)
	}

	// Release all
	for i := 0; i < 10; i++ {
		ws.releaseProjectSlot("default")
	}
	if got := ws.TotalRunning(); got != 0 {
		t.Errorf("expected TotalRunning=0 after all releases, got %d", got)
	}
}

func TestWorkerService_ProjectConcurrency_NilProjectRepo(t *testing.T) {
	// When projectRepo is nil, all slots should be allowed (no limit enforcement)
	ws := NewWorkerService(nil, 0, nil)

	for i := 0; i < 5; i++ {
		if !ws.tryAcquireProjectSlot("any-project") {
			t.Errorf("expected slot %d to succeed with nil projectRepo", i)
		}
	}
	if got := ws.TotalRunning(); got != 5 {
		t.Errorf("expected TotalRunning=5, got %d", got)
	}
}

func TestWorkerService_ProjectConcurrency_MultipleProjects(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	// Project A: max 1 worker
	maxA := 1
	pA := &models.Project{Name: "Project A", MaxWorkers: &maxA}
	projectRepo.Create(ctx, pA)

	// Project B: max 3 workers
	maxB := 3
	pB := &models.Project{Name: "Project B", MaxWorkers: &maxB}
	projectRepo.Create(ctx, pB)

	ws := NewWorkerService(nil, 0, projectRepo)

	// Fill up project A
	if !ws.tryAcquireProjectSlot(pA.ID) {
		t.Error("expected project A first slot to succeed")
	}
	if ws.tryAcquireProjectSlot(pA.ID) {
		t.Error("expected project A second slot to FAIL")
	}

	// Project B should still work
	if !ws.tryAcquireProjectSlot(pB.ID) {
		t.Error("expected project B first slot to succeed")
	}
	if !ws.tryAcquireProjectSlot(pB.ID) {
		t.Error("expected project B second slot to succeed")
	}
	if !ws.tryAcquireProjectSlot(pB.ID) {
		t.Error("expected project B third slot to succeed")
	}
	if ws.tryAcquireProjectSlot(pB.ID) {
		t.Error("expected project B fourth slot to FAIL")
	}

	// Total running should be 4 (1 from A + 3 from B)
	if got := ws.TotalRunning(); got != 4 {
		t.Errorf("expected TotalRunning=4, got %d", got)
	}
}

func TestWorkerService_DispatchPrunesNonPendingTasks(t *testing.T) {
	// When dispatchNext scans the queue, tasks whose status changed to
	// completed should be pruned from the queue instead of being dispatched.
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	ws := NewWorkerService(nil, 1, projectRepo)
	ws.SetTaskRepo(taskRepo)

	// Create a task and mark it completed
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Already Completed Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusCompleted); err != nil {
		t.Fatalf("failed to update status: %v", err)
	}

	// Add directly to queue and pending map
	ws.mu.Lock()
	ws.pending[task.ID] = true
	ws.queue = append(ws.queue, *task)
	ws.mu.Unlock()

	// Start the service — dispatchNext will be called and should prune the stale task
	ws.Start(ctx)
	defer ws.Stop()
	ws.dispatchNext()

	ws.mu.Lock()
	inPending := ws.pending[task.ID]
	queueLen := len(ws.queue)
	ws.mu.Unlock()

	if inPending {
		t.Error("expected completed task to be pruned from pending map")
	}
	if queueLen != 0 {
		t.Errorf("expected empty queue after pruning, got %d", queueLen)
	}
}

func TestWorkerService_DispatchPrunesNonActiveCategoryTasks(t *testing.T) {
	// When a task's category is changed away from active/scheduled,
	// dispatchNext should prune it from the queue.
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	ws := NewWorkerService(nil, 1, projectRepo)
	ws.SetTaskRepo(taskRepo)

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Moved Away Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	if err := taskRepo.UpdateCategory(ctx, task.ID, models.CategoryCompleted); err != nil {
		t.Fatalf("failed to update category: %v", err)
	}

	// Add directly to queue
	ws.mu.Lock()
	ws.pending[task.ID] = true
	ws.queue = append(ws.queue, *task)
	ws.mu.Unlock()

	ws.Start(ctx)
	defer ws.Stop()
	ws.dispatchNext()

	ws.mu.Lock()
	inPending := ws.pending[task.ID]
	queueLen := len(ws.queue)
	ws.mu.Unlock()

	if inPending {
		t.Error("expected non-active-category task to be pruned from pending map")
	}
	if queueLen != 0 {
		t.Errorf("expected empty queue after pruning, got %d", queueLen)
	}
}
func TestTaskService_ActivateAllBacklogStartsSwarmPlanner(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	workerSvc := newTestWorkerService(t)
	taskSvc := NewTaskService(taskRepo, nil, workerSvc)
	swarmSvc := NewSwarmService(taskSvc, taskRepo, nil, workerSvc)
	taskSvc.SetSwarmService(swarmSvc)
	ctx := context.Background()

	project := &models.Project{Name: "Swarm Bulk Activation"}
	require.NoError(t, projectRepo.Create(ctx, project))
	startImmediately := false
	parent, err := swarmSvc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: project.ID, Title: "Deferred swarm", Prompt: "plan later", Category: models.CategoryBacklog, MaxWorkers: 2, ReviewerEnabled: true, MergerEnabled: true, StartImmediately: &startImmediately})
	require.NoError(t, err)

	count, err := taskSvc.ActivateAllBacklog(ctx, project.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assertPlannerSubmittedForParent(t, taskRepo, workerSvc, parent.ID)
}

func TestTaskService_ExecuteBacklogTasksStartsSwarmPlanner(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	taskSvc := NewTaskService(taskRepo, nil, workerSvc)
	swarmSvc := NewSwarmService(taskSvc, taskRepo, nil, workerSvc)
	taskSvc.SetSwarmService(swarmSvc)
	ctx := context.Background()

	startImmediately := false
	parent, err := swarmSvc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Deferred priority swarm", Prompt: "plan later", Category: models.CategoryBacklog, Priority: 3, MaxWorkers: 2, ReviewerEnabled: true, MergerEnabled: true, StartImmediately: &startImmediately})
	require.NoError(t, err)

	_, submitted, err := taskSvc.ExecuteBacklogTasks(ctx, "default", 3)
	require.NoError(t, err)
	assert.Equal(t, 1, submitted)
	assertPlannerSubmittedForParent(t, taskRepo, workerSvc, parent.ID)
}

func TestTaskService_ExecuteTasksByTagsStartsSwarmPlanner(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	taskSvc := NewTaskService(taskRepo, nil, workerSvc)
	swarmSvc := NewSwarmService(taskSvc, taskRepo, nil, workerSvc)
	taskSvc.SetSwarmService(swarmSvc)
	ctx := context.Background()

	startImmediately := false
	parent, err := swarmSvc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Deferred tagged swarm", Prompt: "plan later", Category: models.CategoryBacklog, Tag: models.TagBug, Priority: 4, MaxWorkers: 2, ReviewerEnabled: true, MergerEnabled: true, StartImmediately: &startImmediately})
	require.NoError(t, err)

	_, submitted, err := taskSvc.ExecuteTasksByTags(ctx, []models.TaskTag{models.TagBug}, "default", 1, false)
	require.NoError(t, err)
	assert.Equal(t, 1, submitted)
	assertPlannerSubmittedForParent(t, taskRepo, workerSvc, parent.ID)
}

func TestTaskService_UpdateStatusPendingStartsSwarmPlanner(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	taskSvc := NewTaskService(taskRepo, nil, workerSvc)
	swarmSvc := NewSwarmService(taskSvc, taskRepo, nil, workerSvc)
	taskSvc.SetSwarmService(swarmSvc)
	ctx := context.Background()

	startImmediately := false
	parent, err := swarmSvc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Active failed swarm", Prompt: "plan later", Category: models.CategoryBacklog, MaxWorkers: 2, ReviewerEnabled: true, MergerEnabled: true, StartImmediately: &startImmediately})
	require.NoError(t, err)
	require.NoError(t, taskRepo.UpdateCategory(ctx, parent.ID, models.CategoryActive))
	require.NoError(t, taskRepo.UpdateStatus(ctx, parent.ID, models.StatusFailed))

	require.NoError(t, taskSvc.UpdateStatus(ctx, parent.ID, models.StatusPending))
	assertPlannerSubmittedForParent(t, taskRepo, workerSvc, parent.ID)
}

func assertPlannerSubmittedForParent(t *testing.T, taskRepo *repository.TaskRepo, workerSvc *WorkerService, parentID string) {
	t.Helper()
	planner, err := taskRepo.FindSwarmChildByRole(context.Background(), parentID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	assert.Equal(t, models.CategoryActive, planner.Category)
	assert.Equal(t, models.StatusPending, planner.Status)

	storedParent, err := taskRepo.GetByID(context.Background(), parentID)
	require.NoError(t, err)
	require.NotNil(t, storedParent)
	assert.Equal(t, models.CategoryActive, storedParent.Category)
	assert.Equal(t, models.StatusBlocked, storedParent.Status)

	select {
	case submitted := <-workerSvc.Submitted():
		assert.Equal(t, planner.ID, submitted.ID)
		assert.NotEqual(t, parentID, submitted.ID, "swarm parent must not be submitted as a normal task")
	case <-time.After(time.Second):
		t.Fatal("expected planner to be submitted")
	}

	select {
	case submitted := <-workerSvc.Submitted():
		t.Fatalf("unexpected extra submitted task %s", submitted.ID)
	case <-time.After(100 * time.Millisecond):
	}
}

package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/lifecycle"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

// TestWorkerService_TaskClaimedBeforeLifecycleHooks verifies the fix for the
// stale-kanban bug: when the worker picks up a pending task, it must claim
// (set status -> running) BEFORE invoking lifecycle hooks (route_task /
// before_run). Otherwise the Tasks page shows the task as "queued" while
// expensive lifecycle hooks run (which may invoke an LLM), and the dropzone
// only updates after the user manually refreshes.
//
// The test:
//  1. Sets up a worker with a lifecycle runner whose route_task invoker
//     blocks until released, then reads the task's status from the DB.
//  2. Submits a pending task.
//  3. Asserts that, by the time the route_task invoker is called, the task
//     status is already "running" — proving the claim happened first.
//  4. Asserts that a TaskStatusChanged(pending -> running) event was
//     published before the lifecycle hook fired.
func TestWorkerService_TaskAdmissionQueuesFollowupAfterPersistedClaim(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	inputRepo := repository.NewThreadInputRepo(db)
	project := &models.Project{Name: "task admission claim race"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agent := &models.LLMConfig{Name: "Admission Agent", Provider: models.ProviderTest, Model: "test-model", MaxTokens: 4096, AuthMethod: models.AuthMethodCLI, IsDefault: true}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Claimed ordinary task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "stored original prompt", AgentID: &agent.ID}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	caller := testutil.NewMockLLMCaller()
	caller.Response = "ordinary run complete"
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, nil, repository.NewAttachmentRepo(db))
	llmSvc.SetLLMCaller(caller)
	worker := NewWorkerService(llmSvc, 1, projectRepo)
	worker.SetLLMConfigRepo(llmConfigRepo)
	worker.SetTaskRepo(taskRepo)
	worker.SetExecutionRepo(execRepo)
	claimed := make(chan struct{})
	releaseClaim := make(chan struct{})
	worker.afterOrdinaryTaskClaim = func(models.Task) {
		close(claimed)
		<-releaseClaim
	}
	worker.Start(ctx)
	defer worker.Stop()
	worker.Submit(*task)

	select {
	case <-claimed:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for persisted task claim")
	}
	for _, content := range []string{"first follow-up", "second follow-up"} {
		exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: content, IsFollowup: true}
		input := &models.ThreadInput{AgentConfigID: agent.ID, Content: content, Source: models.TaskOriginWeb}
		started, err := execRepo.CreateDirectTaskFollowupOrQueue(ctx, exec, input)
		if err != nil {
			t.Fatalf("admit %q: %v", content, err)
		}
		if started {
			t.Fatalf("follow-up %q started behind persisted ordinary-task claim", content)
		}
	}
	if caller.CallCount() != 0 {
		t.Fatalf("model called while worker was paused before execution insertion: %d", caller.CallCount())
	}
	execs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil || len(execs) != 0 {
		t.Fatalf("expected no execution before releasing claim barrier, got %#v err=%v", execs, err)
	}
	pending, err := inputRepo.ListPendingForTask(ctx, task.ID)
	if err != nil || len(pending) != 2 || pending[0].Content != "first follow-up" || pending[1].Content != "second follow-up" {
		t.Fatalf("expected FIFO follow-ups behind claim, got %#v err=%v", pending, err)
	}

	close(releaseClaim)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stored, getErr := taskRepo.GetByID(ctx, task.ID)
		if getErr == nil && stored != nil && stored.Status == models.StatusCompleted && caller.CallCount() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stored, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil || stored.Status != models.StatusCompleted || caller.CallCount() != 1 {
		t.Fatalf("ordinary run did not terminalize once: task=%#v calls=%d err=%v", stored, caller.CallCount(), err)
	}
	execs, err = execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil || len(execs) != 1 || execs[0].PromptSent != task.Prompt || execs[0].Status != models.ExecCompleted {
		t.Fatalf("expected one completed original-prompt execution, got %#v err=%v", execs, err)
	}

	promoted := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: pending[0].Content, IsFollowup: true}
	if err := inputRepo.ClaimQueuedForTaskExecution(ctx, pending[0].ID, promoted); err != nil {
		t.Fatalf("promote FIFO head: %v", err)
	}
	if err := inputRepo.ClaimQueuedForTaskExecution(ctx, pending[0].ID, &models.Execution{TaskID: task.ID, Status: models.ExecRunning}); err != repository.ErrInputNotPending {
		t.Fatalf("expected exactly-once promotion rejection, got %v", err)
	}
	execs, err = execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil || len(execs) != 2 || execs[1].ID != promoted.ID || execs[1].PromptSent != "first follow-up" {
		t.Fatalf("expected exactly one FIFO promotion, got %#v err=%v", execs, err)
	}
	remaining, err := inputRepo.ListPendingForTask(ctx, task.ID)
	if err != nil || len(remaining) != 1 || remaining[0].Content != "second follow-up" {
		t.Fatalf("expected second follow-up to remain pending, got %#v err=%v", remaining, err)
	}
}

func TestWorkerService_TaskAdmissionRunsFirstTurnWhenFollowupArrivesBeforeClaim(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	inputRepo := repository.NewThreadInputRepo(db)
	project := &models.Project{Name: "follow-up before task claim"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agent := &models.LLMConfig{Name: "Reverse Admission Agent", Provider: models.ProviderTest, Model: "test-model", MaxTokens: 4096, AuthMethod: models.AuthMethodCLI, IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	task := &models.Task{ProjectID: project.ID, Title: "Fresh ordinary task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "stored original prompt", AgentID: &agent.ID}
	require.NoError(t, taskRepo.Create(ctx, task))

	caller := testutil.NewMockLLMCaller()
	caller.Response = "ordinary run complete"
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, nil, repository.NewAttachmentRepo(db))
	llmSvc.SetLLMCaller(caller)
	worker := NewWorkerService(llmSvc, 1, projectRepo)
	worker.SetLLMConfigRepo(llmConfigRepo)
	worker.SetTaskRepo(taskRepo)
	worker.SetExecutionRepo(execRepo)
	beforeClaim := make(chan struct{})
	releaseClaim := make(chan struct{})
	worker.beforeOrdinaryTaskClaim = func(models.Task) {
		close(beforeClaim)
		<-releaseClaim
	}
	worker.Start(ctx)
	defer worker.Stop()
	worker.Submit(*task)

	select {
	case <-beforeClaim:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for pre-claim barrier")
	}
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: "follow-up before claim", Source: models.TaskOriginWeb}
	require.NoError(t, inputRepo.CreateQueued(ctx, queued))
	close(releaseClaim)

	require.Eventually(t, func() bool {
		stored, err := taskRepo.GetByID(ctx, task.ID)
		return err == nil && stored != nil && stored.Status == models.StatusCompleted && caller.CallCount() == 1
	}, 5*time.Second, 10*time.Millisecond)
	execs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1)
	require.Equal(t, task.Prompt, execs[0].PromptSent)
	require.Equal(t, models.ExecCompleted, execs[0].Status)

	pending, err := inputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, queued.ID, pending[0].ID)
	promoted := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: queued.Content, IsFollowup: true}
	require.NoError(t, inputRepo.ClaimQueuedForTaskExecution(ctx, queued.ID, promoted))
	require.ErrorIs(t, inputRepo.ClaimQueuedForTaskExecution(ctx, queued.ID, &models.Execution{TaskID: task.ID, Status: models.ExecRunning}), repository.ErrInputNotPending)
	execs, err = execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 2)
	require.Equal(t, queued.Content, execs[1].PromptSent)
}

func TestWorkerService_TaskClaimedBeforeLifecycleHooks(t *testing.T) {
	db := testutil.NewTestDB(t)
	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	// Subscribe to events BEFORE dispatching.
	sub, err := broadcaster.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer broadcaster.Unsubscribe(sub)

	// Project + agent fixtures.
	project := &models.Project{Name: "claim-before-hooks"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}
	agent := &models.LLMConfig{
		Name:       "Test Agent",
		Provider:   models.ProviderTest,
		Model:      "test-model",
		MaxTokens:  4096,
		AuthMethod: models.AuthMethodCLI,
		IsDefault:  true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("Create agent: %v", err)
	}

	// Pending active task.
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Claim Before Hooks",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
		AgentID:   &agent.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// LLM service with a mock caller that returns instantly after the
	// lifecycle hook releases the worker.
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, nil, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())

	// Build a lifecycle runner whose route_task invoker captures the task
	// status visible at hook invocation time. The invoker blocks briefly
	// to simulate a slow LLM-backed hook so the bug (status still pending
	// during hook execution) would be visible without the fix.
	hookEntered := make(chan struct{}, 1)
	releaseHook := make(chan struct{})
	var (
		mu                sync.Mutex
		statusAtHookEntry models.TaskStatus
	)
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{
		ID:             "route-hook",
		When:           models.LifecycleRouteTask,
		SkillKey:       "route_task",
		OutputContract: models.OutputContractSelectedSkills,
		Blocking:       true,
		Enabled:        true,
	}}}
	invoker := routeHookInvokerFunc(func(invCtx context.Context, _ models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		// Snapshot status visible to the hook.
		dbTask, _ := taskRepo.GetByID(invCtx, in.TaskID)
		mu.Lock()
		if dbTask != nil {
			statusAtHookEntry = dbTask.Status
		}
		mu.Unlock()

		// Signal the test that we entered the hook, then block until released.
		select {
		case hookEntered <- struct{}{}:
		default:
		}
		<-releaseHook

		// Return an empty selected_skills payload — route_task is optional.
		return json.RawMessage(`{"skills":[],"confidence":0.0}`), nil
	})
	runner := lifecycle.NewRunner(store, invoker, nil)

	ws := NewWorkerService(llmSvc, 10, projectRepo)
	ws.SetLLMConfigRepo(llmConfigRepo)
	ws.SetTaskRepo(taskRepo)
	ws.SetLifecycleRunner(runner)
	ws.SetLifecycleRepo(repository.NewLifecycleRepo(db))
	ws.SetExecutionRepo(execRepo)
	ws.Start(ctx)
	defer ws.Stop()

	ws.Submit(*task)

	// Wait until the route_task hook is invoked.
	select {
	case <-hookEntered:
	case <-time.After(5 * time.Second):
		close(releaseHook)
		t.Fatal("timed out waiting for route_task hook to be invoked")
	}

	// At hook entry, the task MUST already be in running status. Before
	// the fix it would still be 'pending', causing the kanban board to
	// show it stuck in the queued sub-zone.
	mu.Lock()
	got := statusAtHookEntry
	mu.Unlock()
	if got != models.StatusRunning {
		close(releaseHook)
		t.Fatalf("expected task to be running when lifecycle hook fires, got %q", got)
	}

	// A TaskStatusChanged(pending -> running) event must have already been
	// published by the pre-claim, BEFORE the lifecycle hook returned. This
	// is what drives the live kanban update.
	var sawRunningEvent bool
drain:
	for {
		select {
		case ev := <-sub:
			if ev.Type == events.TaskStatusChanged && ev.TaskID == task.ID &&
				ev.OldStatus == string(models.StatusPending) &&
				ev.Status == string(models.StatusRunning) {
				sawRunningEvent = true
				break drain
			}
		case <-time.After(500 * time.Millisecond):
			break drain
		}
	}
	if !sawRunningEvent {
		close(releaseHook)
		t.Fatal("expected TaskStatusChanged(pending->running) event to be published before lifecycle hook completed")
	}

	// Release the hook so the worker can finish the task and shut down cleanly.
	close(releaseHook)
}

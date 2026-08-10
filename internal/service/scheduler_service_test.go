package service

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSchedulerService_CheckDueTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	agentRepo := repository.NewAgentRepo(db)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	agent := &models.Agent{Name: "Scheduled Primary Agent", Key: "scheduled-primary-agent", SystemPrompt: "Run scheduled work", Model: "inherit", Scope: models.AgentScopeGlobal, Enabled: true, SelectableAsPrimary: true, GeneratedStatus: models.AgentStatusUserEdited}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create primary agent: %v", err)
	}

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create a scheduled task with a past due schedule
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Due Task",
		Category:          models.CategoryScheduled,
		Status:            models.StatusPending,
		Prompt:            "test",
		AgentDefinitionID: &agent.ID,
	}
	taskRepo.Create(ctx, task)

	now := time.Now().UTC()
	sched := &models.Schedule{
		TaskID:              task.ID,
		RunAt:               now.Add(-1 * time.Minute),
		RepeatType:          models.RepeatOnce,
		RepeatInterval:      1,
		Enabled:             true,
		ClearContextOnStart: true,
	}
	scheduleRepo.Create(ctx, sched)

	// Run checkDueTasks
	svc.checkDueTasks(ctx)

	// Verify task was submitted
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected submitted task ID=%s, got %s", task.ID, submitted.ID)
		}
		if submitted.AgentDefinitionID == nil || *submitted.AgentDefinitionID != agent.ID {
			t.Errorf("expected submitted primary agent %s, got %v", agent.ID, submitted.AgentDefinitionID)
		}
		if !submitted.StartsNewContext {
			t.Error("expected clear-context schedule to mark the submitted run as a new context")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected due task to be submitted")
	}

	// Verify schedule was marked as ran
	updated, _ := scheduleRepo.GetByID(ctx, sched.ID)
	if updated.LastRun == nil {
		t.Error("expected LastRun to be set after checkDueTasks")
	}
}

func TestSchedulerService_CheckDueTasksClearsCancellationRequestBeforeSubmit(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()
	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	task := &models.Task{
		ProjectID: "default",
		Title:     "Due task after stop",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	require.NoError(t, taskRepo.Create(ctx, task))
	workerSvc.MarkCancellationRequested(task.ID)
	require.True(t, workerSvc.IsCancellationRequested(task.ID))

	now := time.Now().UTC()
	sched := &models.Schedule{TaskID: task.ID, RunAt: now.Add(-time.Minute), RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
	require.NoError(t, scheduleRepo.Create(ctx, sched))

	svc.checkDueTasks(ctx)

	select {
	case submitted := <-workerSvc.Submitted():
		require.Equal(t, task.ID, submitted.ID)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected due task to be submitted")
	}
	require.False(t, workerSvc.IsCancellationRequested(task.ID), "due scheduled submission should clear stale cancellation marker")
}

func TestSchedulerService_CheckDueTasksSubmitsOneTimeScheduleCreatedForCompletedScheduledTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()
	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	task := &models.Task{
		ProjectID: "default",
		Title:     "Completed scheduled task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusCompleted,
		Prompt:    "test",
	}
	require.NoError(t, taskRepo.Create(ctx, task))
	actionSvc := NewScheduleActionService(taskRepo, scheduleRepo)
	result, err := actionSvc.Create(ctx, "default", ScheduleTaskRequest{TaskID: task.ID, Time: "09:30", Repeat: "once"})
	require.NoError(t, err)
	require.Equal(t, models.StatusPending, result.Task.Status)

	dueAt := time.Now().UTC().Add(-time.Minute)
	result.Schedule.RunAt = dueAt
	result.Schedule.NextRun = &dueAt
	require.NoError(t, scheduleRepo.Update(ctx, result.Schedule))

	svc.checkDueTasks(ctx)

	select {
	case submitted := <-workerSvc.Submitted():
		require.Equal(t, task.ID, submitted.ID)
		require.Equal(t, models.StatusPending, submitted.Status)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected due one-time schedule to be submitted")
	}
	updatedSchedule, err := scheduleRepo.GetByID(ctx, result.Schedule.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedSchedule.LastRun)
	require.Nil(t, updatedSchedule.NextRun)
}

func TestSchedulerService_MalformedScheduleDoesNotBlockLaterValidSchedule(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()
	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	now := time.Now().UTC()

	malformedTask := &models.Task{ProjectID: "default", Title: "Malformed schedule", Category: models.CategoryScheduled, Status: models.StatusPending, Prompt: "bad"}
	validTask := &models.Task{ProjectID: "default", Title: "Valid schedule", Category: models.CategoryScheduled, Status: models.StatusPending, Prompt: "good"}
	if err := taskRepo.Create(ctx, malformedTask); err != nil {
		t.Fatal(err)
	}
	if err := taskRepo.Create(ctx, validTask); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schedules
		(id, task_id, run_at, repeat_type, repeat_interval, enabled, next_run)
		VALUES (?, ?, ?, ?, ?, 1, ?)`,
		"corrupt-interval", malformedTask.ID, now.Add(-2*time.Minute), models.RepeatSeconds, 9223372036854775807, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	valid := &models.Schedule{TaskID: validTask.ID, RunAt: now.Add(-time.Minute), RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
	if err := scheduleRepo.Create(ctx, valid); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		svc.checkDueTasks(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("malformed schedule blocked processing of later valid schedules")
	}

	persisted, err := scheduleRepo.GetByID(ctx, valid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.LastRun == nil {
		t.Fatal("expected later valid schedule to be processed")
	}
	corrupt, err := scheduleRepo.GetByID(ctx, "corrupt-interval")
	if err != nil {
		t.Fatal(err)
	}
	if corrupt.LastRun != nil {
		t.Fatal("corrupt schedule should not be dispatched or marked as run")
	}
}

func TestSchedulerService_CheckDueTasks_PersistsContextBoundaryAfterWorkerReload(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	model := &models.LLMConfig{Name: "Scheduled context model", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	if err := llmConfigRepo.Create(ctx, model); err != nil {
		t.Fatalf("create model: %v", err)
	}
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	mockLLM := testutil.NewMockLLMCaller()
	mockLLM.Response = "scheduled run complete"
	mockLLM.TextOnly = mockLLM.Response
	llmSvc.SetLLMCaller(mockLLM)

	workerSvc := NewWorkerService(llmSvc, 1, projectRepo)
	workerSvc.SetTaskRepo(taskRepo)
	workerSvc.SetLLMConfigRepo(llmConfigRepo)
	workerSvc.SetExecutionRepo(execRepo)
	workerSvc.Start(ctx)
	defer workerSvc.Stop()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Clear context after worker reload",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "run with a fresh context",
		AgentID:   &model.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	now := time.Now().UTC()
	schedule := &models.Schedule{
		TaskID:              task.ID,
		RunAt:               now.Add(-time.Minute),
		RepeatType:          models.RepeatOnce,
		RepeatInterval:      1,
		Enabled:             true,
		ClearContextOnStart: true,
	}
	if err := scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	NewSchedulerService(scheduleRepo, taskRepo, workerSvc).checkDueTasks(ctx)

	var execution *models.Execution
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		executions, err := execRepo.ListByTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("list executions: %v", err)
		}
		if len(executions) > 0 && executions[0].Status != models.ExecRunning {
			execution = &executions[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if execution == nil {
		t.Fatal("timed out waiting for scheduled execution")
	}
	if !execution.StartsNewContext {
		t.Fatal("expected persisted scheduled execution to start a new context after worker task reload")
	}
}

func TestSchedulerService_CheckDueTasks_SwarmPlannerPersistsContextBoundary(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	model := &models.LLMConfig{Name: "Scheduled swarm context model", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	if err := llmConfigRepo.Create(ctx, model); err != nil {
		t.Fatalf("create model: %v", err)
	}
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	mockLLM := testutil.NewMockLLMCaller()
	mockLLM.Response = `{"workers":[{"title":"Inspect","prompt":"Inspect the task","worker_kind":"analysis","required":true,"read_only":true}]}`
	mockLLM.TextOnly = mockLLM.Response
	llmSvc.SetLLMCaller(mockLLM)

	workerSvc := NewWorkerService(llmSvc, 1, projectRepo)
	workerSvc.SetTaskRepo(taskRepo)
	workerSvc.SetLLMConfigRepo(llmConfigRepo)
	workerSvc.SetExecutionRepo(execRepo)
	workerSvc.Start(ctx)
	defer workerSvc.Stop()
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	swarmSvc := NewSwarmService(taskSvc, taskRepo, nil, workerSvc)
	taskSvc.SetSwarmService(swarmSvc)

	parent, err := swarmSvc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{
		ProjectID: "default", Title: "Scheduled swarm context", Prompt: "Plan on schedule", Category: models.CategoryBacklog,
		MaxWorkers: 2, ReviewerEnabled: true, MergerEnabled: true,
	})
	if err != nil {
		t.Fatalf("create swarm: %v", err)
	}
	now := time.Now().UTC()
	schedule := &models.Schedule{
		TaskID: parent.ID, RunAt: now.Add(-time.Minute), RepeatType: models.RepeatOnce, RepeatInterval: 1,
		Enabled: true, ClearContextOnStart: true,
	}
	if err := scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	scheduler := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	scheduler.SetSwarmPlannerStarter(swarmSvc)
	scheduler.checkDueTasks(ctx)

	planner, err := taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("load planner: planner=%#v err=%v", planner, err)
	}
	var execution *models.Execution
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		executions, listErr := execRepo.ListByTask(ctx, planner.ID)
		if listErr != nil {
			t.Fatalf("list planner executions: %v", listErr)
		}
		if len(executions) > 0 {
			execution = &executions[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if execution == nil {
		t.Fatal("timed out waiting for planner execution")
	}
	if !execution.StartsNewContext {
		t.Fatal("expected scheduled swarm planner execution to persist a new-context boundary")
	}
}

func TestSchedulerService_CheckDueTasks_StartsPlannerForSwarmParent(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	taskSvc := NewTaskService(taskRepo, nil, workerSvc)
	swarmSvc := NewSwarmService(taskSvc, taskRepo, nil, workerSvc)
	taskSvc.SetSwarmService(swarmSvc)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	svc.SetSwarmPlannerStarter(swarmSvc)
	parent, err := swarmSvc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Scheduled swarm", Prompt: "Plan on schedule", Category: models.CategoryBacklog, MaxWorkers: 2, ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatalf("CreateSwarmTask: %v", err)
	}
	now := time.Now().UTC()
	sched := &models.Schedule{TaskID: parent.ID, RunAt: now.Add(-1 * time.Minute), RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
	if err := scheduleRepo.Create(ctx, sched); err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	svc.checkDueTasks(ctx)

	planner, err := taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("expected scheduler to create planner child, planner=%#v err=%v", planner, err)
	}
	if planner.Category != models.CategoryActive || planner.Status != models.StatusPending {
		t.Fatalf("planner not runnable: category=%s status=%s", planner.Category, planner.Status)
	}
	storedParent, err := taskRepo.GetByID(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedParent.Category != models.CategoryActive {
		t.Fatalf("expected scheduled swarm parent to become active, got %s", storedParent.Category)
	}
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != planner.ID {
			t.Fatalf("expected scheduler to submit planner %s, got %s", planner.ID, submitted.ID)
		}
	default:
		t.Fatal("expected planner to be submitted")
	}
	updated, err := scheduleRepo.GetByID(ctx, sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastRun == nil {
		t.Fatal("expected swarm schedule to be marked as ran")
	}
}

func TestSchedulerService_CheckDueTasks_SubmitsMemoryConsolidationTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	task := &models.Task{
		ProjectID: "default",
		Title:     "System: Memory Consolidation",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "Run scheduled memory consolidation for this project.",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	now := time.Now().UTC()
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now.Add(-1 * time.Minute),
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}
	if err := scheduleRepo.Create(ctx, sched); err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	svc.checkDueTasks(ctx)

	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected submitted task ID=%s, got %s", task.ID, submitted.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected memory consolidation task to be submitted through normal worker path")
	}

	updated, err := scheduleRepo.GetByID(ctx, sched.ID)
	if err != nil {
		t.Fatalf("reload schedule: %v", err)
	}
	if updated.LastRun == nil {
		t.Fatal("expected LastRun to be set")
	}
	if updated.NextRun == nil || !updated.NextRun.After(now) {
		t.Fatalf("expected future NextRun, got %v", updated.NextRun)
	}
}

func TestSchedulerService_CheckDueTasks_SkipsRunningTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create a task that's already running
	task := &models.Task{
		ProjectID: "default",
		Title:     "Running Task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusRunning,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task)

	now := time.Now().UTC()
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now.Add(-1 * time.Minute),
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}
	scheduleRepo.Create(ctx, sched)

	svc.checkDueTasks(ctx)

	select {
	case <-workerSvc.Submitted():
		t.Error("running task should not be submitted again")
	case <-time.After(100 * time.Millisecond):
		// Expected - not submitted
	}
}

func TestSchedulerService_CheckActiveTasks_DoesNotSubmitBehindDirectFollowup(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	task := &models.Task{ProjectID: "default", Title: "Follow-up owns admission", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "stored original prompt"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	exec := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "active direct follow-up", IsFollowup: true}
	started, err := repository.NewExecutionRepo(db).CreateDirectTaskFollowupOrQueue(ctx, exec, &models.ThreadInput{Content: exec.PromptSent, Source: models.TaskOriginWeb})
	if err != nil || !started {
		t.Fatalf("admit direct follow-up: started=%v err=%v", started, err)
	}

	svc := NewSchedulerService(repository.NewScheduleRepo(db), taskRepo, workerSvc)
	for i := 0; i < 5; i++ {
		svc.checkActiveTasks(ctx)
	}
	select {
	case submitted := <-workerSvc.Submitted():
		t.Fatalf("scheduler resubmitted original prompt behind direct follow-up: %#v", submitted)
	case <-time.After(100 * time.Millisecond):
	}
	execs, err := repository.NewExecutionRepo(db).ListByTaskChronological(ctx, task.ID)
	if err != nil || len(execs) != 1 || execs[0].ID != exec.ID || execs[0].PromptSent != "active direct follow-up" {
		t.Fatalf("expected only direct follow-up execution, got %#v err=%v", execs, err)
	}
}

func TestSchedulerService_CheckActiveTasks_DoesNotRecoverStaleQueuedFollowup(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	task := &models.Task{ProjectID: "default", Title: "long follow-up", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "stored original prompt"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	exec := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "long direct follow-up", IsFollowup: true}
	started, err := repository.NewExecutionRepo(db).CreateDirectTaskFollowupOrQueue(ctx, exec, &models.ThreadInput{Content: exec.PromptSent})
	if err != nil || !started {
		t.Fatalf("admit follow-up: started=%v err=%v", started, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET updated_at = datetime('now', '-15 minutes') WHERE id = ?`, task.ID); err != nil {
		t.Fatal(err)
	}

	NewSchedulerService(repository.NewScheduleRepo(db), taskRepo, workerSvc).checkActiveTasks(ctx)
	select {
	case submitted := <-workerSvc.Submitted():
		t.Fatalf("stale recovery submitted original prompt behind active follow-up: %#v", submitted)
	case <-time.After(100 * time.Millisecond):
	}
	stored, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.StatusQueued {
		t.Fatalf("active follow-up ownership must preserve queued task status, got %s", stored.Status)
	}
}

func TestSchedulerService_CheckActiveTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create an active+pending task
	task := &models.Task{
		ProjectID: "default",
		Title:     "Active Pending",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task)

	// Create a backlog+pending task (should not be submitted)
	taskRepo.Create(ctx, &models.Task{
		ProjectID: "default",
		Title:     "Backlog Pending",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "test",
	})

	svc.checkActiveTasks(ctx)

	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.Title != "Active Pending" {
			t.Errorf("expected Active Pending, got %q", submitted.Title)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected active pending task to be submitted")
	}

	// Verify backlog task was NOT submitted
	select {
	case submitted := <-workerSvc.Submitted():
		t.Errorf("backlog task should not be submitted, got %q", submitted.Title)
	case <-time.After(100 * time.Millisecond):
		// Expected
	}
}

func TestSchedulerService_CheckActiveTasks_NoDuplicateSubmission(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create multiple active+pending tasks
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	task3 := &models.Task{ProjectID: "default", Title: "Task 3", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task1)
	taskRepo.Create(ctx, task2)
	taskRepo.Create(ctx, task3)

	// First call submits all 3
	svc.checkActiveTasks(ctx)

	// Drain the channel
	drained := 0
	for i := 0; i < 3; i++ {
		select {
		case <-workerSvc.Submitted():
			drained++
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("expected 3 tasks on first call, got %d", drained)
		}
	}

	// Second call should NOT re-submit (dedup prevents it, tasks still in pending map)
	// Simulate that the worker hasn't processed them yet by not clearing pending
	svc.checkActiveTasks(ctx)

	select {
	case got := <-workerSvc.Submitted():
		t.Errorf("expected no duplicates on second call, got task %q", got.Title)
	case <-time.After(100 * time.Millisecond):
		// Expected - dedup prevents re-submission
	}
}

func TestSchedulerService_CheckDueTasks_RepeatingSchedule(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	task := &models.Task{
		ProjectID: "default",
		Title:     "Daily Task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task)

	now := time.Now().UTC()
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now.Add(-1 * time.Minute),
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}
	scheduleRepo.Create(ctx, sched)

	svc.checkDueTasks(ctx)

	// Drain submission
	select {
	case <-workerSvc.Submitted():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected task to be submitted")
	}

	// Verify next_run was set for the future
	updated, _ := scheduleRepo.GetByID(ctx, sched.ID)
	if updated.NextRun == nil {
		t.Fatal("expected NextRun to be set for daily schedule")
	}
	if !updated.NextRun.After(now) {
		t.Error("expected NextRun to be in the future")
	}
}

// TestSchedulerService_DailyScheduleFiresSameDay verifies that a daily schedule
// with next_run set to the current day's run time (even if barely in the past)
// is picked up by the scheduler. This is the core bug fix: previously the handler
// pre-advanced next_run to the NEXT day, so a daily 1:33 AM schedule created at
// 1:34 AM would not run until tomorrow.
func TestSchedulerService_DailyScheduleFiresSameDay(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	task := &models.Task{
		ProjectID: "default",
		Title:     "Daily 1:33 AM",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "test daily schedule",
	}
	taskRepo.Create(ctx, task)

	now := time.Now().UTC()
	// Simulate: next_run is 1 minute in the past (schedule was just missed)
	// With the fix, the handler no longer pre-advances to tomorrow
	pastRunTime := now.Add(-1 * time.Minute)
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          pastRunTime,
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
		NextRun:        &pastRunTime, // next_run = run_at (not pre-advanced)
	}
	// Use Create which sets NextRun = RunAt if nil
	scheduleRepo.Create(ctx, sched)

	// The scheduler should find this schedule as due (next_run <= now)
	svc.checkDueTasks(ctx)

	// Verify task was submitted
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected submitted task ID=%s, got %s", task.ID, submitted.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("daily schedule with next_run 1 minute ago should be submitted")
	}

	// Verify next_run was advanced to tomorrow (not today)
	updated, _ := scheduleRepo.GetByID(ctx, sched.ID)
	if updated.NextRun == nil {
		t.Fatal("expected NextRun to be set after execution")
	}
	if !updated.NextRun.After(now) {
		t.Error("expected NextRun to be in the future after execution")
	}
}

// TestSchedulerService_ScheduledTaskResetsCompletedStatus verifies that the
// scheduler resets a completed/failed task to pending before submitting it.
// This is important for repeating schedules where the task runs multiple times.
func TestSchedulerService_ScheduledTaskResetsCompletedStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create a task that previously completed
	task := &models.Task{
		ProjectID: "default",
		Title:     "Previously Completed",
		Category:  models.CategoryScheduled,
		Status:    models.StatusCompleted,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task)

	now := time.Now().UTC()
	pastTime := now.Add(-30 * time.Second)
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          pastTime,
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}
	scheduleRepo.Create(ctx, sched)

	svc.checkDueTasks(ctx)

	// Should be submitted even though status was "completed"
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected task %s, got %s", task.ID, submitted.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("completed task with due schedule should be reset to pending and submitted")
	}

	// Verify status was reset to pending
	dbTask, _ := taskRepo.GetByID(ctx, task.ID)
	if dbTask.Status != models.StatusPending {
		t.Errorf("expected status=pending, got %s", dbTask.Status)
	}
}

func TestSchedulerService_StartupCatchesMissedSchedules(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	now := time.Now().UTC()

	// Create multiple tasks that should have run while the app was "down"
	task1 := &models.Task{
		ProjectID: "default",
		Title:     "Missed Once",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task1)
	sched1 := &models.Schedule{
		TaskID:         task1.ID,
		RunAt:          now.Add(-2 * time.Hour), // 2 hours ago
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}
	scheduleRepo.Create(ctx, sched1)

	task2 := &models.Task{
		ProjectID: "default",
		Title:     "Missed Daily",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task2)
	sched2 := &models.Schedule{
		TaskID:         task2.ID,
		RunAt:          now.Add(-3 * 24 * time.Hour), // 3 days ago
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}
	scheduleRepo.Create(ctx, sched2)

	// Simulate app startup: checkDueTasks runs immediately
	svc.checkDueTasks(ctx)

	// Both missed tasks should be submitted
	submitted := make(map[string]bool)
	for i := 0; i < 2; i++ {
		select {
		case task := <-workerSvc.Submitted():
			submitted[task.ID] = true
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("expected 2 missed tasks to be submitted, got %d", i)
		}
	}

	if !submitted[task1.ID] {
		t.Error("expected missed one-time task to be submitted on startup")
	}
	if !submitted[task2.ID] {
		t.Error("expected missed daily task to be submitted on startup")
	}

	// Verify one-time schedule has no next run
	updated1, _ := scheduleRepo.GetByID(ctx, sched1.ID)
	if updated1.NextRun != nil {
		t.Error("expected one-time schedule to have nil NextRun after execution")
	}

	// Verify daily schedule has next run in the future
	updated2, _ := scheduleRepo.GetByID(ctx, sched2.ID)
	if updated2.NextRun == nil {
		t.Fatal("expected daily schedule to have NextRun set")
	}
	if !updated2.NextRun.After(now) {
		t.Error("expected daily schedule NextRun to be in the future, not catching up on all missed occurrences")
	}
}

func TestSchedulerService_DragDropReschedule_DoesNotExecuteCompletedTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create a completed one-time scheduled task (simulating a task that was already executed)
	task := &models.Task{
		ProjectID: "default",
		Title:     "Completed Task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusCompleted,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task)

	now := time.Now().UTC()
	// Create a one-time schedule with next_run in the past (simulating drag/drop to past time)
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now.Add(-1 * time.Hour),
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}
	pastTime := now.Add(-30 * time.Minute)
	sched.NextRun = &pastTime
	scheduleRepo.Create(ctx, sched)

	// Run checkDueTasks - this should NOT execute the task
	svc.checkDueTasks(ctx)

	// Verify task was NOT submitted
	select {
	case submitted := <-workerSvc.Submitted():
		t.Fatalf("expected no task submission for completed one-time schedule, but got task ID=%s", submitted.ID)
	case <-time.After(100 * time.Millisecond):
		// Expected - no submission
	}

	// Verify task status is still completed
	updatedTask, _ := taskRepo.GetByID(ctx, task.ID)
	if updatedTask.Status != models.StatusCompleted {
		t.Errorf("expected task status to remain completed, got %s", updatedTask.Status)
	}
}

func TestSchedulerService_RecurringSchedule_ExecutesCompletedTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create a completed daily scheduled task (completed yesterday, should run again today)
	task := &models.Task{
		ProjectID: "default",
		Title:     "Daily Task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusCompleted,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task)

	now := time.Now().UTC()
	// Create a daily schedule with next_run due now
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now.Add(-24 * time.Hour),
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
	}
	scheduleRepo.Create(ctx, sched)

	// Run checkDueTasks - this SHOULD execute the task for recurring schedules
	svc.checkDueTasks(ctx)

	// Verify task WAS submitted
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected submitted task ID=%s, got %s", task.ID, submitted.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected daily recurring task to be submitted even though it was completed")
	}

	// Verify task status was reset to pending
	updatedTask, _ := taskRepo.GetByID(ctx, task.ID)
	if updatedTask.Status != models.StatusPending {
		t.Errorf("expected task status to be reset to pending, got %s", updatedTask.Status)
	}
}

// TestSchedulerService_RecoverStaleQueuedTasks verifies that checkActiveTasks
// recovers tasks stuck in "queued" status for longer than the stale timeout.
// This handles the case where a thread follow-up goroutine crashed without
// cleaning up the task status.
func TestSchedulerService_RecoverStaleQueuedTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create a task with "queued" status in active category
	task := &models.Task{
		ProjectID: "default",
		Title:     "Stale Queued Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending, // Create as pending first (CHECK constraint)
		Prompt:    "test prompt",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Set status to queued
	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusQueued); err != nil {
		t.Fatalf("UpdateStatus to queued: %v", err)
	}

	// Manually set updated_at to the past to simulate a stale task
	_, err := db.ExecContext(ctx,
		`UPDATE tasks SET updated_at = datetime('now', '-15 minutes') WHERE id = ?`, task.ID)
	if err != nil {
		t.Fatalf("Set stale updated_at: %v", err)
	}

	// Run checkActiveTasks — should recover the stale queued task
	svc.checkActiveTasks(ctx)

	// Verify task was reset to pending
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updatedTask.Status != models.StatusPending {
		t.Errorf("expected stale queued task to be reset to pending, got %s", updatedTask.Status)
	}

	// Verify it was submitted to the worker
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected submitted task ID=%s, got %s", task.ID, submitted.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected stale queued task to be submitted")
	}
}

func TestSchedulerService_RestartRecoveryPromotesQueuedFollowupBeforeOriginalPrompt(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	taskRepo := repository.NewTaskRepo(db, nil)
	inputRepo := repository.NewThreadInputRepo(db)
	workerSvc := newTestWorkerService(t)
	svc := NewSchedulerService(repository.NewScheduleRepo(db), taskRepo, workerSvc)

	task := &models.Task{ProjectID: "default", Title: "restart admission", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "stored original prompt"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: "durable follow-up"}
	if err := inputRepo.CreateQueued(ctx, queued); err != nil {
		t.Fatal(err)
	}
	if reset, err := taskRepo.ResetOrphanedRunning(ctx); err != nil || reset != 1 {
		t.Fatalf("restart reset: count=%d err=%v", reset, err)
	}
	resetTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil || resetTask.Status != models.StatusFailed || resetTask.Category != models.CategoryBacklog {
		t.Fatalf("durable follow-up must retain admission across restart: task=%#v err=%v", resetTask, err)
	}

	// Exercise the former startup ordering as an adversarial interleaving. The
	// database guards must remain sufficient even though production now offers
	// queued-input recovery before starting scheduler scans.
	svc.checkActiveTasks(ctx)
	select {
	case submitted := <-workerSvc.Submitted():
		t.Fatalf("scheduler submitted original prompt ahead of durable follow-up: %#v", submitted)
	case <-time.After(100 * time.Millisecond):
	}

	agent, err := repository.NewLLMConfigRepo(db).GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("load default model: %#v err=%v", agent, err)
	}
	promoted := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: queued.Content, IsFollowup: true}
	if err := inputRepo.ClaimQueuedForTaskExecution(ctx, queued.ID, promoted); err != nil {
		t.Fatalf("promote durable follow-up after restart: %v", err)
	}
	if promoted.PromptSent != "durable follow-up" {
		t.Fatalf("promoted wrong prompt: %q", promoted.PromptSent)
	}
}

// TestSchedulerService_DoesNotRecoverRecentQueuedTasks verifies that
// checkActiveTasks does NOT reset tasks that recently entered "queued" status.
// These tasks are actively being handled by thread follow-up goroutines.
func TestSchedulerService_DoesNotRecoverRecentQueuedTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	// Create a task with "queued" status — recently updated (not stale)
	task := &models.Task{
		ProjectID: "default",
		Title:     "Recent Queued Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Set status to queued (updated_at is set to now automatically)
	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusQueued); err != nil {
		t.Fatalf("UpdateStatus to queued: %v", err)
	}

	// Run checkActiveTasks — should NOT recover the recent queued task
	svc.checkActiveTasks(ctx)

	// Verify task is still queued (not reset to pending)
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updatedTask.Status != models.StatusQueued {
		t.Errorf("expected recent queued task to remain queued, got %s", updatedTask.Status)
	}

	// Verify nothing was submitted to the worker (only pending tasks are submitted)
	select {
	case <-workerSvc.Submitted():
		t.Fatal("expected no tasks to be submitted for recent queued task")
	case <-time.After(100 * time.Millisecond):
		// Good — no submission
	}
}

func TestSchedulerService_WorktreeCleanupIntegration(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	// Create worktree service
	worktreeSvc := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	// Create scheduler service and wire worktree service
	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	svc.SetWorktreeService(worktreeSvc)

	// Set cleanup policy
	if err := settingsRepo.Set(ctx, "worktree_cleanup", "after_merge"); err != nil {
		t.Fatal(err)
	}

	// Test that checkWorktreeCleanup can be called without error
	// (actual cleanup functionality is tested in worktree_service_test.go)
	svc.checkWorktreeCleanup(ctx)

	// Verify lastCleanupAt was set
	if svc.lastCleanupAt.IsZero() {
		t.Error("expected lastCleanupAt to be set after cleanup check")
	}

	// Test with nil worktree service (should not panic)
	svc2 := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	svc2.checkWorktreeCleanup(ctx) // Should return immediately

	if !svc2.lastCleanupAt.IsZero() {
		t.Error("expected lastCleanupAt to remain zero with nil worktree service")
	}
}

func TestSchedulerService_CheckDueTasks_SkipsDisabledSchedule(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	task := &models.Task{
		ProjectID: "default",
		Title:     "Disabled Scheduled Task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task)

	now := time.Now().UTC()
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now.Add(-1 * time.Minute),
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}
	scheduleRepo.Create(ctx, sched)
	// Disable the schedule
	scheduleRepo.ToggleEnabled(ctx, sched.ID, false)

	svc.checkDueTasks(ctx)

	select {
	case submitted := <-workerSvc.Submitted():
		t.Errorf("expected no task submission for disabled schedule, got task %s", submitted.ID)
	case <-time.After(50 * time.Millisecond):
		// expected: no submission
	}
}

func TestSchedulerService_CheckDueTasks_ReenabledScheduleRuns(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	ctx := context.Background()

	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	task := &models.Task{
		ProjectID: "default",
		Title:     "Re-enabled Scheduled Task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	taskRepo.Create(ctx, task)

	now := time.Now().UTC()
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now.Add(-1 * time.Minute),
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        false,
	}
	scheduleRepo.Create(ctx, sched)

	// Confirm disabled schedule is not submitted
	svc.checkDueTasks(ctx)
	select {
	case submitted := <-workerSvc.Submitted():
		t.Errorf("expected no submission while disabled, got %s", submitted.ID)
	case <-time.After(50 * time.Millisecond):
		// correct
	}

	// Re-enable
	scheduleRepo.ToggleEnabled(ctx, sched.ID, true)

	// Now it should run
	svc.checkDueTasks(ctx)
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != task.ID {
			t.Errorf("expected task %s, got %s", task.ID, submitted.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected re-enabled task to be submitted")
	}
}

func TestSchedulerService_CheckActiveTasksStartsSwarmPlanner(t *testing.T) {
	db := testutil.NewTestDB(t)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := newTestWorkerService(t)
	taskSvc := NewTaskService(taskRepo, nil, workerSvc)
	swarmSvc := NewSwarmService(taskSvc, taskRepo, nil, workerSvc)
	taskSvc.SetSwarmService(swarmSvc)
	svc := NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	svc.SetSwarmPlannerStarter(swarmSvc)
	ctx := context.Background()

	startImmediately := false
	parent, err := swarmSvc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Recovered active swarm", Prompt: "plan after restart", Category: models.CategoryBacklog, MaxWorkers: 2, ReviewerEnabled: true, MergerEnabled: true, StartImmediately: &startImmediately})
	if err != nil {
		t.Fatalf("CreateSwarmTask: %v", err)
	}
	if err := taskRepo.UpdateCategory(ctx, parent.ID, models.CategoryActive); err != nil {
		t.Fatalf("UpdateCategory: %v", err)
	}
	if err := taskRepo.UpdateStatus(ctx, parent.ID, models.StatusPending); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	svc.checkActiveTasks(ctx)

	planner, err := taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil {
		t.Fatalf("FindSwarmChildByRole: %v", err)
	}
	if planner == nil {
		t.Fatal("expected planner to be created")
	}
	if planner.Category != models.CategoryActive {
		t.Fatalf("expected planner category active, got %s", planner.Category)
	}
	if planner.Status != models.StatusPending {
		t.Fatalf("expected planner status pending, got %s", planner.Status)
	}

	storedParent, err := taskRepo.GetByID(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if storedParent == nil {
		t.Fatal("expected parent to exist")
	}
	if storedParent.Status != models.StatusBlocked {
		t.Fatalf("expected parent status blocked, got %s", storedParent.Status)
	}

	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != planner.ID {
			t.Fatalf("expected submitted planner %s, got %s", planner.ID, submitted.ID)
		}
		if submitted.ID == parent.ID {
			t.Fatal("swarm parent must not be submitted as a normal task")
		}
	case <-time.After(time.Second):
		t.Fatal("expected planner to be submitted")
	}

	select {
	case submitted := <-workerSvc.Submitted():
		t.Fatalf("unexpected extra submitted task %s", submitted.ID)
	case <-time.After(100 * time.Millisecond):
	}
}

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/automationobs"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

type automationRuntimeFixture struct {
	project    models.Project
	task       models.Task
	schedule   models.Schedule
	definition *models.AutomationDefinition
	repo       *repository.AutomationRepo
	taskRepo   *repository.TaskRepo
	schedRepo  *repository.ScheduleRepo
}

func newAutomationRuntimeFixture(t *testing.T, adapterKey string) automationRuntimeFixture {
	t.Helper()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	automationRepo := repository.NewAutomationRepo(db)
	project := automationTestProject(t, projectRepo, "Runtime "+adapterKey)
	task, schedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Runtime task")
	due := time.Now().UTC().Add(-time.Minute)
	schedule.RunAt = due.Add(-time.Hour)
	schedule.NextRun = &due
	schedule.RepeatType = models.RepeatHours
	schedule.RepeatInterval = 1
	require.NoError(t, scheduleRepo.Update(context.Background(), &schedule))
	triggerKey, taskKey, stableKey := "vision_suggestions", "vision_suggestions", "native-sdlc/runtime"
	if adapterKey == AutomationAdapterGitHubSDLC {
		triggerKey, taskKey, stableKey = "dev_inbox", "dev_inbox", "github-sdlc/runtime"
	}
	definition, _, err := NewAutomationRegistrationService(automationRepo, NewAutomationAdapterRegistry()).Register(context.Background(), AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: adapterKey, StableKey: stableKey,
		Resources: []models.AutomationResourceBinding{{NodeKey: triggerKey, ResourceType: "schedule", ResourceID: schedule.ID}, {NodeKey: taskKey, ResourceType: "task", ResourceID: task.ID}},
	})
	require.NoError(t, err)
	return automationRuntimeFixture{project: project, task: task, schedule: schedule, definition: definition, repo: automationRepo, taskRepo: taskRepo, schedRepo: scheduleRepo}
}

func automationNodeByKey(t *testing.T, definition *models.AutomationDefinition, key string) models.AutomationNode {
	t.Helper()
	for _, node := range definition.Nodes {
		if node.NodeKey == key {
			return node
		}
	}
	t.Fatalf("missing automation node %s", key)
	return models.AutomationNode{}
}

func TestAutomationRuntimeAtomicOccurrenceDispatchAndRestartRecovery(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	now := time.Now().UTC()
	next := fixture.schedule.ComputeNextRun(now)
	invocation, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, now, next)
	require.NoError(t, err)
	require.NotNil(t, dispatch)
	require.Equal(t, models.AutomationInvocationClaimed, invocation.Status)

	againInvocation, againDispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, now, next)
	require.NoError(t, err)
	require.Equal(t, invocation.ID, againInvocation.ID)
	require.Equal(t, dispatch.ID, againDispatch.ID)
	storedSchedule, err := fixture.schedRepo.GetByID(ctx, fixture.schedule.ID)
	require.NoError(t, err)
	require.True(t, storedSchedule.NextRun.Equal(*next))

	const pollers = 8
	var winners atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < pollers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			leased, leaseErr := fixture.repo.LeaseNextDispatch(ctx, fmt.Sprintf("poller-%d", i), now, time.Minute)
			require.NoError(t, leaseErr)
			if leased != nil {
				winners.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	require.Equal(t, int32(1), winners.Load())

	leased, err := fixture.repo.LeaseNextDispatch(ctx, "owner", now.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, leased, "expired processing lease must recover")
	execution, err := fixture.taskRepo.ClaimAutomationDispatch(ctx, dispatch.ID, "owner")
	require.NoError(t, err)
	require.Equal(t, dispatch.ID, execution.DispatchID)
	sameExecution, err := fixture.taskRepo.ClaimAutomationDispatch(ctx, dispatch.ID, "owner")
	require.NoError(t, err)
	require.Equal(t, execution.ID, sameExecution.ID, "dispatch retry must resolve the prepared execution")
	require.ErrorIs(t, fixture.repo.RenewDispatchLease(ctx, dispatch.ID, "not-owner", now.Add(4*time.Minute)), repository.ErrAutomationDispatchLease)
	require.NoError(t, fixture.repo.RenewDispatchLease(ctx, dispatch.ID, "owner", now.Add(4*time.Minute)))
	producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	pendingBinding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID,
		InvocationID: invocation.ID, NodeID: producer.ID}
	_, pendingActivity, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{pendingBinding}}, Binding: pendingBinding,
		ActivityKey: "invocation:pending-external", ActivityType: "external_reconciliation", ActivityStatus: models.AutomationActivityPending,
	})
	require.NoError(t, err)

	reset, err := fixture.taskRepo.ResetOrphanedRunning(ctx)
	require.NoError(t, err)
	require.Zero(t, reset, "generic startup recovery must preserve prepared automation tasks")
	recovered, err := repository.NewExecutionRepo(fixture.repo.DB()).RecoverStaleRunningTaskExecutions(ctx)
	require.NoError(t, err)
	require.Zero(t, recovered, "generic execution recovery must preserve dispatch executions")

	require.NoError(t, fixture.repo.MarkDispatchSubmitted(ctx, dispatch.ID, "owner", execution.ID))
	require.NoError(t, repository.NewExecutionRepo(fixture.repo.DB()).Complete(ctx, execution.ID, models.ExecCompleted, "ok", "", 1, 1))
	require.NoError(t, fixture.taskRepo.UpdateStatus(ctx, fixture.task.ID, models.StatusCompleted))
	reconciler := NewAutomationReconciler(fixture.repo, repository.NewExecutionRepo(fixture.repo.DB()), NewWorkerService(nil, 1, nil))
	require.NoError(t, reconciler.ReconcileOnce(ctx))
	var outboxStatus string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status FROM automation_dispatch_outbox WHERE id = ?`, dispatch.ID).Scan(&outboxStatus))
	require.Equal(t, "completed", outboxStatus)
	var invocationStatus string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status FROM automation_invocations WHERE id = ?`, invocation.ID).Scan(&invocationStatus))
	require.Equal(t, "running", invocationStatus, "terminal execution must not close an invocation with pending owned activity")
	_, err = fixture.repo.DB().Exec(`UPDATE automation_activities SET status = 'completed', completed_at = CURRENT_TIMESTAMP WHERE id = ?`, pendingActivity.ID)
	require.NoError(t, err)
	require.NoError(t, reconciler.ReconcileOnce(ctx))
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status FROM automation_invocations WHERE id = ?`, invocation.ID).Scan(&invocationStatus))
	require.Equal(t, "completed", invocationStatus)
	var reservations int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_task_run_reservations WHERE dispatch_id = ?`, dispatch.ID).Scan(&reservations))
	require.Zero(t, reservations)
}

func TestAutomationDeleteRejectsInFlightDispatchAndPreservesRestartRecovery(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	now := time.Now().UTC()
	_, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, now, fixture.schedule.ComputeNextRun(now))
	require.NoError(t, err)
	require.NotNil(t, dispatch)
	leased, err := fixture.repo.LeaseNextDispatch(ctx, "deleting-process", now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, dispatch.ID, leased.ID)
	execution, err := fixture.taskRepo.ClaimAutomationDispatch(ctx, dispatch.ID, "deleting-process")
	require.NoError(t, err)
	require.NoError(t, fixture.repo.MarkDispatchSubmitted(ctx, dispatch.ID, "deleting-process", execution.ID))

	lifecycle := NewAutomationLifecycleService(fixture.repo, fixture.schedRepo)
	err = lifecycle.Delete(ctx, fixture.project.ID, fixture.definition.Automation.ID)
	require.ErrorContains(t, err, "in-flight")
	definition, err := fixture.repo.GetDefinition(ctx, fixture.project.ID, fixture.definition.Automation.ID)
	require.NoError(t, err)
	require.NotNil(t, definition, "rejected deletion must preserve Automation recovery ownership")
	var outboxRows, reservationRows, executionRows int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_dispatch_outbox WHERE id = ?`, dispatch.ID).Scan(&outboxRows))
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_task_run_reservations WHERE dispatch_id = ?`, dispatch.ID).Scan(&reservationRows))
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM executions WHERE id = ? AND dispatch_id = ?`, execution.ID, dispatch.ID).Scan(&executionRows))
	require.Equal(t, 1, outboxRows)
	require.Equal(t, 1, reservationRows)
	require.Equal(t, 1, executionRows)
	recovered, err := repository.NewExecutionRepo(fixture.repo.DB()).RecoverStaleRunningTaskExecutions(ctx)
	require.NoError(t, err)
	require.Zero(t, recovered, "generic recovery must leave the preserved dispatch to Automation reconciliation")

	require.NoError(t, repository.NewExecutionRepo(fixture.repo.DB()).Complete(ctx, execution.ID, models.ExecCompleted, "ok", "", 1, 1))
	require.NoError(t, fixture.taskRepo.UpdateStatus(ctx, fixture.task.ID, models.StatusCompleted))
	reconciler := NewAutomationReconciler(fixture.repo, repository.NewExecutionRepo(fixture.repo.DB()), NewWorkerService(nil, 1, nil))
	require.NoError(t, reconciler.ReconcileOnce(ctx))
	var outboxStatus string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status FROM automation_dispatch_outbox WHERE id = ?`, dispatch.ID).Scan(&outboxStatus))
	require.Equal(t, "completed", outboxStatus)
	require.NoError(t, lifecycle.Delete(ctx, fixture.project.ID, fixture.definition.Automation.ID), "terminal reconciliation must make deletion safe")
}

func TestAutomationRuntimeSchedulerRoutesOwnedTriggerAtomically(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	require.NoError(t, fixture.taskRepo.UpdateCategory(context.Background(), fixture.task.ID, models.CategoryActive))
	scheduler := NewSchedulerService(fixture.schedRepo, fixture.taskRepo, NewWorkerService(nil, 1, nil))
	scheduler.SetAutomationRepo(fixture.repo)
	scheduler.checkDueTasks(context.Background())
	var invocations, dispatches int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_invocations WHERE trigger_resource_id = ?`, fixture.schedule.ID).Scan(&invocations))
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_dispatch_outbox`).Scan(&dispatches))
	require.Equal(t, 1, invocations)
	require.Equal(t, 1, dispatches)
	stored, err := fixture.schedRepo.GetByID(context.Background(), fixture.schedule.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.NextRun)
	require.True(t, stored.NextRun.After(time.Now().UTC()))
	storedTask, err := fixture.taskRepo.GetByID(context.Background(), fixture.task.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryActive, storedTask.Category, "automation scheduling must preserve an existing runnable category")
	claimedOrdinarily, err := fixture.taskRepo.ClaimTask(context.Background(), fixture.task.ID)
	require.NoError(t, err)
	require.False(t, claimedOrdinarily, "ordinary task claiming must not consume an Automation reservation")

	scheduler.checkActiveTasks(context.Background())
	select {
	case submitted := <-scheduler.workerSvc.Submitted():
		t.Fatalf("reserved automation task %s was submitted through the ordinary worker path", submitted.ID)
	default:
	}
}

func TestAutomationRuntimeConcurrentSchedulePollersShareOneOccurrence(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	now := time.Now().UTC()
	next := fixture.schedule.ComputeNextRun(now)
	const pollers = 8
	type result struct {
		invocationID string
		dispatchID   string
		err          error
	}
	results := make(chan result, pollers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < pollers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			invocation, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, now, next)
			value := result{err: err}
			if invocation != nil {
				value.invocationID = invocation.ID
			}
			if dispatch != nil {
				value.dispatchID = dispatch.ID
			}
			results <- value
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	invocations := map[string]bool{}
	dispatches := map[string]bool{}
	for value := range results {
		require.NoError(t, value.err)
		require.NotEmpty(t, value.invocationID)
		require.NotEmpty(t, value.dispatchID)
		invocations[value.invocationID] = true
		dispatches[value.dispatchID] = true
	}
	require.Len(t, invocations, 1)
	require.Len(t, dispatches, 1)
	var invocationRows, dispatchRows int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_invocations WHERE trigger_resource_id = ?`, fixture.schedule.ID).Scan(&invocationRows))
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_dispatch_outbox`).Scan(&dispatchRows))
	require.Equal(t, 1, invocationRows)
	require.Equal(t, 1, dispatchRows)
}

func TestAutomationRuntimeExpectedDueCASLeavesNoOrphanInvocation(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	stale := fixture.schedule
	future := time.Now().UTC().Add(time.Hour)
	fixture.schedule.NextRun = &future
	require.NoError(t, fixture.schedRepo.Update(ctx, &fixture.schedule))
	_, _, err := fixture.repo.ClaimScheduledOccurrence(ctx, stale, time.Now().UTC(), stale.ComputeNextRun(time.Now().UTC()))
	require.ErrorIs(t, err, repository.ErrAutomationScheduleChanged)
	var invocations int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_invocations WHERE trigger_resource_id = ?`, stale.ID).Scan(&invocations))
	require.Zero(t, invocations)
}

func TestAutomationRuntimeOverlappingInvocationsProjectConcurrently(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	firstTask, firstSchedule := automationTestTaskAndSchedule(t, fixture.taskRepo, fixture.schedRepo, fixture.project.ID, "First runtime loop")
	inboxTask, inboxSchedule := automationTestTaskAndSchedule(t, fixture.taskRepo, fixture.schedRepo, fixture.project.ID, "Runtime inbox")
	due := time.Now().UTC().Add(-time.Minute)
	for _, schedule := range []*models.Schedule{&firstSchedule, &inboxSchedule} {
		schedule.NextRun = &due
		schedule.RunAt = due.Add(-time.Hour)
		schedule.RepeatType = models.RepeatHours
		schedule.RepeatInterval = 1
		require.NoError(t, fixture.schedRepo.Update(ctx, schedule))
	}
	definition, _, err := NewAutomationRegistrationService(fixture.repo, NewAutomationAdapterRegistry()).Register(ctx, AutomationRegistrationRequest{
		ProjectID: fixture.project.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/runtime-overlap",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: firstSchedule.ID},
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: firstTask.ID},
			{NodeKey: "inbox", ResourceType: "schedule", ResourceID: inboxSchedule.ID},
			{NodeKey: "inbox", ResourceType: "task", ResourceID: inboxTask.ID},
		},
	})
	require.NoError(t, err)
	first, firstDispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, firstSchedule, time.Now().UTC(), firstSchedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	second, secondDispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, inboxSchedule, time.Now().UTC(), inboxSchedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	require.NotNil(t, firstDispatch)
	require.NotNil(t, secondDispatch)
	require.NotEqual(t, first.ID, second.ID)
	graph, err := NewAutomationGraphService(fixture.repo).GetLive(ctx, fixture.project.ID, definition.Automation.ID, time.Now())
	require.NoError(t, err)
	require.Equal(t, 2, graph.ActiveInvocations)
}

func TestAutomationRuntimePreparedDispatchUsesExistingWorkerPipeline(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	llmConfigRepo := repository.NewLLMConfigRepo(fixture.repo.DB())
	agent := models.LLMConfig{Name: "Automation worker", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, &agent))
	require.NoError(t, fixture.repo.DB().QueryRow(`UPDATE tasks SET agent_id = ? WHERE id = ? RETURNING id`, agent.ID, fixture.task.ID).Scan(new(string)))
	projectRepo := repository.NewProjectRepo(fixture.repo.DB())
	execRepo := repository.NewExecutionRepo(fixture.repo.DB())
	execRepo.SetAutomationRepo(fixture.repo)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, fixture.taskRepo, projectRepo, fixture.schedRepo, repository.NewAttachmentRepo(fixture.repo.DB()))
	mockLLM := testutil.NewMockLLMCaller()
	mockLLM.Response = "automation run completed"
	mockLLM.TextOnly = mockLLM.Response
	llmSvc.SetLLMCaller(mockLLM)
	llmSvc.SetAutomationRepo(fixture.repo)
	worker := NewWorkerService(llmSvc, 1, projectRepo)
	worker.SetTaskRepo(fixture.taskRepo)
	worker.SetLLMConfigRepo(llmConfigRepo)
	worker.SetExecutionRepo(execRepo)
	worker.SetAutomationRepo(fixture.repo)
	worker.Start(ctx)
	defer worker.Stop()

	_, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Now().UTC(), fixture.schedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	require.NotNil(t, dispatch)
	crashedLease, err := fixture.repo.LeaseNextDispatch(ctx, "crashed-process", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.Equal(t, dispatch.ID, crashedLease.ID)
	crashExecution, err := fixture.taskRepo.ClaimAutomationDispatch(ctx, dispatch.ID, "crashed-process")
	require.NoError(t, err)
	past := time.Now().UTC().Add(-time.Minute)
	_, err = fixture.repo.DB().Exec(`UPDATE automation_dispatch_outbox SET claim_expires_at = ? WHERE id = ?`, past, dispatch.ID)
	require.NoError(t, err)
	_, err = fixture.repo.DB().Exec(`UPDATE automation_task_run_reservations SET lease_expires_at = ? WHERE dispatch_id = ?`, past, dispatch.ID)
	require.NoError(t, err)
	dispatcher := NewAutomationDispatcher(fixture.repo, fixture.taskRepo, worker)
	dispatched, err := dispatcher.DispatchOne(ctx)
	require.NoError(t, err)
	require.True(t, dispatched)
	require.Eventually(t, func() bool {
		var status string
		return fixture.repo.DB().QueryRow(`SELECT status FROM automation_dispatch_outbox WHERE id = ?`, dispatch.ID).Scan(&status) == nil && status == "completed"
	}, 5*time.Second, 20*time.Millisecond)
	var executionCount int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM executions WHERE dispatch_id = ?`, dispatch.ID).Scan(&executionCount))
	require.Equal(t, 1, executionCount, "prepared dispatch must reuse one execution through the normal worker pipeline")
	var activityCount int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activities WHERE activity_key = ?`, "dispatch:"+dispatch.ID+":execute").Scan(&activityCount))
	require.Equal(t, 1, activityCount)
	var executionActivityCount int
	var preparedExecutionID string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT id FROM executions WHERE dispatch_id = ?`, dispatch.ID).Scan(&preparedExecutionID))
	require.Equal(t, crashExecution.ID, preparedExecutionID, "restart recovery must submit the precreated execution")
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(DISTINCT a.id) FROM automation_activities a
		JOIN automation_activity_resources ar ON ar.activity_id = a.id
		WHERE ar.resource_type = 'execution' AND ar.resource_id = ?`, preparedExecutionID).Scan(&executionActivityCount))
	require.Equal(t, 1, executionActivityCount, "prepared execution must not create a second generic runtime activity")
}

func TestAutomationRuntimeReclaimedDispatchAlreadyQueuedIsAcknowledged(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	_, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Now().UTC(), fixture.schedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	leased, err := fixture.repo.LeaseNextDispatch(ctx, "crashed-owner", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.Equal(t, dispatch.ID, leased.ID)
	execution, err := fixture.taskRepo.ClaimAutomationDispatch(ctx, dispatch.ID, "crashed-owner")
	require.NoError(t, err)
	worker := NewWorkerService(nil, 1, nil)
	worker.Submit(fixture.task)
	past := time.Now().UTC().Add(-time.Minute)
	_, err = fixture.repo.DB().Exec(`UPDATE automation_dispatch_outbox SET claim_expires_at = ? WHERE id = ?`, past, dispatch.ID)
	require.NoError(t, err)
	_, err = fixture.repo.DB().Exec(`UPDATE automation_task_run_reservations SET lease_expires_at = ? WHERE dispatch_id = ?`, past, dispatch.ID)
	require.NoError(t, err)
	dispatcher := NewAutomationDispatcher(fixture.repo, fixture.taskRepo, worker)
	dispatched, err := dispatcher.DispatchOne(ctx)
	require.NoError(t, err)
	require.True(t, dispatched)
	var status, storedExecutionID string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status, execution_id FROM automation_dispatch_outbox WHERE id = ?`, dispatch.ID).Scan(&status, &storedExecutionID))
	require.Equal(t, "submitted", status)
	require.Equal(t, execution.ID, storedExecutionID)
}

func TestAutomationRuntimeReconcilerResubmitsAcknowledgedPreparedExecution(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	_, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Now().UTC(), fixture.schedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	leased, err := fixture.repo.LeaseNextDispatch(ctx, "owner", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.Equal(t, dispatch.ID, leased.ID)
	execution, err := fixture.taskRepo.ClaimAutomationDispatch(ctx, dispatch.ID, "owner")
	require.NoError(t, err)
	require.NoError(t, fixture.repo.MarkDispatchSubmitted(ctx, dispatch.ID, "owner", execution.ID))
	worker := NewWorkerService(nil, 1, nil)
	reconciler := NewAutomationReconciler(fixture.repo, repository.NewExecutionRepo(fixture.repo.DB()), worker)
	require.NoError(t, reconciler.ReconcileOnce(ctx))
	select {
	case submitted := <-worker.Submitted():
		require.Equal(t, fixture.task.ID, submitted.ID)
	default:
		t.Fatal("expected reconciler to resubmit the durable prepared execution")
	}
}

func TestAutomationRuntimeReconcilesTerminalExecutionProjectionAfterCrash(t *testing.T) {
	automationobs.ResetForTest()
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	inbox := automationNodeByKey(t, fixture.definition, "inbox")
	implementation := automationNodeByKey(t, fixture.definition, "implementation")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, NodeID: implementation.ID}
	item, _, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "crash:item", ActivityKey: "crash:handoff", ActivityType: "handoff", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "crash:implementation", FromNodeID: inbox.ID, ToNodeID: implementation.ID, Transition: models.AutomationTransitionEntered,
	})
	require.NoError(t, err)
	binding.WorkItemID = item.ID
	execution := models.Execution{TaskID: fixture.task.ID, Status: models.ExecRunning, PromptSent: "crash recovery"}
	execRepo := repository.NewExecutionRepo(fixture.repo.DB())
	require.NoError(t, execRepo.Create(ctx, &execution))
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		ActivityKey: "execution:" + execution.ID + ":run", ActivityType: "task_execution", ActivityStatus: models.AutomationActivityRunning,
		Resources: []models.AutomationActivityResource{{ResourceType: "execution", ResourceID: execution.ID}, {ResourceType: "task", ResourceID: fixture.task.ID}},
	})
	require.NoError(t, err)
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		ActivityKey: "execution:" + execution.ID + ":external-action", ActivityType: "create_notification", ActivityStatus: models.AutomationActivityCompleted,
		Resources: []models.AutomationActivityResource{{ResourceType: "execution", ResourceID: execution.ID}},
	})
	require.NoError(t, err)
	_, err = fixture.repo.DB().Exec(`UPDATE executions SET status = 'completed', completed_at = CURRENT_TIMESTAMP WHERE id = ?`, execution.ID)
	require.NoError(t, err, "simulate process loss after authoritative execution write and before projection update")
	reconciler := NewAutomationReconciler(fixture.repo, execRepo, NewWorkerService(nil, 1, nil))
	require.NoError(t, reconciler.ReconcileOnce(ctx))
	var status string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status FROM automation_work_items WHERE id = ?`, item.ID).Scan(&status))
	require.Equal(t, "completed", status)
	var activityStatus string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status FROM automation_activities WHERE activity_key = ?`, "execution:"+execution.ID+":run").Scan(&activityStatus))
	require.Equal(t, "completed", activityStatus)
	var externalStatus string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status FROM automation_activities WHERE activity_key = ?`, "execution:"+execution.ID+":external-action").Scan(&externalStatus))
	require.Equal(t, "completed", externalStatus, "execution reconciliation must not rewrite a successful domain action")
	require.Greater(t, automationobs.Snapshot()["automation.reconciliation.projection_repaired"].Count, uint64(0))
}

func TestAutomationRuntimeSkippedOccurrenceAndProjectionIdempotency(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	require.NoError(t, fixture.taskRepo.UpdateStatus(ctx, fixture.task.ID, models.StatusRunning))
	now := time.Now().UTC()
	invocation, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, now, fixture.schedule.ComputeNextRun(now))
	require.NoError(t, err)
	require.Nil(t, dispatch)
	require.Equal(t, models.AutomationInvocationSkipped, invocation.Status)
	require.Equal(t, "task_running", invocation.SkippedReason)

	approval := automationNodeByKey(t, fixture.definition, "approval")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: invocation.ID, NodeID: approval.ID}
	projection := repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "alert:stable", WorkItemKind: "suggestion", WorkItemTitle: "Stable", WorkItemStatus: models.AutomationWorkItemWaiting,
		ActivityKey: "alert:stable:create", ActivityType: "create_notification", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "alert:stable:waiting", ToNodeID: approval.ID, Transition: models.AutomationTransitionWaiting,
	}
	item, _, err := fixture.repo.RecordProjectionEvent(ctx, projection)
	require.NoError(t, err)
	again, _, err := fixture.repo.RecordProjectionEvent(ctx, projection)
	require.NoError(t, err)
	require.Equal(t, item.ID, again.ID)
	var transitions int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_transitions WHERE work_item_id = ?`, item.ID).Scan(&transitions))
	require.Equal(t, 1, transitions)

	newTask, newSchedule := automationTestTaskAndSchedule(t, fixture.taskRepo, fixture.schedRepo, fixture.project.ID, "New topology worker")
	newDefinition, _, err := NewAutomationRegistrationService(fixture.repo, NewAutomationAdapterRegistry()).Register(ctx, AutomationRegistrationRequest{
		ProjectID: fixture.project.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/runtime",
		Resources: []models.AutomationResourceBinding{{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: newSchedule.ID}, {NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: newTask.ID}},
	})
	require.NoError(t, err)
	require.Equal(t, fixture.definition.Version.ID, newDefinition.Version.ID, "setup reruns must preserve the point-in-time graph")
	newApproval := automationNodeByKey(t, newDefinition, "approval")
	newCompleted := automationNodeByKey(t, newDefinition, "completed")
	newBinding := models.AutomationBinding{AutomationID: newDefinition.Automation.ID, VersionID: newDefinition.Version.ID, NodeID: newApproval.ID}
	mappedItem, mappedActivity, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{newBinding}}, Binding: newBinding,
		WorkItemKey: "alert:stable", WorkItemStatus: models.AutomationWorkItemWaiting,
		ActivityKey: "alert:stable:current-graph-process", ActivityType: "process_existing", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "alert:stable:current-graph-waiting", ToNodeID: newApproval.ID, Transition: models.AutomationTransitionWaiting,
	})
	require.NoError(t, err)
	require.Equal(t, item.ID, mappedItem.ID, "setup reruns must keep work on the saved graph projection")
	require.Equal(t, newDefinition.Version.ID, mappedActivity.VersionID)
	var preservedProjectionRows int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_work_items WHERE id = ?`, item.ID).Scan(&preservedProjectionRows))
	require.Equal(t, 1, preservedProjectionRows, "setup reruns must preserve current runtime projection")
	liveCurrentGraph, err := NewAutomationGraphService(fixture.repo).GetLive(ctx, fixture.project.ID, fixture.definition.Automation.ID, time.Now())
	require.NoError(t, err)
	for _, node := range liveCurrentGraph.Nodes {
		if node.NodeKey == "approval" {
			require.Equal(t, 1, node.Counts.Waiting, "Live must count only current-graph positions")
		}
	}

	newBinding.WorkItemID = mappedItem.ID
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{newBinding}}, Binding: newBinding,
		ActivityKey: "alert:stable:current-graph-complete", ActivityType: "outcome", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "alert:stable:current-graph-completed", FromNodeID: newApproval.ID, ToNodeID: newCompleted.ID, Transition: models.AutomationTransitionCompleted,
	})
	require.NoError(t, err)
	var itemStatus string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status FROM automation_work_items WHERE id = ?`, mappedItem.ID).Scan(&itemStatus))
	require.Equal(t, "completed", itemStatus)
	var positions int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_work_item_positions WHERE work_item_id = ?`, mappedItem.ID).Scan(&positions))
	require.Zero(t, positions)

	currentContext := models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{newBinding}}
	otherProject := automationTestProject(t, repository.NewProjectRepo(fixture.repo.DB()), "Foreign runtime")
	foreignTask := models.Task{ProjectID: otherProject.ID, Title: "Foreign", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, repository.NewTaskRepo(fixture.repo.DB(), nil).Create(ctx, &foreignTask))
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: currentContext, Binding: newBinding, ActivityKey: "foreign-resource", ActivityType: "test", ActivityStatus: models.AutomationActivityCompleted,
		Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: foreignTask.ID}},
	})
	require.ErrorContains(t, err, "does not belong to project")
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: currentContext, Binding: newBinding, ActivityKey: "malformed-external", ActivityType: "test", ActivityStatus: models.AutomationActivityCompleted,
		Resources: []models.AutomationActivityResource{{ResourceType: "github_issue", ResourceID: "github:not-qualified"}},
	})
	require.ErrorContains(t, err, "canonical and repository-qualified")
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: currentContext, Binding: newBinding, ActivityKey: "unsafe-external", ActivityType: "test", ActivityStatus: models.AutomationActivityCompleted,
		Resources: []models.AutomationActivityResource{{ResourceType: "github_issue", ResourceID: "github:owner/<script>:issue:1"}},
	})
	require.ErrorContains(t, err, "valid owner/repository")
}

func TestAutomationRuntimeSkippedOneTimeAndSharedTaskReservation(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	fixture.schedule.RepeatType = models.RepeatOnce
	fixture.schedule.RepeatInterval = 1
	require.NoError(t, fixture.schedRepo.Update(ctx, &fixture.schedule))
	require.NoError(t, fixture.taskRepo.UpdateStatus(ctx, fixture.task.ID, models.StatusRunning))
	invocation, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Now().UTC(), nil)
	require.NoError(t, err)
	require.Nil(t, dispatch)
	require.Equal(t, models.AutomationInvocationSkipped, invocation.Status)
	stored, err := fixture.schedRepo.GetByID(ctx, fixture.schedule.ID)
	require.NoError(t, err)
	require.Nil(t, stored.NextRun, "a skipped one-time occurrence must clear next_run")

	shared := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	secondSchedule := models.Schedule{TaskID: shared.task.ID, RunAt: time.Now().UTC().Add(-2 * time.Minute), RepeatType: models.RepeatHours, RepeatInterval: 1, Enabled: true}
	due := time.Now().UTC().Add(-time.Minute)
	secondSchedule.NextRun = &due
	require.NoError(t, shared.schedRepo.Create(ctx, &secondSchedule))
	_, _, err = NewAutomationRegistrationService(shared.repo, NewAutomationAdapterRegistry()).Register(ctx, AutomationRegistrationRequest{
		ProjectID: shared.project.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/shared-second",
		Resources: []models.AutomationResourceBinding{{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: secondSchedule.ID}, {NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: shared.task.ID}},
	})
	require.NoError(t, err)
	now := time.Now().UTC()
	type claimResult struct {
		invocation *models.AutomationInvocation
		dispatch   *models.AutomationDispatch
		err        error
	}
	claimResults := make(chan claimResult, 2)
	start := make(chan struct{})
	var claimWG sync.WaitGroup
	for _, scheduled := range []models.Schedule{shared.schedule, secondSchedule} {
		scheduled := scheduled
		claimWG.Add(1)
		go func() {
			defer claimWG.Done()
			<-start
			invocation, dispatch, claimErr := shared.repo.ClaimScheduledOccurrence(ctx, scheduled, now, scheduled.ComputeNextRun(now))
			claimResults <- claimResult{invocation: invocation, dispatch: dispatch, err: claimErr}
		}()
	}
	close(start)
	claimWG.Wait()
	close(claimResults)
	var invocationIDs []string
	var dispatchWinners, skippedLosers int
	for result := range claimResults {
		require.NoError(t, result.err)
		require.NotNil(t, result.invocation)
		invocationIDs = append(invocationIDs, result.invocation.ID)
		if result.dispatch != nil {
			dispatchWinners++
		} else {
			skippedLosers++
			require.Equal(t, models.AutomationInvocationSkipped, result.invocation.Status)
			require.Equal(t, "task_reserved", result.invocation.SkippedReason)
		}
	}
	require.Equal(t, 1, dispatchWinners)
	require.Equal(t, 1, skippedLosers)
	require.Len(t, invocationIDs, 2)
	require.NotEqual(t, invocationIDs[0], invocationIDs[1])
}

func TestAutomationRuntimeCompletedBranchDoesNotCloseParallelWork(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	approval := automationNodeByKey(t, fixture.definition, "approval")
	inbox := automationNodeByKey(t, fixture.definition, "inbox")
	completed := automationNodeByKey(t, fixture.definition, "completed")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, NodeID: approval.ID}
	item, _, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "parallel:item", ActivityKey: "parallel:approval", ActivityType: "branch", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "parallel:approval", ToNodeID: approval.ID, Transition: models.AutomationTransitionEntered,
	})
	require.NoError(t, err)
	binding.WorkItemID = item.ID
	binding.NodeID = inbox.ID
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		ActivityKey: "parallel:inbox", ActivityType: "branch", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "parallel:inbox", ToNodeID: inbox.ID, Transition: models.AutomationTransitionEntered,
	})
	require.NoError(t, err)
	binding.NodeID = approval.ID
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		ActivityKey: "parallel:approval:done", ActivityType: "branch", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "parallel:approval:done", FromNodeID: approval.ID, ToNodeID: completed.ID, Transition: models.AutomationTransitionCompleted,
	})
	require.NoError(t, err)
	var status string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status FROM automation_work_items WHERE id = ?`, item.ID).Scan(&status))
	require.Equal(t, "active", status)
	var positions int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_work_item_positions WHERE work_item_id = ?`, item.ID).Scan(&positions))
	require.Equal(t, 1, positions)
	binding.NodeID = inbox.ID
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		ActivityKey: "parallel:inbox:done", ActivityType: "branch", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "parallel:inbox:done", FromNodeID: inbox.ID, ToNodeID: completed.ID, Transition: models.AutomationTransitionCompleted,
	})
	require.NoError(t, err)
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status FROM automation_work_items WHERE id = ?`, item.ID).Scan(&status))
	require.Equal(t, "completed", status)
}

func TestAutomationRuntimeCompositeConstraintsAndProjectCascade(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	otherTask, otherSchedule := automationTestTaskAndSchedule(t, fixture.taskRepo, fixture.schedRepo, fixture.project.ID, "Other topology")
	otherDefinition, _, err := NewAutomationRegistrationService(fixture.repo, NewAutomationAdapterRegistry()).Register(ctx, AutomationRegistrationRequest{
		ProjectID: fixture.project.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/other-topology",
		Resources: []models.AutomationResourceBinding{{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: otherSchedule.ID}, {NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: otherTask.ID}},
	})
	require.NoError(t, err)
	foreignNode := automationNodeByKey(t, otherDefinition, "vision_suggestions")
	_, err = fixture.repo.DB().Exec(`INSERT INTO automation_invocations
		(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key)
		VALUES (?, ?, ?, ?, 'schedule', ?, 'mismatched-parent')`, fixture.project.ID, fixture.definition.Automation.ID,
		fixture.definition.Version.ID, foreignNode.ID, fixture.schedule.ID)
	require.Error(t, err, "invocation must reject a node from another automation/version")

	invocation, _, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Now().UTC(), fixture.schedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	approval := automationNodeByKey(t, fixture.definition, "approval")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: invocation.ID, NodeID: approval.ID}
	item, activity, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "constraint:item", ActivityKey: "constraint:activity", ActivityType: "test", ActivityStatus: models.AutomationActivityRunning,
	})
	require.NoError(t, err)
	_, err = fixture.repo.DB().Exec(`INSERT INTO automation_work_item_positions
		(work_item_id, project_id, automation_id, version_id, node_id, state) VALUES (?, ?, ?, ?, ?, 'active')`,
		item.ID, fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID, foreignNode.ID)
	require.Error(t, err, "position must reject a node from another topology")
	_, err = fixture.repo.DB().Exec(`INSERT INTO automation_transitions
		(project_id, automation_id, version_id, work_item_id, activity_id, to_node_id, event_key, state)
		VALUES (?, ?, ?, ?, ?, ?, 'constraint:mismatch', 'entered')`, fixture.project.ID, fixture.definition.Automation.ID,
		fixture.definition.Version.ID, item.ID, activity.ID, foreignNode.ID)
	require.Error(t, err, "transition must reject a node from another topology")

	llmRepo := repository.NewLLMConfigRepo(fixture.repo.DB())
	agent := models.LLMConfig{Name: "Cascade binding model", Provider: models.ProviderTest, Model: "test"}
	require.NoError(t, llmRepo.Create(ctx, &agent))
	inputRepo := repository.NewThreadInputRepo(fixture.repo.DB())
	input := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: fixture.project.ID, TaskID: fixture.task.ID,
		AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "cascade binding"}
	require.NoError(t, inputRepo.CreateQueued(ctx, &input))
	binding.WorkItemID = item.ID
	require.NoError(t, fixture.repo.BindThreadInput(ctx, input.ID,
		models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, "cascade"))
	var bindingCount int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_thread_input_bindings WHERE thread_input_id = ?`, input.ID).Scan(&bindingCount))
	require.Equal(t, 1, bindingCount)

	require.NoError(t, repository.NewProjectRepo(fixture.repo.DB()).Delete(ctx, fixture.project.ID))
	for _, table := range []string{"automations", "automation_invocations", "automation_dispatch_outbox", "automation_task_run_reservations", "automation_work_items", "automation_work_item_positions", "automation_thread_input_bindings", "automation_activities", "automation_activity_resources", "automation_transitions"} {
		var count int
		require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
		require.Zero(t, count, table+" must cascade on project deletion")
	}
}

func TestAutomationRuntimeChildTaskInheritsPersistedParentContext(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, NodeID: producer.ID}
	_, _, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "parent:causal-work", ActivityKey: "parent:causal-task", ActivityType: "task_execution", ActivityStatus: models.AutomationActivityRunning,
		Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: fixture.task.ID}},
	})
	require.NoError(t, err)
	child := models.Task{ProjectID: fixture.project.ID, Title: "Causal child", Category: models.CategoryBacklog,
		Status: models.StatusPending, Priority: 2, ParentTaskID: &fixture.task.ID, SwarmRole: models.SwarmRoleWorker}
	require.NoError(t, fixture.taskRepo.Create(ctx, &child))
	inherited, err := fixture.repo.ContextForTask(ctx, fixture.project.ID, child.ID)
	require.NoError(t, err)
	require.Len(t, inherited.Bindings, 1)
	require.Equal(t, binding.AutomationID, inherited.Bindings[0].AutomationID)
	require.Equal(t, binding.NodeID, inherited.Bindings[0].NodeID)
}

func TestAutomationRuntimeDispatchFailureBackoffIsOwnerOnlyAndTerminal(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	now := time.Now().UTC()
	_, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, now, fixture.schedule.ComputeNextRun(now))
	require.NoError(t, err)
	leased, err := fixture.repo.LeaseNextDispatch(ctx, "owner", now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, dispatch.ID, leased.ID)
	require.ErrorIs(t, fixture.repo.FailDispatch(ctx, dispatch.ID, "other", "wrong owner", 2, now), repository.ErrAutomationDispatchLease)
	require.NoError(t, fixture.repo.FailDispatch(ctx, dispatch.ID, "owner", "retry", 2, now))
	var status string
	var nextAttempt time.Time
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status, next_attempt_at FROM automation_dispatch_outbox WHERE id = ?`, dispatch.ID).Scan(&status, &nextAttempt))
	require.Equal(t, "pending", status)
	require.True(t, nextAttempt.After(now))
	leased, err = fixture.repo.LeaseNextDispatch(ctx, "owner", nextAttempt.Add(time.Millisecond), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, leased)
	require.NoError(t, fixture.repo.FailDispatch(ctx, dispatch.ID, "owner", "terminal", 2, nextAttempt.Add(time.Millisecond)))
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status FROM automation_dispatch_outbox WHERE id = ?`, dispatch.ID).Scan(&status))
	require.Equal(t, "failed", status)
	var invocationStatus string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status FROM automation_invocations WHERE id = ?`, dispatch.InvocationID).Scan(&invocationStatus))
	require.Equal(t, "failed", invocationStatus)
	var reservations int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_task_run_reservations WHERE dispatch_id = ?`, dispatch.ID).Scan(&reservations))
	require.Zero(t, reservations)
}

func TestAutomationRuntimeSharedInboxExecutionPreservesMultipleBindings(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	secondSchedule := models.Schedule{TaskID: fixture.task.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatHours, RepeatInterval: 1, Enabled: true}
	require.NoError(t, fixture.schedRepo.Create(ctx, &secondSchedule))
	second, _, err := NewAutomationRegistrationService(fixture.repo, NewAutomationAdapterRegistry()).Register(ctx, AutomationRegistrationRequest{
		ProjectID: fixture.project.ID, AdapterKey: AutomationAdapterNativeSDLC, StableKey: "native-sdlc/multi-binding",
		Resources: []models.AutomationResourceBinding{{NodeKey: "inbox", ResourceType: "schedule", ResourceID: secondSchedule.ID}, {NodeKey: "inbox", ResourceType: "task", ResourceID: fixture.task.ID}},
	})
	require.NoError(t, err)
	definitions := []*models.AutomationDefinition{fixture.definition, second}
	var bindings []models.AutomationBinding
	for i, definition := range definitions {
		inbox := automationNodeByKey(t, definition, "inbox")
		binding := models.AutomationBinding{AutomationID: definition.Automation.ID, VersionID: definition.Version.ID, NodeID: inbox.ID}
		item, _, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			WorkItemKey: fmt.Sprintf("shared:item:%d", i), ActivityKey: fmt.Sprintf("shared:seed:%d", i), ActivityType: "shared_inbox", ActivityStatus: models.AutomationActivityRunning,
			Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: fixture.task.ID}},
		})
		require.NoError(t, err)
		binding.WorkItemID = item.ID
		bindings = append(bindings, binding)
	}
	taskContext, err := fixture.repo.ContextForTask(ctx, fixture.project.ID, fixture.task.ID)
	require.NoError(t, err)
	require.Len(t, taskContext.Bindings, 2)
	execution := models.Execution{TaskID: fixture.task.ID, Status: models.ExecRunning, PromptSent: "shared inbox"}
	require.NoError(t, repository.NewExecutionRepo(fixture.repo.DB()).Create(ctx, &execution))
	for i, binding := range bindings {
		_, _, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: bindings}, Binding: binding,
			ActivityKey: fmt.Sprintf("shared:execution:%s:%d", execution.ID, i), ActivityType: "thread_input_execution", ActivityStatus: models.AutomationActivityRunning,
			Resources: []models.AutomationActivityResource{{ResourceType: "execution", ResourceID: execution.ID}, {ResourceType: "task", ResourceID: fixture.task.ID}},
		})
		require.NoError(t, err)
	}
	executionContext, err := fixture.repo.ContextForExecution(ctx, fixture.project.ID, execution.ID)
	require.NoError(t, err)
	require.Len(t, executionContext.Bindings, 2, "one shared execution must retain both causal automation bindings")
	require.NotEqual(t, executionContext.Bindings[0].WorkItemID, executionContext.Bindings[1].WorkItemID)
}

func TestAutomationRuntimeThreadInputBindingSurvivesPromotion(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	llmRepo := repository.NewLLMConfigRepo(fixture.repo.DB())
	agent := models.LLMConfig{Name: "Automation queue", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmRepo.Create(ctx, &agent))
	inputRepo := repository.NewThreadInputRepo(fixture.repo.DB())
	input := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: fixture.project.ID, TaskID: fixture.task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "continue automation"}
	require.NoError(t, inputRepo.CreateQueued(ctx, &input))
	inbox := automationNodeByKey(t, fixture.definition, "inbox")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, NodeID: inbox.ID, InvocationID: ""}
	// A queued binding must have an invocation or work item. Create a durable work item first.
	item, _, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "alert:queued", WorkItemKind: "suggestion", ActivityKey: "alert:queued:seed", ActivityType: "seed", ActivityStatus: models.AutomationActivityCompleted,
	})
	require.NoError(t, err)
	binding.WorkItemID = item.ID
	automationContext := models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}
	require.NoError(t, fixture.repo.BindThreadInput(ctx, input.ID, automationContext, "queued-work"))
	promoted := models.Execution{TaskID: fixture.task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: input.Content, IsFollowup: true}
	require.NoError(t, inputRepo.ClaimQueuedForTaskExecution(ctx, input.ID, &promoted))
	loaded, err := fixture.repo.ContextForThreadInput(ctx, fixture.project.ID, input.ID)
	require.NoError(t, err)
	require.Len(t, loaded.Bindings, 1)
	require.Equal(t, item.ID, loaded.Bindings[0].WorkItemID)
	var activityCount int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activities a JOIN automation_activity_resources ar ON ar.activity_id = a.id WHERE ar.resource_type = 'execution' AND ar.resource_id = ?`, promoted.ID).Scan(&activityCount))
	require.Equal(t, 1, activityCount)
	_, err = fixture.repo.DB().Exec(`DELETE FROM thread_inputs WHERE id = ?`, input.ID)
	require.NoError(t, err)
	var remainingBindings int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_thread_input_bindings WHERE thread_input_id = ?`, input.ID).Scan(&remainingBindings))
	require.Zero(t, remainingBindings, "deleting the authoritative queued input must cascade its causal bindings")

	foreignProject := automationTestProject(t, repository.NewProjectRepo(fixture.repo.DB()), "Foreign queued binding")
	foreignTask := models.Task{ProjectID: foreignProject.ID, Title: "Foreign queue", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, repository.NewTaskRepo(fixture.repo.DB(), nil).Create(ctx, &foreignTask))
	foreignInput := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: foreignProject.ID, TaskID: foreignTask.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "foreign"}
	require.NoError(t, inputRepo.CreateQueued(ctx, &foreignInput))
	require.ErrorContains(t, fixture.repo.BindThreadInput(ctx, foreignInput.ID, automationContext, "foreign"), "project mismatch")
}

func TestAutomationRuntimeGitHubIssueInboxAndPRProvenance(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterGitHubSDLC)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(fixture.repo.DB())
	fixture.project.RepoURL = "https://github.com/example/runtime.git"
	fixture.project.RepoPath = t.TempDir()
	require.NoError(t, projectRepo.Update(ctx, &fixture.project))
	require.NoError(t, fixture.repo.DB().QueryRow(`UPDATE tasks SET worktree_branch = 'task/runtime' WHERE id = ? RETURNING id`, fixture.task.ID).Scan(new(string)))
	execution := models.Execution{TaskID: fixture.task.ID, Status: models.ExecRunning, PromptSent: "github runtime"}
	require.NoError(t, repository.NewExecutionRepo(fixture.repo.DB()).Create(ctx, &execution))
	bugFinder := automationNodeByKey(t, fixture.definition, "bug_finder")
	invocation, _, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Now().UTC(), fixture.schedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: invocation.ID, NodeID: bugFinder.ID}
	ctx = WithAutomationContext(ctx, models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}})
	ctx = withAutomationExecution(ctx, fixture.task.ID, execution.ID)

	var createCalls atomic.Int32
	var resolvedRepoMu sync.Mutex
	var resolvedRepoURLs []string
	var resolvedRepoPaths []string
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*GitHubRepoRef, error) {
			resolvedRepoMu.Lock()
			defer resolvedRepoMu.Unlock()
			resolvedRepoURLs = append(resolvedRepoURLs, repoURL)
			resolvedRepoPaths = append(resolvedRepoPaths, repoPath)
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
		},
		createIssueFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
			createCalls.Add(1)
			return &GitHubIssue{Number: 42, URL: "https://github.com/example/runtime/issues/42", Title: req.Title, State: "open"}, nil
		},
		listMyIssuesFn: func(context.Context, *GitHubRepoRef) (*GitHubAuthenticatedUser, []GitHubIssue, error) {
			return &GitHubAuthenticatedUser{Login: "dev"}, []GitHubIssue{{Number: 42, Title: "Exact issue", State: "open"}, {Number: 43, Title: "Second issue", State: "open"}}, nil
		},
		createPRFn: func(context.Context, *GitHubRepoRef, GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
			return &GitHubPullRequest{Number: 7, URL: "https://github.com/example/runtime/pull/7", State: "open"}, nil
		},
	}
	opts := githubIssueRuntimeOptions{ProjectID: fixture.project.ID, ProjectRepo: projectRepo, TaskRepo: fixture.taskRepo,
		TaskPullRequestRepo: repository.NewTaskPullRequestRepo(fixture.repo.DB()), AutomationRepo: fixture.repo, GitHub: provider}
	handlers := buildGitHubIssueRuntimeHandlers(opts)
	input := json.RawMessage(`{"title":"Exact issue","body":"body","labels":["bug"]}`)
	first, err := handlers["github_create_issue"](ctx, input)
	require.NoError(t, err)
	require.Contains(t, first, `"Number":42`)
	second, err := handlers["github_create_issue"](ctx, input)
	require.NoError(t, err)
	require.Contains(t, second, `"reused":true`)
	require.Equal(t, int32(1), createCalls.Load(), "successful issue creation retry must resolve persisted provenance")

	ambiguousInput := githubCreateIssueRuntimeInput{Title: "Ambiguous issue", Body: "body"}
	repoRef, err := provider.ResolveRepo(ctx, fixture.project.RepoURL, fixture.project.RepoPath)
	require.NoError(t, err)
	ambiguousKey := githubIssueCreationActivityKey(ctx, repoRef, ambiguousInput)
	resourceID, err := fixture.repo.ReserveExternalActivity(ctx, fixture.project.ID, binding, ambiguousKey, "create_github_issue", "github_issue")
	require.NoError(t, err)
	require.Empty(t, resourceID)
	_, err = handlers["github_create_issue"](ctx, json.RawMessage(`{"title":"Ambiguous issue","body":"body"}`))
	require.ErrorIs(t, err, repository.ErrAutomationExternalReconciliation)
	require.Equal(t, int32(1), createCalls.Load(), "an ambiguous prior mutation must not call GitHub again")

	devInbox := automationNodeByKey(t, fixture.definition, "dev_inbox")
	inboxBinding := binding
	inboxBinding.NodeID = devInbox.ID
	inboxCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{inboxBinding}})
	_, err = handlers["github_create_issue"](inboxCtx, json.RawMessage(`{"title":"Unauthorized inbox issue"}`))
	require.ErrorContains(t, err, "not authorized by the caller's Automation graph")
	require.Equal(t, int32(1), createCalls.Load(), "an Automation node without a create-issue edge must fail closed")
	_, err = handlers["github_list_my_assigned_issues"](inboxCtx, json.RawMessage(`{}`))
	require.NoError(t, err)
	var issueItems int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_work_items WHERE automation_id = ? AND kind = 'github_issue'`, fixture.definition.Automation.ID).Scan(&issueItems))
	require.Equal(t, 2, issueItems, "one shared inbox execution must preserve distinct issue work items")

	taskSvc := NewTaskService(fixture.taskRepo, nil, nil)
	implementationNode := automationNodeByKey(t, fixture.definition, "implementation")
	require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `UPDATE automation_nodes SET config_json = ? WHERE id = ? RETURNING id`,
		`{"prompt":"Implement the exact assigned GitHub issue and open a pull request.","category":"backlog","priority":2}`, implementationNode.ID).Scan(&implementationNode.ID))
	llmSvc := &LLMService{automationRepo: fixture.repo, githubIssueRuntime: provider, projectRepo: projectRepo,
		taskRepo: fixture.taskRepo, taskSvc: taskSvc}
	runtime := llmSvc.taskControlRuntimeTools(fixture.task)
	require.NotNil(t, runtime)

	resolvedRepoURLs = nil
	resolvedRepoPaths = nil
	beforeTasks, err := fixture.taskRepo.ListByProject(ctx, fixture.project.ID, "")
	require.NoError(t, err)
	invalidOutput, handled, isErr, invalidErr := runtime.Executor(inboxCtx, "create_task", json.RawMessage(`{
		"title":"Undiscovered issue task","prompt":"must not persist","category":"active",
		"source_github_issue_number":999,"source_github_repo_url":"https://github.com/attacker/override"
	}`))
	require.True(t, handled)
	require.True(t, isErr)
	require.Error(t, invalidErr)
	require.Empty(t, invalidOutput)
	afterInvalidTasks, err := fixture.taskRepo.ListByProject(ctx, fixture.project.ID, "")
	require.NoError(t, err)
	require.Len(t, afterInvalidTasks, len(beforeTasks), "an undiscovered issue must fail before any task is persisted")

	configuredRepoURL := fixture.project.RepoURL
	fixture.project.RepoURL = ""
	require.NoError(t, projectRepo.Update(ctx, &fixture.project))
	_, handled, isErr, missingRepoErr := runtime.Executor(inboxCtx, "create_task", json.RawMessage(`{
		"title":"Missing repository issue task","prompt":"must not persist",
		"source_github_issue_number":42,"source_github_repo_url":"https://github.com/attacker/fallback"
	}`))
	require.True(t, handled)
	require.True(t, isErr)
	require.ErrorContains(t, missingRepoErr, "repository URL is unavailable")
	afterMissingRepoTasks, err := fixture.taskRepo.ListByProject(ctx, fixture.project.ID, "")
	require.NoError(t, err)
	require.Len(t, afterMissingRepoTasks, len(beforeTasks), "missing explicit project repo_url must fail before task persistence")
	fixture.project.RepoURL = configuredRepoURL
	require.NoError(t, projectRepo.Update(ctx, &fixture.project))

	firstOutput, handled, isErr, err := runtime.Executor(inboxCtx, "create_task", json.RawMessage(`{
		"title":"Implement exact issue","prompt":"opaque implementation prompt","category":"backlog",
		"source_github_issue_number":42,"source_github_repo_url":"https://github.com/attacker/override"
	}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr)
	implementationTask, err := fixture.taskRepo.GetByProjectAndTitle(ctx, fixture.project.ID, "Implement exact issue")
	require.NoError(t, err)
	require.NotNil(t, implementationTask)
	require.Equal(t, repository.AutomationCompilerTaskCreatedVia(fixture.definition.Automation.ID, implementationNode.NodeKey), implementationTask.CreatedVia,
		"issue-specific Automation Tasks need a durable origin marker after graph replacement deletes projection")
	require.Contains(t, firstOutput, implementationTask.ID)
	implementationContext, err := fixture.repo.ContextForTask(ctx, fixture.project.ID, implementationTask.ID)
	require.NoError(t, err)
	require.Len(t, implementationContext.Bindings, 1)
	issue42Context, err := fixture.repo.BindingsForWorkItemKey(ctx, fixture.project.ID, "github:example/runtime:issue:42")
	require.NoError(t, err)
	require.Equal(t, issue42Context.Bindings[0].WorkItemID, implementationContext.Bindings[0].WorkItemID,
		"implementation task must bind atomically to the exact persisted issue selected by source_github_issue_number")

	secondOutput, handled, isErr, err := runtime.Executor(inboxCtx, "create_task", json.RawMessage(`{
		"title":"Duplicate model title for issue 42","prompt":"must reuse canonical task","category":"backlog",
		"source_github_issue_number":42,"source_github_repo_url":"https://github.com/another/override"
	}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr)
	require.Contains(t, secondOutput, implementationTask.ID)
	afterDuplicateTasks, err := fixture.taskRepo.ListByProject(ctx, fixture.project.ID, "")
	require.NoError(t, err)
	require.Len(t, afterDuplicateTasks, len(beforeTasks)+1, "one issue work item must have at most one implementation task")

	telegram := &TelegramService{taskSvc: taskSvc, taskRepo: fixture.taskRepo, llmSvc: llmSvc}
	telegramHandlers := telegram.telegramActionHandlersForTask(fixture.project.ID, fixture.task.ID, 123, 456, nil)
	channelOutput, err := telegramHandlers["create_task"](inboxCtx, json.RawMessage(`{
		"title":"Channel duplicate title for issue 42","prompt":"must reuse canonical task",
		"source_github_issue_number":42,"source_github_repo_url":"https://github.com/channel/override"
	}`))
	require.NoError(t, err)
	require.Contains(t, channelOutput, implementationTask.ID)
	afterChannelTasks, err := fixture.taskRepo.ListByProject(ctx, fixture.project.ID, "")
	require.NoError(t, err)
	require.Len(t, afterChannelTasks, len(beforeTasks)+1, "task-bound channel create_task must use exact Automation provenance")

	type taskCreationCallResult struct {
		output  string
		handled bool
		isErr   bool
		err     error
	}
	startConcurrentCreation := make(chan struct{})
	concurrentResults := make(chan taskCreationCallResult, 2)
	for _, title := range []string{"First concurrent title for issue 43", "Second concurrent title for issue 43"} {
		title := title
		go func() {
			<-startConcurrentCreation
			payload, marshalErr := json.Marshal(TaskCreationRequest{Title: title, Prompt: "implement issue 43",
				Category: string(models.CategoryBacklog), SourceGitHubIssueNumber: 43, SourceGitHubRepoURL: "https://github.com/attacker/concurrent"})
			if marshalErr != nil {
				concurrentResults <- taskCreationCallResult{err: marshalErr}
				return
			}
			output, handled, isErr, callErr := runtime.Executor(inboxCtx, "create_task", payload)
			concurrentResults <- taskCreationCallResult{output: output, handled: handled, isErr: isErr, err: callErr}
		}()
	}
	close(startConcurrentCreation)
	concurrentOutputs := make([]string, 0, 2)
	for range 2 {
		result := <-concurrentResults
		require.NoError(t, result.err)
		require.True(t, result.handled)
		require.False(t, result.isErr)
		concurrentOutputs = append(concurrentOutputs, result.output)
	}
	afterConcurrentTasks, err := fixture.taskRepo.ListByProject(ctx, fixture.project.ID, "")
	require.NoError(t, err)
	require.Len(t, afterConcurrentTasks, len(beforeTasks)+2, "concurrent creation must persist one canonical task for issue 43")
	var issue43Task *models.Task
	for i := range afterConcurrentTasks {
		if strings.Contains(afterConcurrentTasks[i].Title, "concurrent title for issue 43") {
			issue43Task = &afterConcurrentTasks[i]
			break
		}
	}
	require.NotNil(t, issue43Task)
	for _, output := range concurrentOutputs {
		require.Contains(t, output, issue43Task.ID)
	}
	resolvedRepoMu.Lock()
	for _, repoURL := range resolvedRepoURLs {
		require.Equal(t, fixture.project.RepoURL, repoURL, "Automation task provenance must ignore model repository overrides")
	}
	for _, repoPath := range resolvedRepoPaths {
		require.Empty(t, repoPath, "Automation task provenance must never receive repo_path")
	}
	resolvedRepoMu.Unlock()
	resolvedRepoPaths = nil

	_, err = handlers["github_open_pull_request"](ctx, json.RawMessage(fmt.Sprintf(`{"task_id":%q,"issue_number":42,"pr_title":"PR"}`, fixture.task.ID)))
	require.ErrorContains(t, err, "not authorized by the caller's Automation graph")
	_, err = handlers["github_open_pull_request"](ctx, json.RawMessage(fmt.Sprintf(`{"task_id":%q,"issue_number":42,"pr_title":"PR"}`, implementationTask.ID)))
	require.ErrorContains(t, err, "cannot mutate a different task")
	implementationWorktree := t.TempDir()
	require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `UPDATE tasks SET worktree_path = ?, worktree_branch = ? WHERE id = ? RETURNING id`,
		implementationWorktree, "task/issue-42", implementationTask.ID).Scan(&implementationTask.ID))
	implementationTask.WorktreePath = implementationWorktree
	implementationTask.WorktreeBranch = "task/issue-42"
	implementationCtx := WithAutomationContext(context.Background(), implementationContext)
	implementationCtx = withAutomationExecution(implementationCtx, implementationTask.ID, execution.ID)
	_, err = handlers["github_open_pull_request"](implementationCtx, json.RawMessage(fmt.Sprintf(`{"task_id":%q,"issue_number":42,"pr_title":"PR"}`, implementationTask.ID)))
	require.NoError(t, err)
	for _, repoPath := range resolvedRepoPaths {
		require.Empty(t, repoPath, "Automation GitHub repository resolution must never receive repo_path")
	}
	_, err = handlers["github_replace_pull_request_branch"](implementationCtx, json.RawMessage(fmt.Sprintf(`{"task_id":%q,"expected_head_sha":%q,"confirm_history_rewrite":true}`, fixture.task.ID, strings.Repeat("a", 40))))
	require.ErrorContains(t, err, "cannot mutate a different task")
	var prResources int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activity_resources WHERE resource_type = 'pull_request' AND resource_id = 'github:example/runtime:pull:7'`).Scan(&prResources))
	require.Equal(t, 1, prResources)
	edgeExpectations := map[string]int{
		"bug_to_issue": 1, "issue_to_assignment": 1, "assignment_to_inbox": 2,
		"inbox_to_implementation": 2, "implementation_to_pr": 1, "pr_to_review": 1,
	}
	for edgeKey, expected := range edgeExpectations {
		var edgeTransitions int
		require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_transitions tr
			JOIN automation_edges e ON e.id = tr.edge_id WHERE tr.automation_id = ? AND e.edge_key = ?`,
			fixture.definition.Automation.ID, edgeKey).Scan(&edgeTransitions))
		require.Equal(t, expected, edgeTransitions, edgeKey+" must be represented by exact persisted provenance")
	}
	var waitingReview int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_work_item_positions p JOIN automation_nodes n ON n.id = p.node_id WHERE p.automation_id = ? AND n.node_key = 'review' AND p.state = 'waiting'`, fixture.definition.Automation.ID).Scan(&waitingReview))
	require.Equal(t, 1, waitingReview)
}

func newAutomationGitHubIssueCausalContext(t *testing.T, fixture automationRuntimeFixture, definition *models.AutomationDefinition, task models.Task, nodeKey, occurrence string) context.Context {
	t.Helper()
	ctx := context.Background()
	sourceNode := automationNodeByKey(t, definition, nodeKey)
	var invocationID string
	require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `INSERT INTO automation_invocations
		(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status, started_at)
		VALUES (?, ?, ?, ?, 'schedule', ?, ?, 'running', CURRENT_TIMESTAMP) RETURNING id`, fixture.project.ID,
		definition.Automation.ID, definition.Version.ID, sourceNode.ID, fixture.schedule.ID, occurrence).Scan(&invocationID))
	execution := models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: occurrence}
	require.NoError(t, repository.NewExecutionRepo(fixture.repo.DB()).Create(ctx, &execution))
	binding := models.AutomationBinding{AutomationID: definition.Automation.ID, VersionID: definition.Version.ID,
		InvocationID: invocationID, NodeID: sourceNode.ID}
	causalCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}})
	return withAutomationExecution(causalCtx, task.ID, execution.ID)
}

func newAutomationGitHubIssueDedupHarness(t *testing.T, provider GitHubIssueRuntimeProvider) (automationRuntimeFixture, func(string) context.Context, func(context.Context, json.RawMessage) (string, error)) {
	t.Helper()
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterGitHubSDLC)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(fixture.repo.DB())
	fixture.project.RepoURL = "https://github.com/example/runtime.git"
	fixture.project.RepoPath = t.TempDir()
	require.NoError(t, projectRepo.Update(ctx, &fixture.project))
	newCausalContext := func(occurrence string) context.Context {
		t.Helper()
		return newAutomationGitHubIssueCausalContext(t, fixture, fixture.definition, fixture.task, "bug_finder", occurrence)
	}
	handlers := buildGitHubIssueRuntimeHandlers(githubIssueRuntimeOptions{ProjectID: fixture.project.ID, ProjectRepo: projectRepo,
		TaskRepo: fixture.taskRepo, AutomationRepo: fixture.repo, GitHub: provider})
	return fixture, newCausalContext, handlers["github_create_issue"]
}

func assertAutomationGitHubIssueProjection(t *testing.T, fixture automationRuntimeFixture, issueNumber int, activityKey string) {
	t.Helper()
	resourceID := fmt.Sprintf("github:example/runtime:issue:%d", issueNumber)
	var workItems int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_work_items
		WHERE project_id = ? AND automation_id = ? AND work_item_key = ? AND kind = 'github_issue' AND status = 'waiting'`,
		fixture.project.ID, fixture.definition.Automation.ID, resourceID).Scan(&workItems))
	require.Equal(t, 1, workItems, "the created issue must have one waiting Automation work item")

	var activities int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activities
		WHERE project_id = ? AND automation_id = ? AND version_id = ? AND activity_key = ?
			AND activity_type = 'create_github_issue' AND status = 'completed' AND work_item_id IS NOT NULL`,
		fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID, activityKey).Scan(&activities))
	require.Equal(t, 1, activities, "the original issue activity must be completed against the work item")
	var totalActivities int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activities
		WHERE project_id = ? AND automation_id = ? AND version_id = ? AND activity_type = 'create_github_issue'`,
		fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID).Scan(&totalActivities))
	require.Equal(t, 1, totalActivities, "retries must not leave duplicate or pending issue activities")

	var resources int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activity_resources ar
		JOIN automation_activities a ON a.id = ar.activity_id
		WHERE a.project_id = ? AND a.automation_id = ? AND a.version_id = ? AND a.activity_key = ?
			AND ((ar.resource_type = 'github_issue' AND ar.resource_id = ?)
				OR ar.resource_type = 'task' OR ar.resource_type = 'execution')`, fixture.project.ID,
		fixture.definition.Automation.ID, fixture.definition.Version.ID, activityKey, resourceID).Scan(&resources))
	require.Equal(t, 3, resources, "the repaired activity must retain issue, task, and execution provenance")

	for edgeKey, state := range map[string]string{"bug_to_issue": "entered", "issue_to_assignment": "waiting"} {
		var transitions int
		require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_transitions tr
			JOIN automation_edges e ON e.id = tr.edge_id
			WHERE tr.project_id = ? AND tr.automation_id = ? AND tr.version_id = ? AND e.edge_key = ? AND tr.state = ?`,
			fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID, edgeKey, state).Scan(&transitions))
		require.Equal(t, 1, transitions, edgeKey+" must be recorded exactly once")
	}
	var waitingAssignment int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_work_item_positions p
		JOIN automation_nodes n ON n.id = p.node_id
		JOIN automation_work_items wi ON wi.id = p.work_item_id
		WHERE p.project_id = ? AND p.automation_id = ? AND p.version_id = ? AND n.node_key = 'assignment'
			AND p.state = 'waiting' AND wi.work_item_key = ?`, fixture.project.ID, fixture.definition.Automation.ID,
		fixture.definition.Version.ID, resourceID).Scan(&waitingAssignment))
	require.Equal(t, 1, waitingAssignment, "the issue must wait at the human assignment gate")
}

func replaceAutomationGitHubIssueGraph(t *testing.T, fixture automationRuntimeFixture) *models.AutomationDefinition {
	t.Helper()
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(fixture.repo.DB())
	settingsRepo := repository.NewSettingsRepo(fixture.repo.DB())
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT))
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingPAT, "test-token"))
	githubAuthRepo := repository.NewGitHubAuthRepo(fixture.repo.DB())
	require.NoError(t, githubAuthRepo.UpsertProjectInbox(ctx, &models.GitHubProjectInbox{
		ProjectID: fixture.project.ID, GitHubLogin: "automation-bot", Enabled: true,
	}))
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(fixture.repo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterGitHubSDLC)
	require.NoError(t, err)
	validator := NewAutomationSaveValidator(registry, drafts)
	validator.SetCapabilityDependencies(projectRepo, settingsRepo, githubAuthRepo)
	taskSvc := NewTaskService(fixture.taskRepo, repository.NewAttachmentRepo(fixture.repo.DB()), nil)
	compiler := NewAutomationCompiler(fixture.repo, taskSvc, fixture.taskRepo, fixture.schedRepo, validator)
	saved, err := compiler.Save(ctx, AutomationSaveRequest{ProjectID: fixture.project.ID,
		AutomationID: fixture.definition.Automation.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.NotEqual(t, fixture.definition.Version.ID, saved.Definition.Version.ID)
	return saved.Definition
}

func TestAutomationGitHubIssueCreationRejectsCompletedClaimFromDifferentAutomation(t *testing.T) {
	var createCalls atomic.Int32
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime"}, nil
		},
		createIssueFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
			createCalls.Add(1)
			return &GitHubIssue{Number: 93, URL: "https://github.com/example/runtime/issues/93", Title: req.Title, State: "open"}, nil
		},
	}
	fixture, newFirstContext, createIssue := newAutomationGitHubIssueDedupHarness(t, provider)
	ctx := context.Background()
	secondTask, secondSchedule := automationTestTaskAndSchedule(t, fixture.taskRepo, fixture.schedRepo, fixture.project.ID, "Second GitHub Automation")
	secondDefinition, _, err := NewAutomationRegistrationService(fixture.repo, NewAutomationAdapterRegistry()).Register(ctx, AutomationRegistrationRequest{
		ProjectID: fixture.project.ID, AdapterKey: AutomationAdapterGitHubSDLC, StableKey: "github-sdlc/runtime-second",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "dev_inbox", ResourceType: "schedule", ResourceID: secondSchedule.ID},
			{NodeKey: "dev_inbox", ResourceType: "task", ResourceID: secondTask.ID},
		},
	})
	require.NoError(t, err)
	input := json.RawMessage(`{"title":"Cross Automation collision","body":"body"}`)

	firstOutput, firstErr := createIssue(newFirstContext("cross-automation-first"), input)
	require.NoError(t, firstErr)
	require.Contains(t, firstOutput, `"Number":93`)
	secondCtx := newAutomationGitHubIssueCausalContext(t, fixture, secondDefinition, secondTask, "bug_finder", "cross-automation-second")
	_, secondErr := createIssue(secondCtx, json.RawMessage(`{"title":"  cross   automation COLLISION ","body":"different"}`))
	require.ErrorIs(t, secondErr, repository.ErrAutomationExternalReconciliation)
	require.ErrorContains(t, secondErr, "different Automation source")
	require.Equal(t, int32(1), createCalls.Load(), "a source collision must neither reuse nor recreate the issue")
	var secondActivities int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activities
		WHERE project_id = ? AND automation_id = ? AND activity_type = 'create_github_issue'`,
		fixture.project.ID, secondDefinition.Automation.ID).Scan(&secondActivities))
	require.Zero(t, secondActivities, "the colliding Automation must not adopt the original issue projection")
}

func TestAutomationGitHubIssueCreationRejectsCompletedClaimAfterGraphReplacement(t *testing.T) {
	var createCalls atomic.Int32
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime"}, nil
		},
		createIssueFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
			createCalls.Add(1)
			return &GitHubIssue{Number: 94, URL: "https://github.com/example/runtime/issues/94", Title: req.Title, State: "open"}, nil
		},
	}
	fixture, newOriginalContext, createIssue := newAutomationGitHubIssueDedupHarness(t, provider)
	input := json.RawMessage(`{"title":"Replaced graph collision","body":"body"}`)
	_, firstErr := createIssue(newOriginalContext("replacement-first"), input)
	require.NoError(t, firstErr)
	oldVersionID := fixture.definition.Version.ID
	replacement := replaceAutomationGitHubIssueGraph(t, fixture)
	var oldVersions int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_versions WHERE id = ?`, oldVersionID).Scan(&oldVersions))
	require.Zero(t, oldVersions, "Save must delete the original graph before the retry")
	replacementTaskID := automationResourceID(t, replacement, "bug_finder", "task")
	replacementTask, err := fixture.taskRepo.GetByID(context.Background(), replacementTaskID)
	require.NoError(t, err)
	require.NotNil(t, replacementTask)

	replacementCtx := newAutomationGitHubIssueCausalContext(t, fixture, replacement, *replacementTask, "bug_finder", "replacement-second")
	_, retryErr := createIssue(replacementCtx, input)
	require.ErrorIs(t, retryErr, repository.ErrAutomationExternalReconciliation)
	require.ErrorContains(t, retryErr, "different Automation source")
	require.Equal(t, int32(1), createCalls.Load(), "graph replacement must neither adopt nor recreate the original issue")
}

func TestAutomationGitHubIssueProjectionRepairRejectsDeletedSourceGraph(t *testing.T) {
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime"}, nil
		},
		createIssueFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
			return &GitHubIssue{Number: 95, URL: "https://github.com/example/runtime/issues/95", Title: req.Title, State: "open"}, nil
		},
	}
	fixture, newOriginalContext, createIssue := newAutomationGitHubIssueDedupHarness(t, provider)
	title := "Deleted projection source"
	_, firstErr := createIssue(newOriginalContext("deleted-source-first"), json.RawMessage(`{"title":"Deleted projection source","body":"body"}`))
	require.NoError(t, firstErr)
	fingerprint := githubIssueTitleFingerprint(title)
	var ownerToken, sourceJSON string
	var issueNumber int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT owner_token, projection_source_json, created_issue_number
		FROM automation_github_issue_dedup_leases
		WHERE project_id = ? AND repository_full_name = ? AND title_fingerprint = ?`, fixture.project.ID, "example/runtime", fingerprint).
		Scan(&ownerToken, &sourceJSON, &issueNumber))
	var source repository.AutomationGitHubIssueDedupSource
	require.NoError(t, json.Unmarshal([]byte(sourceJSON), &source))
	replaceAutomationGitHubIssueGraph(t, fixture)

	projectRepo := repository.NewProjectRepo(fixture.repo.DB())
	opts := githubIssueRuntimeOptions{ProjectID: fixture.project.ID, ProjectRepo: projectRepo,
		TaskRepo: fixture.taskRepo, AutomationRepo: fixture.repo, GitHub: provider}
	_, repairErr := repairAutomationGitHubIssueProjection(opts, &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime"}, title,
		repository.AutomationGitHubIssueDedupClaim{IssueNumber: issueNumber, OwnerToken: ownerToken, Source: source})
	require.ErrorContains(t, repairErr, "expected GitHub issue projection")
}

func TestAutomationGitHubIssueCreationFailsClosedAfterAmbiguousProviderOutcome(t *testing.T) {
	var createCalls atomic.Int32
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime"}, nil
		},
		createIssueFn: func(context.Context, *GitHubRepoRef, GitHubCreateIssueRequest) (*GitHubIssue, error) {
			createCalls.Add(1)
			return nil, errors.New("provider timeout after request dispatch")
		},
	}
	fixture, newCausalContext, createIssue := newAutomationGitHubIssueDedupHarness(t, provider)
	input := json.RawMessage(`{"title":"Ambiguous post-mutation issue","body":"body"}`)

	_, firstErr := createIssue(newCausalContext("ambiguous-first"), input)
	require.ErrorContains(t, firstErr, "provider timeout")
	fingerprint := githubIssueTitleFingerprint("Ambiguous post-mutation issue")
	_, err := fixture.repo.DB().Exec(`UPDATE automation_github_issue_dedup_leases SET lease_expires_at = ?
		WHERE project_id = ? AND repository_full_name = ? AND title_fingerprint = ?`, time.Now().UTC().Add(-time.Hour),
		fixture.project.ID, "example/runtime", fingerprint)
	require.NoError(t, err)

	_, retryErr := createIssue(newCausalContext("ambiguous-retry"), input)
	require.ErrorIs(t, retryErr, repository.ErrAutomationExternalReconciliation)
	require.Equal(t, int32(1), createCalls.Load(), "lease expiry must not retry a create whose external outcome is uncertain")
	var mutationState string
	var recordedNumber sql.NullInt64
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT mutation_state, created_issue_number FROM automation_github_issue_dedup_leases
		WHERE project_id = ? AND repository_full_name = ? AND title_fingerprint = ?`, fixture.project.ID, "example/runtime", fingerprint).
		Scan(&mutationState, &recordedNumber))
	require.Equal(t, "dispatched", mutationState)
	require.False(t, recordedNumber.Valid, "an uncertain external outcome must remain numberless and fail closed")
}

func TestAutomationGitHubIssueCreationRecordsSuccessDespiteRequestCancellation(t *testing.T) {
	var createCalls atomic.Int32
	var cancelFirst context.CancelFunc
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime"}, nil
		},
		createIssueFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
			if createCalls.Add(1) == 1 {
				cancelFirst()
			}
			return &GitHubIssue{Number: 91, URL: "https://github.com/example/runtime/issues/91", Title: req.Title, State: "open"}, nil
		},
	}
	fixture, newCausalContext, createIssue := newAutomationGitHubIssueDedupHarness(t, provider)
	firstBaseCtx := newCausalContext("canceled-success-first")
	firstCtx, cancel := context.WithCancel(firstBaseCtx)
	cancelFirst = cancel
	defer cancel()
	input := json.RawMessage(`{"title":"Canceled successful issue","body":"body"}`)
	activityKey := githubIssueCreationActivityKey(firstBaseCtx, &GitHubRepoRef{FullName: "example/runtime"},
		githubCreateIssueRuntimeInput{Title: "Canceled successful issue", Body: "body"})

	firstOutput, firstErr := createIssue(firstCtx, input)
	require.NoError(t, firstErr)
	require.Contains(t, firstOutput, `"Number":91`)
	fingerprint := githubIssueTitleFingerprint("Canceled successful issue")
	var mutationState string
	var recordedNumber sql.NullInt64
	var projectionSource string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT mutation_state, created_issue_number, projection_source_json
		FROM automation_github_issue_dedup_leases
		WHERE project_id = ? AND repository_full_name = ? AND title_fingerprint = ?`, fixture.project.ID, "example/runtime", fingerprint).
		Scan(&mutationState, &recordedNumber, &projectionSource))
	if !recordedNumber.Valid || recordedNumber.Int64 != 91 {
		t.Errorf("created issue number after canceled request = %v, want 91", recordedNumber)
	}
	require.Equal(t, "completed", mutationState)
	require.NotContains(t, projectionSource, "Canceled successful issue")
	require.NotContains(t, projectionSource, "body")
	require.Contains(t, projectionSource, fixture.definition.Automation.ID)
	assertAutomationGitHubIssueProjection(t, fixture, 91, activityKey)

	sameOutput, sameErr := createIssue(firstBaseCtx, input)
	require.NoError(t, sameErr)
	require.Contains(t, sameOutput, `"reused":true`)
	laterOutput, laterErr := createIssue(newCausalContext("canceled-success-retry"), input)
	require.NoError(t, laterErr)
	require.Contains(t, laterOutput, `"Number":91`)
	require.Contains(t, laterOutput, `"reused":true`)
	assertAutomationGitHubIssueProjection(t, fixture, 91, activityKey)
	require.Equal(t, int32(1), createCalls.Load(), "request cancellation after provider success must not allow a second create")
}

func TestAutomationGitHubIssueCreationRepairsCompletedClaimProjection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		triggerSQL string
	}{
		{
			name: "before work item",
			triggerSQL: `CREATE TRIGGER fail_automation_issue_projection
				BEFORE INSERT ON automation_work_items BEGIN
					SELECT RAISE(FAIL, 'injected issue projection failure');
				END`,
		},
		{
			name: "before assignment transition",
			triggerSQL: `CREATE TRIGGER fail_automation_issue_projection
				BEFORE INSERT ON automation_transitions
				WHEN NEW.event_key = 'github:example/runtime:issue:92:created:assignment' BEGIN
					SELECT RAISE(FAIL, 'injected issue projection failure');
				END`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var createCalls atomic.Int32
			provider := &fakeGitHubIssueRuntimeProvider{
				resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
					return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime"}, nil
				},
				createIssueFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
					createCalls.Add(1)
					return &GitHubIssue{Number: 92, URL: "https://github.com/example/runtime/issues/92", Title: req.Title, State: "open"}, nil
				},
			}
			fixture, newCausalContext, createIssue := newAutomationGitHubIssueDedupHarness(t, provider)
			firstCtx := newCausalContext("projection-failure-first")
			input := json.RawMessage(`{"title":"Projection repair issue","body":"body"}`)
			activityKey := githubIssueCreationActivityKey(firstCtx, &GitHubRepoRef{FullName: "example/runtime"},
				githubCreateIssueRuntimeInput{Title: "Projection repair issue", Body: "body"})
			_, err := fixture.repo.DB().Exec(tc.triggerSQL)
			require.NoError(t, err)

			_, firstErr := createIssue(firstCtx, input)
			require.ErrorContains(t, firstErr, "injected issue projection failure")
			_, err = fixture.repo.DB().Exec(`DROP TRIGGER fail_automation_issue_projection`)
			require.NoError(t, err)
			fingerprint := githubIssueTitleFingerprint("Projection repair issue")
			var mutationState string
			var recordedNumber sql.NullInt64
			require.NoError(t, fixture.repo.DB().QueryRow(`SELECT mutation_state, created_issue_number FROM automation_github_issue_dedup_leases
				WHERE project_id = ? AND repository_full_name = ? AND title_fingerprint = ?`, fixture.project.ID, "example/runtime", fingerprint).
				Scan(&mutationState, &recordedNumber))
			require.Equal(t, "completed", mutationState)
			require.Equal(t, int64(92), recordedNumber.Int64)

			sameOutput, sameErr := createIssue(firstCtx, input)
			require.NoError(t, sameErr)
			require.Contains(t, sameOutput, `"Number":92`)
			require.Contains(t, sameOutput, `"reused":true`)
			laterCtx := newCausalContext("projection-failure-later")
			laterActivityKey := githubIssueCreationActivityKey(laterCtx, &GitHubRepoRef{FullName: "example/runtime"},
				githubCreateIssueRuntimeInput{Title: "Projection repair issue", Body: "body"})
			laterAutomationContext, ok := AutomationContextFromContext(laterCtx)
			require.True(t, ok)
			for _, binding := range laterAutomationContext.Bindings {
				issueNode, nodeErr := fixture.repo.GetConnectedNodeByRole(context.Background(), fixture.project.ID,
					binding.AutomationID, binding.VersionID, binding.NodeID, "create_github_issue", true)
				require.NoError(t, nodeErr)
				require.NotNil(t, issueNode)
				binding.NodeID = issueNode.ID
				resourceID, reserveErr := fixture.repo.ReserveExternalActivity(context.Background(), fixture.project.ID, binding,
					laterActivityKey, "create_github_issue", "github_issue")
				require.NoError(t, reserveErr)
				require.Empty(t, resourceID)
			}
			laterOutput, laterErr := createIssue(laterCtx, input)
			require.NoError(t, laterErr)
			require.Contains(t, laterOutput, `"Number":92`)
			require.Contains(t, laterOutput, `"reused":true`)
			assertAutomationGitHubIssueProjection(t, fixture, 92, activityKey)
			require.Equal(t, int32(1), createCalls.Load(), "projection repair must not mutate GitHub again")
		})
	}
}

func TestAutomationGitHubIssueCreationDoesNotReadExistingIssuesForDeduplication(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterGitHubSDLC)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(fixture.repo.DB())
	fixture.project.RepoURL = "https://github.com/example/runtime.git"
	require.NoError(t, projectRepo.Update(ctx, &fixture.project))
	bugFinder := automationNodeByKey(t, fixture.definition, "bug_finder")
	invocation, _, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Now().UTC(), fixture.schedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	execution := models.Execution{TaskID: fixture.task.ID, Status: models.ExecRunning, PromptSent: "safe issue creation"}
	require.NoError(t, repository.NewExecutionRepo(fixture.repo.DB()).Create(ctx, &execution))
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID,
		InvocationID: invocation.ID, NodeID: bugFinder.ID}
	ctx = WithAutomationContext(ctx, models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}})
	ctx = withAutomationExecution(ctx, fixture.task.ID, execution.ID)

	var lookupCalls atomic.Int32
	var createCalls atomic.Int32
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime"}, nil
		},
		getIssueFn: func(context.Context, *GitHubRepoRef, int) (*GitHubIssue, error) {
			lookupCalls.Add(1)
			return nil, errors.New("existing issue read is forbidden during issue creation")
		},
		listMyIssuesFn: func(context.Context, *GitHubRepoRef) (*GitHubAuthenticatedUser, []GitHubIssue, error) {
			lookupCalls.Add(1)
			return nil, nil, errors.New("existing issue list is forbidden during issue creation")
		},
		listAssignedIssuesFn: func(context.Context, *GitHubRepoRef, string) ([]GitHubIssue, error) {
			lookupCalls.Add(1)
			return nil, errors.New("assigned issue list is forbidden during issue creation")
		},
		createIssueFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
			createCalls.Add(1)
			return &GitHubIssue{Number: 88, URL: "https://github.com/example/runtime/issues/88", Title: req.Title, State: "open"}, nil
		},
	}
	handlers := buildGitHubIssueRuntimeHandlers(githubIssueRuntimeOptions{ProjectID: fixture.project.ID, ProjectRepo: projectRepo,
		TaskRepo: fixture.taskRepo, AutomationRepo: fixture.repo, GitHub: provider})

	output, err := handlers["github_create_issue"](ctx, json.RawMessage(`{"title":"Safe duplicate boundary","body":"body"}`))
	require.NoError(t, err)
	require.Contains(t, output, `"Number":88`)
	require.Zero(t, lookupCalls.Load(), "Automation duplicate protection must not read existing GitHub issues")
	require.Equal(t, int32(1), createCalls.Load())
}

func TestAutomationGitHubIssueCreationSerializesLocalDedupAcrossExecutions(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterGitHubSDLC)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(fixture.repo.DB())
	fixture.project.RepoURL = "https://github.com/example/runtime.git"
	fixture.project.RepoPath = t.TempDir()
	require.NoError(t, projectRepo.Update(ctx, &fixture.project))
	bugFinder := automationNodeByKey(t, fixture.definition, "bug_finder")

	newCausalContext := func(occurrence string) context.Context {
		t.Helper()
		var invocationID string
		require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `INSERT INTO automation_invocations
			(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status, started_at)
			VALUES (?, ?, ?, ?, 'schedule', ?, ?, 'running', CURRENT_TIMESTAMP) RETURNING id`, fixture.project.ID,
			fixture.definition.Automation.ID, fixture.definition.Version.ID, bugFinder.ID, fixture.schedule.ID, occurrence).Scan(&invocationID))
		execution := models.Execution{TaskID: fixture.task.ID, Status: models.ExecRunning, PromptSent: occurrence}
		require.NoError(t, repository.NewExecutionRepo(fixture.repo.DB()).Create(ctx, &execution))
		binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID,
			InvocationID: invocationID, NodeID: bugFinder.ID}
		value := WithAutomationContext(ctx, models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}})
		return withAutomationExecution(value, fixture.task.ID, execution.ID)
	}
	firstCtx := newCausalContext("concurrent-first")
	secondCtx := newCausalContext("concurrent-second")

	createStarted := make(chan struct{})
	releaseCreate := make(chan struct{})
	var createCalls atomic.Int32
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime"}, nil
		},
		createIssueFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
			if createCalls.Add(1) == 1 {
				close(createStarted)
			}
			<-releaseCreate
			return &GitHubIssue{Number: 88, URL: "https://github.com/example/runtime/issues/88", Title: req.Title, State: "open"}, nil
		},
	}
	handlers := buildGitHubIssueRuntimeHandlers(githubIssueRuntimeOptions{ProjectID: fixture.project.ID, ProjectRepo: projectRepo,
		TaskRepo: fixture.taskRepo, AutomationRepo: fixture.repo, GitHub: provider})
	input := json.RawMessage(`{"title":"Concurrent duplicate","body":"first body"}`)
	type result struct {
		output string
		err    error
	}
	firstResult := make(chan result, 1)
	go func() {
		output, err := handlers["github_create_issue"](firstCtx, input)
		firstResult <- result{output: output, err: err}
	}()
	select {
	case <-createStarted:
	case <-time.After(time.Second):
		t.Fatal("first execution did not enter GitHub issue creation")
	}

	secondOutput, secondErr := handlers["github_create_issue"](secondCtx, json.RawMessage(`{"title":"  concurrent   duplicate  ","body":"second body"}`))
	require.ErrorContains(t, secondErr, "already checking or creating")
	require.Empty(t, secondOutput)
	close(releaseCreate)
	first := <-firstResult
	require.NoError(t, first.err)
	require.Contains(t, first.output, `"Number":88`)
	require.Equal(t, int32(1), createCalls.Load(), "concurrent Automation runs must create one canonical issue")

	duplicateOutput, duplicateErr := handlers["github_create_issue"](secondCtx, json.RawMessage(`{"title":"CONCURRENT DUPLICATE","body":"retry body"}`))
	require.NoError(t, duplicateErr)
	require.Contains(t, duplicateOutput, `"Number":88`)
	require.Contains(t, duplicateOutput, `"URL":"https://github.com/example/runtime/issues/88"`)
	require.Contains(t, duplicateOutput, `"reused":true`)
	require.Equal(t, int32(1), createCalls.Load(), "local deduplication must reuse later equivalent runs without reading GitHub")
}

func TestAutomationObservabilityRecordsSafeLifecycleAndGraphMetrics(t *testing.T) {
	automationobs.ResetForTest()
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	registration := NewAutomationRegistrationService(fixture.repo, NewAutomationAdapterRegistry())
	_, _, err := registration.Register(ctx, AutomationRegistrationRequest{ProjectID: fixture.project.ID, AdapterKey: "unsupported", StableKey: "invalid", Resources: []models.AutomationResourceBinding{{}}})
	require.Error(t, err)

	now := time.Now().UTC()
	next := fixture.schedule.ComputeNextRun(now)
	invocation, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, now, next)
	require.NoError(t, err)
	require.NotNil(t, invocation)
	require.NotNil(t, dispatch)

	producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: invocation.ID, NodeID: producer.ID}
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "observability:item", ActivityKey: "observability:activity", ActivityType: "test", ActivityStatus: models.AutomationActivityRunning,
		EventKey: "observability:bad-transition", ToNodeID: "missing-node", Transition: models.AutomationTransitionEntered,
	})
	require.Error(t, err)

	graph, err := NewAutomationGraphService(fixture.repo).GetLive(ctx, fixture.project.ID, fixture.definition.Automation.ID, now)
	require.NoError(t, err)
	require.NotNil(t, graph)

	drafts := NewAutomationDraftService(fixture.repo, NewAutomationAdapterRegistry())
	blank, err := drafts.BlankCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	planner := NewAutomationSaveValidator(NewAutomationAdapterRegistry(), drafts)
	compiler := NewAutomationCompiler(fixture.repo, NewTaskService(fixture.taskRepo, repository.NewAttachmentRepo(fixture.repo.DB()), nil), fixture.taskRepo, fixture.schedRepo, planner)
	plan, _, err := compiler.PreviewSave(ctx, fixture.project.ID, blank)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Validation)

	metrics := automationobs.Snapshot()
	for _, name := range []string{
		"automation.registration.validation_failure",
		"automation.invocation.created",
		"automation.transition.append_failure",
		"automation.save.validation_failure",
		"automation.graph.query_duration_ms",
		"automation.graph.payload_bytes",
	} {
		require.Greater(t, metrics[name].Count, uint64(0), "missing local metric %s", name)
	}
	require.Greater(t, metrics["automation.graph.payload_bytes"].Max, int64(0))
}

func TestAutomationLiveDisplayStatePrecedencePreservesMixedCounters(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	approval := automationNodeByKey(t, fixture.definition, "approval")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, NodeID: approval.ID}
	cases := []struct {
		key        string
		transition models.AutomationTransitionState
		activity   models.AutomationActivityStatus
	}{
		{key: "running", transition: models.AutomationTransitionEntered, activity: models.AutomationActivityRunning},
		{key: "position-only-running", transition: models.AutomationTransitionEntered, activity: models.AutomationActivityCompleted},
		{key: "waiting", transition: models.AutomationTransitionWaiting, activity: models.AutomationActivityRunning},
		{key: "blocked", transition: models.AutomationTransitionBlocked, activity: models.AutomationActivityCompleted},
		{key: "failed", transition: models.AutomationTransitionFailed, activity: models.AutomationActivityFailed},
		{key: "completed", transition: models.AutomationTransitionCompleted, activity: models.AutomationActivityCompleted},
	}
	for _, tc := range cases {
		_, _, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			WorkItemKey: "mixed:" + tc.key, ActivityKey: "mixed:" + tc.key, ActivityType: "test", ActivityStatus: tc.activity,
			EventKey: "mixed:" + tc.key, ToNodeID: approval.ID, Transition: tc.transition,
		})
		require.NoError(t, err)
	}
	invocation, _, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Now().UTC(), fixture.schedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	invocationBinding := binding
	invocationBinding.InvocationID = invocation.ID
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{invocationBinding}}, Binding: invocationBinding,
		ActivityKey: "mixed:invocation-only-failure", ActivityType: "test", ActivityStatus: models.AutomationActivityFailed,
	})
	require.NoError(t, err)
	graph, err := NewAutomationGraphService(fixture.repo).GetLive(ctx, fixture.project.ID, fixture.definition.Automation.ID, time.Now())
	require.NoError(t, err)
	for _, node := range graph.Nodes {
		if node.ID != approval.ID {
			continue
		}
		require.Equal(t, 2, node.Counts.Running, "a waiting work item must not also remain counted as running activity")
		require.Equal(t, 1, node.Counts.Waiting, "one waiting work item must have exactly one precedence-selected state")
		require.Equal(t, 1, node.Counts.Blocked)
		require.Equal(t, 2, node.Counts.Failed, "one failed work item must count once while an invocation-only failure remains visible")
		require.Equal(t, 1, node.Counts.CompletedRecently, "active, waiting, blocked, or failed work must not also appear recently completed")
		require.Equal(t, "failed", node.DisplayState)
	}
	cards, err := NewAutomationGraphService(fixture.repo).List(ctx, fixture.project.ID)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	require.Equal(t, 2, cards[0].Counts.Running)
	require.Equal(t, 1, cards[0].Counts.Waiting)
	require.Equal(t, 1, cards[0].Counts.Blocked)
	require.Equal(t, 2, cards[0].Counts.Failed, "portfolio failures must retain Live provenance deduplication")
	require.Equal(t, 1, cards[0].Counts.CompletedRecently, "portfolio counters must choose one state per work-item identity")
}

type fakeAutomationPullRequestProvider struct {
	calls        int
	resolveCalls int
	resolvedURL  string
	resolvedPath string
	pull         GitHubPullRequest
	err          error
}

func (f *fakeAutomationPullRequestProvider) ResolveRepo(_ context.Context, repoURL, repoPath string) (*GitHubRepoRef, error) {
	f.resolveCalls++
	f.resolvedURL = repoURL
	f.resolvedPath = repoPath
	return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime"}, nil
}

func (f *fakeAutomationPullRequestProvider) GetPullRequest(context.Context, *GitHubRepoRef, int) (*GitHubPullRequest, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	pull := f.pull
	return &pull, nil
}

func TestAutomationExternalPullRequestRefreshIsExplicitCachedAndReconcilesProjection(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterGitHubSDLC)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(fixture.repo.DB())
	fixture.project.RepoURL = "https://github.com/example/runtime"
	require.NoError(t, projectRepo.Update(ctx, &fixture.project))

	openPR := automationNodeByKey(t, fixture.definition, "open_pr")
	review := automationNodeByKey(t, fixture.definition, "review")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, NodeID: openPR.ID}
	contextValue := models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}
	_, _, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: contextValue, Binding: binding, WorkItemKey: "github:example/runtime:issue:42",
		ActivityKey: "github:example/runtime:pull:7:open", ActivityType: "open_pull_request", ActivityStatus: models.AutomationActivityCompleted,
		Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: fixture.task.ID}, {ResourceType: "pull_request", ResourceID: "github:example/runtime:pull:7"}},
		EventKey:  "github:example/runtime:pull:7:review", FromNodeID: openPR.ID, ToNodeID: review.ID, Transition: models.AutomationTransitionWaiting,
	})
	require.NoError(t, err)

	pullRequests := repository.NewTaskPullRequestRepo(fixture.repo.DB())
	record := models.TaskPullRequest{TaskID: fixture.task.ID, PRNumber: 7, PRURL: "https://github.com/example/runtime/pull/7", PRState: "open"}
	require.NoError(t, pullRequests.Upsert(ctx, &record))
	now := time.Now().UTC().Truncate(time.Second)
	_, err = fixture.repo.DB().ExecContext(ctx, `UPDATE task_pull_requests SET updated_at = datetime(?) WHERE id = ?`, now.Add(-time.Hour).Format("2006-01-02 15:04:05"), record.ID)
	require.NoError(t, err)

	provider := &fakeAutomationPullRequestProvider{pull: GitHubPullRequest{Number: 7, URL: record.PRURL, State: "closed", Merged: true}}
	external := NewAutomationExternalStateService(fixture.repo, pullRequests, projectRepo, provider)
	visionTrigger := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	_, err = fixture.repo.DB().ExecContext(ctx, `INSERT INTO automation_invocations
		(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status, started_at, completed_at)
		VALUES (?, ?, ?, ?, 'schedule', ?, 'external-health', 'completed', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID, visionTrigger.ID, fixture.schedule.ID)
	require.NoError(t, err)
	health, err := fixture.repo.RecomputeAutomationHealth(ctx, fixture.project.ID, fixture.definition.Automation.ID, now)
	require.NoError(t, err)
	require.Equal(t, models.AutomationHealthDegraded, health.State)
	require.Contains(t, health.Reason, "stale")
	graph, err := NewAutomationGraphService(fixture.repo).GetLive(ctx, fixture.project.ID, fixture.definition.Automation.ID, now)
	require.NoError(t, err)
	require.Equal(t, 0, provider.calls, "ordinary graph reads must never call GitHub")
	require.Equal(t, 1, graph.ExternalState.TrackedResources)
	require.True(t, graph.ExternalState.Stale)

	state, err := external.Refresh(ctx, fixture.project.ID, fixture.definition.Automation.ID, now)
	require.NoError(t, err)
	require.Equal(t, fixture.project.RepoURL, provider.resolvedURL)
	require.Empty(t, provider.resolvedPath, "Automation external refresh must not allow Git remote inference")
	require.Equal(t, 1, provider.calls)
	require.False(t, state.Stale)
	stored, err := pullRequests.GetByTaskID(ctx, fixture.task.ID)
	require.NoError(t, err)
	require.Equal(t, "merged", stored.PRState)
	var storedHealth, storedHealthReason string
	require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `SELECT health_state, health_reason FROM automations WHERE id = ?`, fixture.definition.Automation.ID).Scan(&storedHealth, &storedHealthReason))
	require.Equal(t, string(models.AutomationHealthHealthy), storedHealth, "successful refresh must persistently clear stale external degradation")
	var lifecycle string
	require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `SELECT lifecycle_state FROM automations WHERE id = ?`, fixture.definition.Automation.ID).Scan(&lifecycle))
	require.Equal(t, "active", lifecycle, "external health evaluation must never change lifecycle")
	var completed int
	require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_work_items WHERE automation_id = ? AND status = 'completed'`, fixture.definition.Automation.ID).Scan(&completed))
	require.Equal(t, 1, completed, "merged PR state must advance the persisted Automation projection")

	state, err = external.Refresh(ctx, fixture.project.ID, fixture.definition.Automation.ID, now.Add(30*time.Second))
	require.NoError(t, err)
	require.Equal(t, 1, provider.calls, "a fresh explicit result must be served from the persisted cache")
	require.False(t, state.Stale)

	_, err = fixture.repo.DB().ExecContext(ctx, `UPDATE task_pull_requests SET updated_at = datetime(?) WHERE id = ?`, now.Add(-time.Hour).Format("2006-01-02 15:04:05"), record.ID)
	require.NoError(t, err)
	provider.err = errors.New("github API request failed (429): rate limit exceeded")
	_, err = external.Refresh(ctx, fixture.project.ID, fixture.definition.Automation.ID, now)
	require.ErrorContains(t, err, "429")
	require.Equal(t, 2, provider.calls, "provider rate-limit failures must not be retried")
	graph, err = NewAutomationGraphService(fixture.repo).GetLive(ctx, fixture.project.ID, fixture.definition.Automation.ID, now)
	require.NoError(t, err)
	require.True(t, graph.ExternalState.Stale, "a failed external refresh must retain stale persisted freshness")

	fixture.project.RepoURL = ""
	require.NoError(t, projectRepo.Update(ctx, &fixture.project))
	resolveCalls := provider.resolveCalls
	_, err = external.Refresh(ctx, fixture.project.ID, fixture.definition.Automation.ID, now)
	require.ErrorContains(t, err, "explicit repository URL")
	require.Equal(t, resolveCalls, provider.resolveCalls, "missing repo_url must fail before provider resolution can infer a Git remote")
}

func TestAutomationRuntimeNativeAlertLifecycleAndLiveProjection(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	alertRepo := repository.NewAlertRepo(fixture.repo.DB())
	broadcaster := events.NewBroadcaster()
	fixture.repo.SetBroadcaster(broadcaster)
	sub, err := broadcaster.Subscribe()
	require.NoError(t, err)
	defer broadcaster.Unsubscribe(sub)
	alertRepo.SetAutomationRepo(fixture.repo)
	alertSvc := NewAlertService(alertRepo, nil)
	producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, NodeID: producer.ID}
	producerExecution := models.Execution{TaskID: fixture.task.ID, Status: models.ExecRunning, PromptSent: "produce exact suggestion"}
	require.NoError(t, repository.NewExecutionRepo(fixture.repo.DB()).Create(ctx, &producerExecution))
	ctx = WithAutomationContext(ctx, models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}})
	ctx = withAutomationExecution(ctx, fixture.task.ID, producerExecution.ID)
	alert, err := alertSvc.CreateActionable(ctx, &models.Alert{ProjectID: fixture.project.ID, SourceTaskID: &fixture.task.ID, Type: "product_suggestion", Title: "Follow me", Body: "private body", IdempotencyKey: "follow-me"})
	require.NoError(t, err)
	require.NotNil(t, alert.ExecutionID)
	require.Equal(t, producerExecution.ID, *alert.ExecutionID)
	var producerExecutionLinks int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activity_resources
		WHERE resource_type = 'execution' AND resource_id = ?`, producerExecution.ID).Scan(&producerExecutionLinks))
	require.Equal(t, 1, producerExecutionLinks)
	select {
	case event := <-sub:
		require.Equal(t, events.AutomationWorkItemUpdated, event.Type)
		require.Equal(t, fixture.project.ID, event.ProjectID)
		require.Equal(t, fixture.definition.Automation.ID, event.AutomationID)
		require.Equal(t, fixture.definition.Version.ID, event.VersionID)
		require.NotEmpty(t, event.WorkItemID)
		require.NotEmpty(t, event.NodeID)
	case <-time.After(time.Second):
		t.Fatal("expected compact automation invalidation after alert projection commit")
	}
	require.NoError(t, alertSvc.SetDecision(ctx, fixture.project.ID, alert.ID, models.AlertDecisionApproved))
	_, err = alertSvc.ClaimApproved(ctx, fixture.project.ID, alert.ID, "first-inbox", time.Minute)
	require.NoError(t, err)
	require.NoError(t, alertSvc.ReleaseClaim(ctx, fixture.project.ID, alert.ID, "first-inbox"))
	var runningClaims int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activities
		WHERE automation_id = ? AND activity_type = 'claim_notification' AND status = 'running'`, fixture.definition.Automation.ID).Scan(&runningClaims))
	require.Zero(t, runningClaims, "releasing an Alert lease must not leave a running Automation claim")
	_, err = alertSvc.ClaimApproved(ctx, fixture.project.ID, alert.ID, "inbox-task", time.Minute)
	require.NoError(t, err)
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activities
		WHERE automation_id = ? AND activity_type = 'claim_notification' AND status = 'running'`, fixture.definition.Automation.ID).Scan(&runningClaims))
	require.Equal(t, 1, runningClaims, "reclaim must leave exactly one current Automation claim")
	implementation, err := alertSvc.CreateImplementationTask(ctx, fixture.project.ID, alert.ID, "inbox-task", models.AlertImplementationTaskInput{Title: "Implement", Prompt: "Implement safely", Priority: 2})
	require.NoError(t, err)
	require.NoError(t, alertSvc.MarkProcessing(ctx, fixture.project.ID, alert.ID, "inbox-task", models.AlertProcessingCompleted, ""))
	var completedClaims int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activities
		WHERE automation_id = ? AND activity_type = 'claim_notification' AND status = 'completed'`, fixture.definition.Automation.ID).Scan(&completedClaims))
	require.Equal(t, 1, completedClaims, "terminal processing must complete the current inbox claim")
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activities
		WHERE automation_id = ? AND activity_type = 'claim_notification' AND status = 'running'`, fixture.definition.Automation.ID).Scan(&runningClaims))
	require.Zero(t, runningClaims, "terminal processing must not leave an inbox claim running forever")

	var workItems, transitions int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_work_items WHERE automation_id = ? AND work_item_key = ?`, fixture.definition.Automation.ID, "alert:"+alert.ID).Scan(&workItems))
	require.Equal(t, 1, workItems)
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_transitions WHERE automation_id = ?`, fixture.definition.Automation.ID).Scan(&transitions))
	require.GreaterOrEqual(t, transitions, 4)
	for _, edgeKey := range []string{"vision_to_notification", "notification_to_approval", "approval_to_inbox", "inbox_to_implementation"} {
		var edgeTransitions int
		require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_transitions tr
			JOIN automation_edges e ON e.id = tr.edge_id WHERE tr.automation_id = ? AND e.edge_key = ?`,
			fixture.definition.Automation.ID, edgeKey).Scan(&edgeTransitions))
		require.Equal(t, 1, edgeTransitions, edgeKey+" must be represented by an exact persisted transition")
	}
	contextForTask, err := fixture.repo.ContextForTask(ctx, fixture.project.ID, implementation.ID)
	require.NoError(t, err)
	require.Len(t, contextForTask.Bindings, 1)

	graph, err := NewAutomationGraphService(fixture.repo).GetLive(ctx, fixture.project.ID, fixture.definition.Automation.ID, time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, graph.ActiveWorkItems, "inbox processing completion must not masquerade as implementation completion")
	implementationExecution := models.Execution{TaskID: implementation.ID, Status: models.ExecRunning, PromptSent: "private implementation prompt"}
	execRepo := repository.NewExecutionRepo(fixture.repo.DB())
	execRepo.SetAutomationRepo(fixture.repo)
	require.NoError(t, execRepo.Create(ctx, &implementationExecution))
	implementationBinding := contextForTask.Bindings[0]
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: contextForTask, Binding: implementationBinding,
		ActivityKey: "execution:" + implementationExecution.ID + ":run", ActivityType: "task_execution", ActivityStatus: models.AutomationActivityRunning,
		Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: implementation.ID}, {ResourceType: "execution", ResourceID: implementationExecution.ID}},
	})
	require.NoError(t, err)
	require.NoError(t, execRepo.Complete(ctx, implementationExecution.ID, models.ExecCompleted, "ok", "", 1, 1))
	graph, err = NewAutomationGraphService(fixture.repo).GetLive(ctx, fixture.project.ID, fixture.definition.Automation.ID, time.Now())
	require.NoError(t, err)
	require.Zero(t, graph.ActiveWorkItems)
	var completedEdge models.AutomationLiveEdge
	for _, edge := range graph.Edges {
		if edge.EdgeKey == "implementation_to_completed" {
			completedEdge = edge
		}
	}
	require.Equal(t, 1, completedEdge.TransitionCount)
	require.True(t, completedEdge.Highlighted)
	for _, node := range graph.Nodes {
		if node.NodeKey == "completed" {
			require.Equal(t, "recently_completed", node.DisplayState)
		}
	}
	encoded, err := json.Marshal(graph)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private body")
	require.NotContains(t, string(encoded), "Implement safely")
}

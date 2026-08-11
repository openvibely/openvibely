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
	schedule.ClearContextOnStart = true
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

func TestAutomationManualRunUsesExistingDispatchWithoutChangingSchedule(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	before, err := fixture.schedRepo.GetByID(ctx, fixture.schedule.ID)
	require.NoError(t, err)

	invocations, dispatches, err := fixture.repo.ClaimManualAutomationRun(ctx, fixture.project.ID, fixture.definition.Automation.ID, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, invocations, 1)
	require.Len(t, dispatches, 1)
	require.Equal(t, "manual", invocations[0].TriggerResourceType)
	require.Equal(t, fixture.schedule.ID, invocations[0].TriggerResourceID)
	require.Nil(t, invocations[0].ScheduledFor)
	require.Equal(t, fixture.task.ID, dispatches[0].TaskID)

	liveAfterClaim, err := NewAutomationGraphService(fixture.repo).GetLive(ctx, fixture.project.ID, fixture.definition.Automation.ID, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, liveAfterClaim)
	triggerNode := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	var triggerLiveNode *models.AutomationLiveNode
	for i := range liveAfterClaim.Nodes {
		if liveAfterClaim.Nodes[i].ID == triggerNode.ID {
			triggerLiveNode = &liveAfterClaim.Nodes[i]
			break
		}
	}
	require.NotNil(t, triggerLiveNode)
	require.Equal(t, "running", triggerLiveNode.DisplayState, "Run now must immediately project its claimed invocation onto the Live canvas")
	require.Equal(t, 1, triggerLiveNode.Counts.Running)

	after, err := fixture.schedRepo.GetByID(ctx, fixture.schedule.ID)
	require.NoError(t, err)
	require.Equal(t, before.RunAt, after.RunAt)
	require.Equal(t, before.LastRun, after.LastRun)
	require.Equal(t, before.NextRun, after.NextRun)
	require.Equal(t, before.RepeatType, after.RepeatType)
	require.Equal(t, before.RepeatInterval, after.RepeatInterval)
	require.Equal(t, before.Enabled, after.Enabled)

	leased, err := fixture.repo.LeaseNextDispatch(ctx, "manual-run-test", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.Equal(t, dispatches[0].ID, leased.ID)
	execution, err := fixture.taskRepo.ClaimAutomationDispatch(ctx, leased.ID, "manual-run-test")
	require.NoError(t, err)
	require.Equal(t, fixture.task.Prompt, execution.PromptSent)
	require.True(t, execution.StartsNewContext, "manual dispatch must preserve the owned Schedule's clear-context setting")
	liveAfterActivity, err := NewAutomationGraphService(fixture.repo).GetLive(ctx, fixture.project.ID, fixture.definition.Automation.ID, time.Now().UTC())
	require.NoError(t, err)
	activityNodeFound := false
	for _, node := range liveAfterActivity.Nodes {
		if node.ID == triggerNode.ID {
			activityNodeFound = true
			require.Equal(t, 1, node.Counts.Running, "the invocation fallback must yield to its recorded activity without double counting")
		}
	}
	require.True(t, activityNodeFound)
	require.NoError(t, fixture.repo.MarkDispatchSubmitted(ctx, leased.ID, "manual-run-test", execution.ID))
	_, err = fixture.repo.DB().Exec(`UPDATE executions SET status = 'completed', completed_at = CURRENT_TIMESTAMP WHERE id = ?`, execution.ID)
	require.NoError(t, err)
	_, err = fixture.repo.DB().Exec(`UPDATE tasks SET status = 'completed' WHERE id = ?`, fixture.task.ID)
	require.NoError(t, err)
	require.NoError(t, fixture.repo.CompleteDispatch(ctx, leased.ID, execution.ID, models.ExecCompleted, ""))

	dueSchedule, err := fixture.schedRepo.GetByID(ctx, fixture.schedule.ID)
	require.NoError(t, err)
	scheduledInvocation, scheduledDispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, *dueSchedule, time.Now().UTC(), dueSchedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	require.Equal(t, "schedule", scheduledInvocation.TriggerResourceType)
	require.NotNil(t, scheduledDispatch, "the genuine due occurrence must still dispatch after a manual run")
}

func TestAutomationManualRunQueuesEveryScheduleEntryAndRejectsTamperedBinding(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	secondTask := models.Task{ProjectID: fixture.project.ID, Title: "Second manual entry", Category: models.CategoryScheduled, Priority: 3, Status: models.StatusPending, Prompt: "second persisted prompt"}
	require.NoError(t, fixture.taskRepo.Create(ctx, &secondTask))
	secondRunAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	secondNextRun := secondRunAt.Add(time.Hour)
	secondSchedule := models.Schedule{TaskID: secondTask.ID, RunAt: secondRunAt, NextRun: &secondNextRun, RepeatType: models.RepeatHours, RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}
	require.NoError(t, fixture.schedRepo.Create(ctx, &secondSchedule))
	var secondNodeID string
	require.NoError(t, fixture.repo.DB().QueryRow(`INSERT INTO automation_nodes
		(project_id, automation_id, version_id, node_key, name, node_type, role)
		VALUES (?, ?, ?, 'second_manual_entry', 'Second manual entry', 'agent_task', 'task') RETURNING id`,
		fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID).Scan(&secondNodeID))
	for _, resource := range []struct{ kind, id string }{{"task", secondTask.ID}, {"schedule", secondSchedule.ID}} {
		_, err := fixture.repo.DB().Exec(`INSERT INTO automation_definition_resources
			(project_id, automation_id, version_id, node_id, resource_type, resource_id, relation)
			VALUES (?, ?, ?, ?, ?, ?, 'owned')`, fixture.project.ID, fixture.definition.Automation.ID,
			fixture.definition.Version.ID, secondNodeID, resource.kind, resource.id)
		require.NoError(t, err)
	}
	_, err := fixture.repo.DB().Exec(`INSERT INTO automation_trigger_owners
		(schedule_id, project_id, automation_id, version_id, node_id, ownership_state)
		VALUES (?, ?, ?, ?, ?, 'active')`, secondSchedule.ID, fixture.project.ID, fixture.definition.Automation.ID,
		fixture.definition.Version.ID, secondNodeID)
	require.NoError(t, err)

	invocations, dispatches, err := fixture.repo.ClaimManualAutomationRun(ctx, fixture.project.ID, fixture.definition.Automation.ID, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, invocations, 2)
	require.Len(t, dispatches, 2)
	dispatchedTasks := []string{dispatches[0].TaskID, dispatches[1].TaskID}
	require.ElementsMatch(t, []string{fixture.task.ID, secondTask.ID}, dispatchedTasks)
	storedSecond, err := fixture.schedRepo.GetByID(ctx, secondSchedule.ID)
	require.NoError(t, err)
	require.Equal(t, secondRunAt, storedSecond.RunAt)
	require.Equal(t, &secondNextRun, storedSecond.NextRun)
	require.Nil(t, storedSecond.LastRun)

	tampered := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	_, err = tampered.repo.DB().Exec(`DELETE FROM automation_definition_resources
		WHERE version_id = ? AND resource_type = 'task' AND resource_id = ?`, tampered.definition.Version.ID, tampered.task.ID)
	require.NoError(t, err)
	invocations, dispatches, err = tampered.repo.ClaimManualAutomationRun(ctx, tampered.project.ID, tampered.definition.Automation.ID, time.Now().UTC())
	require.ErrorIs(t, err, repository.ErrAutomationNoScheduleEntries)
	require.Empty(t, invocations)
	require.Empty(t, dispatches)
	var invocationCount int
	require.NoError(t, tampered.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_invocations WHERE automation_id = ?`, tampered.definition.Automation.ID).Scan(&invocationCount))
	require.Zero(t, invocationCount)
}

func TestAutomationManualRunSkipsBusyEntryAndRejectsInactiveLifecycle(t *testing.T) {
	for _, status := range []models.TaskStatus{models.StatusQueued, models.StatusRunning} {
		t.Run(string(status), func(t *testing.T) {
			fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
			_, err := fixture.repo.DB().Exec(`UPDATE tasks SET status = ? WHERE id = ?`, status, fixture.task.ID)
			require.NoError(t, err)
			invocations, dispatches, err := fixture.repo.ClaimManualAutomationRun(context.Background(), fixture.project.ID, fixture.definition.Automation.ID, time.Now().UTC())
			require.NoError(t, err)
			require.Len(t, invocations, 1)
			require.Equal(t, models.AutomationInvocationSkipped, invocations[0].Status)
			require.Equal(t, "task_running", invocations[0].SkippedReason)
			require.Empty(t, dispatches)
		})
	}

	t.Run("reserved", func(t *testing.T) {
		fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
		_, firstDispatches, err := fixture.repo.ClaimManualAutomationRun(context.Background(), fixture.project.ID, fixture.definition.Automation.ID, time.Now().UTC())
		require.NoError(t, err)
		require.Len(t, firstDispatches, 1)
		invocations, dispatches, err := fixture.repo.ClaimManualAutomationRun(context.Background(), fixture.project.ID, fixture.definition.Automation.ID, time.Now().UTC())
		require.NoError(t, err)
		require.Len(t, invocations, 1)
		require.Equal(t, models.AutomationInvocationSkipped, invocations[0].Status)
		require.Equal(t, "task_reserved", invocations[0].SkippedReason)
		require.Empty(t, dispatches)
	})

	for _, state := range []models.AutomationLifecycleState{models.AutomationPaused, models.AutomationArchived} {
		t.Run(string(state), func(t *testing.T) {
			fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
			require.NoError(t, fixture.repo.SetAutomationLifecycle(context.Background(), fixture.project.ID, fixture.definition.Automation.ID, state))
			invocations, dispatches, err := fixture.repo.ClaimManualAutomationRun(context.Background(), fixture.project.ID, fixture.definition.Automation.ID, time.Now().UTC())
			require.ErrorIs(t, err, repository.ErrAutomationNotActive)
			require.Empty(t, invocations)
			require.Empty(t, dispatches)
			var count int
			require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_invocations WHERE automation_id = ?`, fixture.definition.Automation.ID).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestAutomationRuntimeAdvancesStaleTerminalScheduledOccurrence(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterGitHubSDLC)
	ctx := context.Background()

	due := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	firstClaimAt := due.Add(5 * time.Second)
	staleEditAt := due.Add(2*time.Hour + time.Minute)
	fixture.schedule.RunAt = due
	fixture.schedule.NextRun = &due
	fixture.schedule.LastRun = nil
	fixture.schedule.RepeatType = models.RepeatHours
	fixture.schedule.RepeatInterval = 1
	require.NoError(t, fixture.schedRepo.Update(ctx, &fixture.schedule))

	firstNext := fixture.schedule.ComputeNextRun(firstClaimAt)
	invocation, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, firstClaimAt, firstNext)
	require.NoError(t, err)
	require.NotNil(t, dispatch)
	require.Equal(t, models.AutomationInvocationClaimed, invocation.Status)

	leased, err := fixture.repo.LeaseNextDispatch(ctx, "stale-terminal-test", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, leased)
	require.Equal(t, dispatch.ID, leased.ID)
	execution, err := fixture.taskRepo.ClaimAutomationDispatch(ctx, dispatch.ID, "stale-terminal-test")
	require.NoError(t, err)
	require.NoError(t, fixture.repo.CompleteDispatch(ctx, dispatch.ID, execution.ID, models.ExecCompleted, ""))

	stale, err := fixture.schedRepo.GetByID(ctx, fixture.schedule.ID)
	require.NoError(t, err)
	stale.NextRun = &due
	require.NoError(t, fixture.schedRepo.Update(ctx, stale))

	repairNext := stale.ComputeNextRun(staleEditAt)
	repeatedInvocation, repeatedDispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, *stale, staleEditAt, repairNext)
	require.NoError(t, err)
	require.Equal(t, invocation.ID, repeatedInvocation.ID)
	require.Equal(t, dispatch.ID, repeatedDispatch.ID)

	repaired, err := fixture.schedRepo.GetByID(ctx, fixture.schedule.ID)
	require.NoError(t, err)
	require.NotNil(t, repaired.NextRun)
	require.True(t, repaired.NextRun.Equal(*repairNext), "stale completed occurrence must advance to the next future hourly run")
	require.NotNil(t, repaired.LastRun)
	require.True(t, repaired.LastRun.Equal(firstClaimAt.UTC()), "repair must not rewrite the original run marker")

	var invocationCount, dispatchCount int
	require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_invocations WHERE trigger_resource_id = ?`, fixture.schedule.ID).Scan(&invocationCount))
	require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_dispatch_outbox WHERE invocation_id = ?`, invocation.ID).Scan(&dispatchCount))
	require.Equal(t, 1, invocationCount)
	require.Equal(t, 1, dispatchCount)
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
	require.True(t, execution.StartsNewContext)
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
	preRestartRecovered, err := repository.NewExecutionRepo(fixture.repo.DB()).RecoverPreRestartRunningTaskExecutions(ctx)
	require.NoError(t, err)
	require.Zero(t, preRestartRecovered, "pre-restart recovery must preserve dispatch-reserved executions")
	preserved, err := repository.NewExecutionRepo(fixture.repo.DB()).GetByID(ctx, execution.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecRunning, preserved.Status)

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

func TestAutomationDeleteRejectsOrdinaryRunningExecution(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	binding := models.AutomationBinding{
		AutomationID: fixture.definition.Automation.ID,
		VersionID:    fixture.definition.Version.ID,
		NodeID:       producer.ID,
	}
	execution := models.Execution{TaskID: fixture.task.ID, Status: models.ExecRunning, PromptSent: "ordinary automation task"}
	execRepo := repository.NewExecutionRepo(fixture.repo.DB())
	require.NoError(t, execRepo.Create(ctx, &execution))
	require.Empty(t, execution.DispatchID, "ordinary task execution must not depend on scheduler dispatch ownership")
	require.NoError(t, fixture.taskRepo.UpdateStatus(ctx, fixture.task.ID, models.StatusRunning))
	_, activity, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context:        models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}},
		Binding:        binding,
		WorkItemKey:    "execution:" + execution.ID + ":root",
		WorkItemKind:   "task_execution",
		WorkItemTitle:  fixture.task.Title,
		ActivityKey:    "execution:" + execution.ID + ":run",
		ActivityType:   "task_execution",
		ActivityStatus: models.AutomationActivityRunning,
		EventKey:       "execution:" + execution.ID + ":entered",
		ToNodeID:       binding.NodeID,
		Transition:     models.AutomationTransitionEntered,
		Resources:      []models.AutomationActivityResource{{ResourceType: "execution", ResourceID: execution.ID}, {ResourceType: "task", ResourceID: fixture.task.ID}},
	})
	require.NoError(t, err)

	lifecycle := NewAutomationLifecycleService(fixture.repo, fixture.schedRepo)
	err = lifecycle.Delete(ctx, fixture.project.ID, fixture.definition.Automation.ID)
	require.ErrorIs(t, err, repository.ErrAutomationDispatchInFlight)
	definition, err := fixture.repo.GetDefinition(ctx, fixture.project.ID, fixture.definition.Automation.ID)
	require.NoError(t, err)
	require.NotNil(t, definition, "rejected deletion must preserve the Automation graph")
	var executionRows, activityRows, resourceRows int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM executions WHERE id = ? AND status = 'running'`, execution.ID).Scan(&executionRows))
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activities WHERE id = ?`, activity.ID).Scan(&activityRows))
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activity_resources WHERE activity_id = ? AND resource_type = 'execution' AND resource_id = ?`, activity.ID, execution.ID).Scan(&resourceRows))
	require.Equal(t, 1, executionRows)
	require.Equal(t, 1, activityRows)
	require.Equal(t, 1, resourceRows)

	require.NoError(t, execRepo.Complete(ctx, execution.ID, models.ExecCompleted, "ok", "", 1, 1))
	require.NoError(t, fixture.taskRepo.UpdateStatus(ctx, fixture.task.ID, models.StatusCompleted))
	require.NoError(t, lifecycle.Delete(ctx, fixture.project.ID, fixture.definition.Automation.ID), "terminal ordinary execution must not block deletion")
}

func TestAutomationDeleteAllowsOrphanedRunningInvocationAfterTaskDeletion(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	now := time.Now().UTC()
	invocation, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, now, fixture.schedule.ComputeNextRun(now))
	require.NoError(t, err)
	require.NotNil(t, dispatch)
	leased, err := fixture.repo.LeaseNextDispatch(ctx, "task-deleting-process", now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, dispatch.ID, leased.ID)
	_, err = fixture.taskRepo.ClaimAutomationDispatch(ctx, dispatch.ID, "task-deleting-process")
	require.NoError(t, err)

	require.NoError(t, fixture.taskRepo.Delete(ctx, fixture.task.ID), "ordinary task deletion reproduces the orphaned invocation in existing databases")
	var invocationStatus string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status FROM automation_invocations WHERE id = ?`, invocation.ID).Scan(&invocationStatus))
	require.Equal(t, "running", invocationStatus)
	var dispatchRows, executionRows int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_dispatch_outbox WHERE invocation_id = ?`, invocation.ID).Scan(&dispatchRows))
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM executions WHERE dispatch_id = ?`, dispatch.ID).Scan(&executionRows))
	require.Zero(t, dispatchRows)
	require.Zero(t, executionRows)

	lifecycle := NewAutomationLifecycleService(fixture.repo, fixture.schedRepo)
	require.NoError(t, lifecycle.Delete(ctx, fixture.project.ID, fixture.definition.Automation.ID),
		"a running invocation with no surviving dispatch or execution cannot own in-flight work")
	definition, err := fixture.repo.GetDefinition(ctx, fixture.project.ID, fixture.definition.Automation.ID)
	require.NoError(t, err)
	require.Nil(t, definition)
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

func TestAutomationRuntimeMonthlyMonthEndClaimPersistsNextRunSequence(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()

	anchor := time.Date(2026, time.January, 31, 10, 0, 0, 0, time.Local)
	fixture.schedule.RunAt = anchor.UTC()
	fixture.schedule.NextRun = &fixture.schedule.RunAt
	fixture.schedule.RepeatType = models.RepeatMonthly
	fixture.schedule.RepeatInterval = 1
	require.NoError(t, fixture.schedRepo.Update(ctx, &fixture.schedule))

	expected := []time.Time{
		time.Date(2026, time.February, 28, 10, 0, 0, 0, time.Local).UTC(),
		time.Date(2026, time.March, 31, 10, 0, 0, 0, time.Local).UTC(),
		time.Date(2026, time.April, 30, 10, 0, 0, 0, time.Local).UTC(),
		time.Date(2026, time.May, 31, 10, 0, 0, 0, time.Local).UTC(),
	}

	for i, want := range expected {
		stored, err := fixture.schedRepo.GetByID(ctx, fixture.schedule.ID)
		require.NoError(t, err)
		require.NotNil(t, stored.NextRun)
		due := *stored.NextRun
		nextRun := stored.ComputeNextRun(due)
		require.NotNil(t, nextRun)

		invocation, _, err := fixture.repo.ClaimScheduledOccurrence(ctx, *stored, due, nextRun)
		require.NoError(t, err, "claim %d", i)
		require.NotNil(t, invocation, "claim %d", i)
		require.NotNil(t, invocation.ScheduledFor, "claim %d", i)
		require.True(t, invocation.ScheduledFor.Equal(due), "claim %d scheduled_for = %v, want %v", i, invocation.ScheduledFor, due)

		persisted, err := fixture.schedRepo.GetByID(ctx, fixture.schedule.ID)
		require.NoError(t, err)
		require.NotNil(t, persisted.LastRun)
		require.True(t, persisted.LastRun.Equal(due), "claim %d last_run = %v, want %v", i, persisted.LastRun, due)
		require.NotNil(t, persisted.NextRun)
		require.True(t, persisted.NextRun.Equal(want), "claim %d next_run = %v, want %v", i, persisted.NextRun, want)
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

func TestAutomationRuntimePreparedDispatchWaitsPendingForGlobalCapacity(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	llmConfigRepo := repository.NewLLMConfigRepo(fixture.repo.DB())
	agent := models.LLMConfig{Name: "Automation capacity worker", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, &agent))
	require.NoError(t, fixture.repo.DB().QueryRow(`UPDATE tasks SET agent_id = ? WHERE id = ? RETURNING id`, agent.ID, fixture.task.ID).Scan(new(string)))

	projectRepo := repository.NewProjectRepo(fixture.repo.DB())
	execRepo := repository.NewExecutionRepo(fixture.repo.DB())
	execRepo.SetAutomationRepo(fixture.repo)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, fixture.taskRepo, projectRepo, fixture.schedRepo, repository.NewAttachmentRepo(fixture.repo.DB()))
	mockLLM := testutil.NewMockLLMCaller()
	mockLLM.Response = "automation capacity run completed"
	mockLLM.TextOnly = mockLLM.Response
	llmSvc.SetLLMCaller(mockLLM)
	llmSvc.SetAutomationRepo(fixture.repo)

	worker := NewWorkerService(llmSvc, 2, projectRepo)
	worker.SetTaskRepo(fixture.taskRepo)
	worker.SetLLMConfigRepo(llmConfigRepo)
	worker.SetExecutionRepo(execRepo)
	worker.SetAutomationRepo(fixture.repo)
	worker.Start(ctx)
	defer worker.Stop()

	otherProject := automationTestProject(t, projectRepo, "Existing capacity holder")
	require.True(t, worker.TryAcquireProjectSlot(otherProject.ID))
	require.True(t, worker.TryAcquireProjectSlot(otherProject.ID))
	defer func() {
		for worker.TotalRunning() > 0 {
			worker.ReleaseProjectSlot(otherProject.ID)
		}
	}()

	_, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Now().UTC(), fixture.schedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	require.NotNil(t, dispatch)
	dispatcher := NewAutomationDispatcher(fixture.repo, fixture.taskRepo, worker)
	dispatched, err := dispatcher.DispatchOne(ctx)
	require.NoError(t, err)
	require.True(t, dispatched)

	storedTask, err := fixture.taskRepo.GetByID(ctx, fixture.task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusPending, storedTask.Status, "capacity-waiting Automation work must remain visually queued")
	var executionCount int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM executions WHERE dispatch_id = ?`, dispatch.ID).Scan(&executionCount))
	require.Zero(t, executionCount, "capacity-waiting Automation work must not have a running execution")
	require.Equal(t, 2, worker.TotalRunning())

	worker.ReleaseProjectSlot(otherProject.ID)
	worker.ReleaseProjectSlot(otherProject.ID)
	worker.dispatchNext()
	require.Eventually(t, func() bool {
		var status string
		return fixture.repo.DB().QueryRow(`SELECT status FROM automation_dispatch_outbox WHERE id = ?`, dispatch.ID).Scan(&status) == nil && status == "completed"
	}, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, 1, mockLLM.CallCount())
}

func TestAutomationRuntimeCapacityQueuedDispatchUsesEditedTaskAndModelCapacity(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	llmConfigRepo := repository.NewLLMConfigRepo(fixture.repo.DB())
	originalAgent := models.LLMConfig{Name: "Original Automation worker", Provider: models.ProviderTest, Model: "original", IsDefault: true, MaxWorkers: 1}
	revisedAgent := models.LLMConfig{Name: "Revised Automation worker", Provider: models.ProviderTest, Model: "revised", MaxWorkers: 1}
	admittedAgent := models.LLMConfig{Name: "Atomically admitted Automation worker", Provider: models.ProviderTest, Model: "admitted", MaxWorkers: 1}
	require.NoError(t, llmConfigRepo.Create(ctx, &originalAgent))
	require.NoError(t, llmConfigRepo.Create(ctx, &revisedAgent))
	require.NoError(t, llmConfigRepo.Create(ctx, &admittedAgent))
	require.NoError(t, fixture.repo.DB().QueryRow(`UPDATE tasks SET agent_id = ? WHERE id = ? RETURNING id`, originalAgent.ID, fixture.task.ID).Scan(new(string)))

	projectRepo := repository.NewProjectRepo(fixture.repo.DB())
	execRepo := repository.NewExecutionRepo(fixture.repo.DB())
	execRepo.SetAutomationRepo(fixture.repo)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, fixture.taskRepo, projectRepo, fixture.schedRepo, repository.NewAttachmentRepo(fixture.repo.DB()))
	mockLLM := testutil.NewMockLLMCaller()
	mockLLM.Response = "edited automation run completed"
	mockLLM.TextOnly = mockLLM.Response
	providerStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	mockLLM.OnCall = func(context.Context, testutil.MockLLMCall) {
		close(providerStarted)
		<-releaseProvider
	}
	llmSvc.SetLLMCaller(mockLLM)
	llmSvc.SetAutomationRepo(fixture.repo)

	worker := NewWorkerService(llmSvc, 1, projectRepo)
	worker.SetTaskRepo(fixture.taskRepo)
	worker.SetLLMConfigRepo(llmConfigRepo)
	worker.SetExecutionRepo(execRepo)
	worker.SetAutomationRepo(fixture.repo)
	const admittedPrompt = "execute the atomically admitted Automation prompt"
	claimEditResult := make(chan error, 1)
	worker.beforeQueuedAutomationTaskClaim = func(models.Task) {
		_, err := fixture.repo.DB().Exec(`UPDATE tasks SET prompt = ?, agent_id = ? WHERE id = ? AND status = 'pending'`,
			admittedPrompt, admittedAgent.ID, fixture.task.ID)
		claimEditResult <- err
	}
	worker.Start(ctx)
	defer worker.Stop()

	capacityProject := automationTestProject(t, projectRepo, "Existing capacity holder for edited Automation")
	require.True(t, worker.TryAcquireProjectSlot(capacityProject.ID))
	capacityHeld := true
	defer func() {
		if capacityHeld {
			worker.ReleaseProjectSlot(capacityProject.ID)
		}
		select {
		case <-releaseProvider:
		default:
			close(releaseProvider)
		}
	}()

	_, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Now().UTC(), fixture.schedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	require.NotNil(t, dispatch)
	dispatcher := NewAutomationDispatcher(fixture.repo, fixture.taskRepo, worker)
	dispatched, err := dispatcher.DispatchOne(ctx)
	require.NoError(t, err)
	require.True(t, dispatched)

	const revisedPrompt = "execute the revised capacity-queued Automation prompt"
	require.NoError(t, fixture.repo.DB().QueryRow(`UPDATE tasks SET prompt = ?, agent_id = ? WHERE id = ? AND status = 'pending' RETURNING id`,
		revisedPrompt, revisedAgent.ID, fixture.task.ID).Scan(new(string)))

	worker.ReleaseProjectSlot(capacityProject.ID)
	capacityHeld = false
	worker.dispatchNext()
	select {
	case <-providerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("edited capacity-queued Automation did not reach provider")
	}
	select {
	case err := <-claimEditResult:
		require.NoError(t, err)
	default:
		t.Fatal("expected deterministic edit at queued Automation claim boundary")
	}

	require.Equal(t, admittedAgent.ID, mockLLM.LastCall().Agent.ID)
	require.Contains(t, mockLLM.LastCall().Prompt, admittedPrompt)
	require.Equal(t, 0, worker.ModelRunning(originalAgent.ID), "original stale model must not own capacity")
	require.Equal(t, 0, worker.ModelRunning(revisedAgent.ID), "refreshed model slot must transfer after the atomic claim")
	require.Equal(t, 1, worker.ModelRunning(admittedAgent.ID), "atomically admitted model must own capacity")

	var promptSent, executionAgentID string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT prompt_sent, COALESCE(agent_config_id, '') FROM executions WHERE dispatch_id = ?`, dispatch.ID).
		Scan(&promptSent, &executionAgentID))
	require.Equal(t, admittedPrompt, promptSent)
	require.Equal(t, admittedAgent.ID, executionAgentID)

	close(releaseProvider)
	require.Eventually(t, func() bool {
		var status string
		return fixture.repo.DB().QueryRow(`SELECT status FROM automation_dispatch_outbox WHERE id = ?`, dispatch.ID).Scan(&status) == nil && status == "completed"
	}, 5*time.Second, 20*time.Millisecond)
}

func TestAutomationRuntimeCancellationDuringAdmittedModelTransferTerminalizesDispatch(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	llmConfigRepo := repository.NewLLMConfigRepo(fixture.repo.DB())
	originalAgent := models.LLMConfig{Name: "Original transfer worker", Provider: models.ProviderTest, Model: "original", IsDefault: true, MaxWorkers: 1}
	blockedAgent := models.LLMConfig{Name: "Blocked admitted transfer worker", Provider: models.ProviderTest, Model: "blocked", MaxWorkers: 1}
	require.NoError(t, llmConfigRepo.Create(ctx, &originalAgent))
	require.NoError(t, llmConfigRepo.Create(ctx, &blockedAgent))
	require.NoError(t, fixture.repo.DB().QueryRow(`UPDATE tasks SET agent_id = ? WHERE id = ? RETURNING id`, originalAgent.ID, fixture.task.ID).Scan(new(string)))

	projectRepo := repository.NewProjectRepo(fixture.repo.DB())
	execRepo := repository.NewExecutionRepo(fixture.repo.DB())
	execRepo.SetAutomationRepo(fixture.repo)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, fixture.taskRepo, projectRepo, fixture.schedRepo, repository.NewAttachmentRepo(fixture.repo.DB()))
	mockLLM := testutil.NewMockLLMCaller()
	llmSvc.SetLLMCaller(mockLLM)
	llmSvc.SetAutomationRepo(fixture.repo)

	worker := NewWorkerService(llmSvc, 1, projectRepo)
	worker.SetTaskRepo(fixture.taskRepo)
	worker.SetLLMConfigRepo(llmConfigRepo)
	worker.SetExecutionRepo(execRepo)
	worker.SetAutomationRepo(fixture.repo)
	claimEdited := make(chan error, 1)
	worker.beforeQueuedAutomationTaskClaim = func(models.Task) {
		_, err := fixture.repo.DB().Exec(`UPDATE tasks SET agent_id = ? WHERE id = ? AND status = 'pending'`, blockedAgent.ID, fixture.task.ID)
		claimEdited <- err
	}
	require.True(t, worker.TryAcquireModelSlot(blockedAgent.ID), "test must saturate the atomically admitted model")
	blockedSlotHeld := true
	defer func() {
		if blockedSlotHeld {
			worker.ReleaseModelSlot(blockedAgent.ID)
		}
	}()
	worker.Start(ctx)
	defer worker.Stop()

	_, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Now().UTC(), fixture.schedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	require.NotNil(t, dispatch)
	dispatcher := NewAutomationDispatcher(fixture.repo, fixture.taskRepo, worker)
	dispatched, err := dispatcher.DispatchOne(ctx)
	require.NoError(t, err)
	require.True(t, dispatched)

	select {
	case err := <-claimEdited:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("queued Automation did not reach the model-transfer claim boundary")
	}
	require.Eventually(t, func() bool {
		var taskStatus, executionStatus string
		err := fixture.repo.DB().QueryRow(`SELECT t.status, e.status FROM tasks t JOIN executions e ON e.task_id = t.id WHERE e.dispatch_id = ?`, dispatch.ID).
			Scan(&taskStatus, &executionStatus)
		return err == nil && taskStatus == string(models.StatusRunning) && executionStatus == string(models.ExecRunning)
	}, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, 1, worker.TotalRunning())
	require.Eventually(t, func() bool {
		return worker.ModelRunning(originalAgent.ID) == 0 && worker.ModelRunning(blockedAgent.ID) == 1
	}, 5*time.Second, 20*time.Millisecond, "stale model reservation must be released before transfer wait")

	require.True(t, worker.CancelRunningTask(fixture.task.ID))
	require.Eventually(t, func() bool {
		var taskStatus, executionStatus, dispatchStatus, invocationStatus, executionError string
		var reservations, executionCompleted, invocationCompleted int
		err := fixture.repo.DB().QueryRow(`SELECT t.status, e.status, d.status, i.status, e.error_message,
			(SELECT COUNT(*) FROM automation_task_run_reservations r WHERE r.dispatch_id = d.id),
			e.completed_at IS NOT NULL, i.completed_at IS NOT NULL
			FROM automation_dispatch_outbox d
			JOIN automation_invocations i ON i.id = d.invocation_id
			JOIN executions e ON e.id = d.execution_id
			JOIN tasks t ON t.id = d.task_id
			WHERE d.id = ?`, dispatch.ID).Scan(&taskStatus, &executionStatus, &dispatchStatus, &invocationStatus,
			&executionError, &reservations, &executionCompleted, &invocationCompleted)
		return err == nil && taskStatus == string(models.StatusCancelled) && executionStatus == string(models.ExecCancelled) &&
			dispatchStatus == "failed" && invocationStatus == string(models.AutomationInvocationCancelled) && reservations == 0 &&
			executionCompleted == 1 && invocationCompleted == 1 && strings.Contains(executionError, "cancelled")
	}, 5*time.Second, 20*time.Millisecond)
	require.Zero(t, mockLLM.CallCount(), "provider must not run after cancellation during model transfer")
	require.Equal(t, 0, worker.TotalRunning())
	require.Equal(t, 0, worker.ProjectRunning(fixture.task.ProjectID))
	require.Equal(t, 0, worker.ModelRunning(originalAgent.ID))
	require.Equal(t, 1, worker.ModelRunning(blockedAgent.ID), "worker must not release the test-owned saturated slot")
	worker.ReleaseModelSlot(blockedAgent.ID)
	blockedSlotHeld = false
	require.Equal(t, 0, worker.ModelRunning(blockedAgent.ID))

	var activityStatus string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status FROM automation_activities WHERE invocation_id = ? AND activity_key = ?`,
		dispatch.InvocationID, "dispatch:"+dispatch.ID+":execute").Scan(&activityStatus))
	require.Equal(t, string(models.AutomationActivityCancelled), activityStatus)
}

func TestAutomationRuntimeSetupFailureAfterClaimTerminalizesDispatch(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	llmConfigRepo := repository.NewLLMConfigRepo(fixture.repo.DB())
	agent := models.LLMConfig{Name: "Removed queued Automation worker", Provider: models.ProviderTest, Model: "removed", IsDefault: true, MaxWorkers: 1}
	require.NoError(t, llmConfigRepo.Create(ctx, &agent))
	require.NoError(t, fixture.repo.DB().QueryRow(`UPDATE tasks SET agent_id = ? WHERE id = ? RETURNING id`, agent.ID, fixture.task.ID).Scan(new(string)))

	projectRepo := repository.NewProjectRepo(fixture.repo.DB())
	execRepo := repository.NewExecutionRepo(fixture.repo.DB())
	execRepo.SetAutomationRepo(fixture.repo)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, fixture.taskRepo, projectRepo, fixture.schedRepo, repository.NewAttachmentRepo(fixture.repo.DB()))
	mockLLM := testutil.NewMockLLMCaller()
	llmSvc.SetLLMCaller(mockLLM)
	llmSvc.SetAutomationRepo(fixture.repo)

	worker := NewWorkerService(llmSvc, 1, projectRepo)
	worker.SetTaskRepo(fixture.taskRepo)
	worker.SetLLMConfigRepo(llmConfigRepo)
	worker.SetExecutionRepo(execRepo)
	worker.SetAutomationRepo(fixture.repo)
	modelRemoved := make(chan error, 1)
	worker.beforeQueuedAutomationTaskClaim = func(models.Task) {
		configs, err := llmConfigRepo.List(context.Background())
		if err == nil {
			for _, config := range configs {
				if err = llmConfigRepo.Delete(context.Background(), config.ID); err != nil {
					break
				}
			}
		}
		modelRemoved <- err
	}
	worker.Start(ctx)
	defer worker.Stop()

	_, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Now().UTC(), fixture.schedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	require.NotNil(t, dispatch)
	dispatcher := NewAutomationDispatcher(fixture.repo, fixture.taskRepo, worker)
	dispatched, err := dispatcher.DispatchOne(ctx)
	require.NoError(t, err)
	require.True(t, dispatched)

	select {
	case err := <-modelRemoved:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("queued Automation did not reach the model-removal claim boundary")
	}

	require.Eventually(t, func() bool {
		var taskStatus, executionStatus, dispatchStatus, invocationStatus, activityStatus, executionError string
		var reservations, executionCompleted, invocationCompleted int
		err := fixture.repo.DB().QueryRow(`SELECT t.status, e.status, d.status, i.status, a.status, e.error_message,
			(SELECT COUNT(*) FROM automation_task_run_reservations r WHERE r.dispatch_id = d.id),
			e.completed_at IS NOT NULL, i.completed_at IS NOT NULL
			FROM automation_dispatch_outbox d
			JOIN automation_invocations i ON i.id = d.invocation_id
			JOIN executions e ON e.id = d.execution_id
			JOIN tasks t ON t.id = d.task_id
			JOIN automation_activities a ON a.invocation_id = i.id AND a.activity_key = 'dispatch:' || d.id || ':execute'
			WHERE d.id = ?`, dispatch.ID).Scan(&taskStatus, &executionStatus, &dispatchStatus, &invocationStatus,
			&activityStatus, &executionError, &reservations, &executionCompleted, &invocationCompleted)
		return err == nil && taskStatus == string(models.StatusFailed) && executionStatus == string(models.ExecFailed) &&
			dispatchStatus == "failed" && invocationStatus == string(models.AutomationInvocationFailed) &&
			activityStatus == string(models.AutomationActivityFailed) && reservations == 0 && executionCompleted == 1 &&
			invocationCompleted == 1 && strings.Contains(executionError, "no agent configured")
	}, 5*time.Second, 20*time.Millisecond)
	require.Zero(t, mockLLM.CallCount(), "provider must not run when the admitted model disappears")
	require.Equal(t, 0, worker.TotalRunning())
	require.Equal(t, 0, worker.ProjectRunning(fixture.task.ProjectID))
	require.Equal(t, 0, worker.ModelRunning(agent.ID))
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

func TestAutomationRuntimeReconcilerAbandonsCancelledCapacityQueuedDispatch(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	_, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Now().UTC(), fixture.schedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	leased, err := fixture.repo.LeaseNextDispatch(ctx, "owner", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NoError(t, fixture.repo.MarkDispatchQueued(ctx, leased.ID, "owner"))
	require.NoError(t, fixture.taskRepo.UpdateStatus(ctx, fixture.task.ID, models.StatusCancelled))

	reconciler := NewAutomationReconciler(fixture.repo, repository.NewExecutionRepo(fixture.repo.DB()), NewWorkerService(nil, 1, nil))
	require.NoError(t, reconciler.ReconcileOnce(ctx))
	var dispatchStatus string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status FROM automation_dispatch_outbox WHERE id = ?`, dispatch.ID).Scan(&dispatchStatus))
	require.Equal(t, "failed", dispatchStatus)
	var invocationStatus string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT status FROM automation_invocations WHERE id = ?`, dispatch.InvocationID).Scan(&invocationStatus))
	require.Equal(t, "cancelled", invocationStatus)
	var reservations int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_task_run_reservations WHERE dispatch_id = ?`, dispatch.ID).Scan(&reservations))
	require.Zero(t, reservations)
}

func TestAutomationRuntimeReconcilerResubmitsCapacityQueuedPreparedDispatch(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	_, dispatch, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Now().UTC(), fixture.schedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	require.NotNil(t, dispatch)
	leased, err := fixture.repo.LeaseNextDispatch(ctx, "owner", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.Equal(t, dispatch.ID, leased.ID)
	require.NoError(t, fixture.repo.MarkDispatchQueued(ctx, dispatch.ID, "owner"))

	storedTask, err := fixture.taskRepo.GetByID(ctx, fixture.task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusPending, storedTask.Status)
	worker := NewWorkerService(nil, 1, nil)
	reconciler := NewAutomationReconciler(fixture.repo, repository.NewExecutionRepo(fixture.repo.DB()), worker)
	require.NoError(t, reconciler.ReconcileOnce(ctx))
	select {
	case submitted := <-worker.Submitted():
		require.Equal(t, fixture.task.ID, submitted.ID)
	default:
		t.Fatal("expected reconciler to resubmit the capacity-queued prepared dispatch")
	}
	var executionCount int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM executions WHERE dispatch_id = ?`, dispatch.ID).Scan(&executionCount))
	require.Zero(t, executionCount)
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

func TestAutomationRuntimeAuthorizedAssigneeScanReanchorsStaleIssueProjection(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterGitHubSDLC)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(fixture.repo.DB())
	fixture.project.RepoURL = "https://github.com/example/runtime.git"
	require.NoError(t, projectRepo.Update(ctx, &fixture.project))
	repoRef := &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}
	issue := GitHubIssue{Number: 288, URL: "https://github.com/example/runtime/issues/288", Title: "Stale assigned issue", Body: "Stale issue body", State: "open", Assignees: []string{"dubee"}, Labels: []string{"bug"}, CompleteForTaskCreation: true, TaskCreationCompletenessKnown: true}
	resourceID := githubIssueResourceID(repoRef, issue.Number)

	_, err := fixture.repo.DB().ExecContext(ctx, `PRAGMA foreign_keys = OFF`)
	require.NoError(t, err)
	_, err = fixture.repo.DB().ExecContext(ctx, `INSERT INTO automation_work_items
		(id, project_id, automation_id, origin_version_id, work_item_key, kind, title, status)
		VALUES ('stale-github-issue-work-item', ?, ?, 'discarded-version', ?, 'github_issue', 'Old title', 'waiting')`,
		fixture.project.ID, fixture.definition.Automation.ID, resourceID)
	require.NoError(t, err)
	_, err = fixture.repo.DB().ExecContext(ctx, `INSERT INTO automation_activities
		(id, project_id, automation_id, version_id, node_id, work_item_id, activity_key, activity_type, status)
		VALUES ('stale-github-issue-activity', ?, ?, 'discarded-version', 'discarded-inbox-node', 'stale-github-issue-work-item', 'stale-discovery', 'discover_assigned_issue', 'completed')`,
		fixture.project.ID, fixture.definition.Automation.ID)
	require.NoError(t, err)
	_, err = fixture.repo.DB().ExecContext(ctx, `INSERT INTO automation_activity_resources
		(activity_id, resource_type, resource_id, relation) VALUES ('stale-github-issue-activity', 'github_issue', ?, 'subject')`, resourceID)
	require.NoError(t, err)
	_, err = fixture.repo.DB().ExecContext(ctx, `INSERT INTO automation_work_item_positions
		(work_item_id, project_id, automation_id, version_id, node_id, state)
		VALUES ('stale-github-issue-work-item', ?, ?, 'discarded-version', 'discarded-inbox-node', 'waiting')`,
		fixture.project.ID, fixture.definition.Automation.ID)
	require.NoError(t, err)
	_, err = fixture.repo.DB().ExecContext(ctx, `INSERT INTO thread_inputs
		(id, scope, project_id, task_id, input_mode, input_status, content, queue_position, source)
		VALUES ('stale-thread-input', 'task_thread', ?, ?, 'queued', 'pending', 'stale queued follow-up', 1, 'test')`,
		fixture.project.ID, fixture.task.ID)
	require.NoError(t, err)
	_, err = fixture.repo.DB().ExecContext(ctx, `INSERT INTO automation_thread_input_bindings
		(id, thread_input_id, project_id, automation_id, version_id, node_id, work_item_id, binding_key)
		VALUES ('stale-thread-input-binding', 'stale-thread-input', ?, ?, 'discarded-version', 'discarded-inbox-node', 'stale-github-issue-work-item', 'stale:0')`,
		fixture.project.ID, fixture.definition.Automation.ID)
	require.NoError(t, err)
	_, err = fixture.repo.DB().ExecContext(ctx, `INSERT INTO automation_work_items
		(id, project_id, automation_id, origin_version_id, parent_work_item_id, work_item_key, kind, title, status)
		VALUES ('stale-child-work-item', ?, ?, 'discarded-version', 'stale-github-issue-work-item', 'stale-child-key', 'work', 'Stale child', 'waiting')`,
		fixture.project.ID, fixture.definition.Automation.ID)
	require.NoError(t, err)
	_, err = fixture.repo.DB().ExecContext(ctx, `INSERT INTO automation_transitions
		(project_id, automation_id, version_id, work_item_id, activity_id, to_node_id, event_key, state)
		VALUES (?, ?, 'discarded-version', 'stale-github-issue-work-item', 'stale-github-issue-activity', 'discarded-inbox-node', 'stale-transition', 'waiting')`,
		fixture.project.ID, fixture.definition.Automation.ID)
	require.NoError(t, err)
	_, err = fixture.repo.DB().ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	require.NoError(t, err)

	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) { return repoRef, nil },
		listAssignedIssuesFn: func(_ context.Context, _ *GitHubRepoRef, assignee string) ([]GitHubIssue, error) {
			require.Equal(t, "dubee", assignee)
			return []GitHubIssue{issue}, nil
		},
	}
	githubAuthRepo := repository.NewGitHubAuthRepo(fixture.repo.DB())
	require.NoError(t, githubAuthRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "dubee"}))
	handlers := buildGitHubIssueRuntimeHandlers(githubIssueRuntimeOptions{ProjectID: fixture.project.ID, ProjectRepo: projectRepo,
		TaskRepo: fixture.taskRepo, AutomationRepo: fixture.repo, GitHubAuthRepo: githubAuthRepo, GitHub: provider})
	inboxCtx := newAutomationGitHubIssueCausalContext(t, fixture, fixture.definition, fixture.task, "dev_inbox", "stale-assigned-scan")

	out, err := handlers["github_list_assigned_issues"](inboxCtx, json.RawMessage(`{"assignee":"dubee"}`))
	require.NoError(t, err)
	require.Contains(t, out, `"Number":288`)
	var originVersionID, title string
	require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `SELECT origin_version_id, title FROM automation_work_items
		WHERE automation_id = ? AND work_item_key = ?`, fixture.definition.Automation.ID, resourceID).Scan(&originVersionID, &title))
	require.Equal(t, fixture.definition.Version.ID, originVersionID)
	require.Equal(t, issue.Title, title)
	var staleRows int
	require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_work_items WHERE id = 'stale-github-issue-work-item'`).Scan(&staleRows))
	require.Zero(t, staleRows)
	require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_activities WHERE id = 'stale-github-issue-activity'`).Scan(&staleRows))
	require.Zero(t, staleRows)
	require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_transitions WHERE event_key = 'stale-transition'`).Scan(&staleRows))
	require.Zero(t, staleRows)
	require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_thread_input_bindings WHERE id = 'stale-thread-input-binding'`).Scan(&staleRows))
	require.Zero(t, staleRows)
	require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_work_items WHERE id = 'stale-child-work-item'`).Scan(&staleRows))
	require.Zero(t, staleRows)
}

func TestAutomationRuntimeGitHubIssueTaskCreationAllowsLaterInboxInvocation(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterGitHubSDLC)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(fixture.repo.DB())
	fixture.project.RepoURL = "https://github.com/example/runtime.git"
	require.NoError(t, projectRepo.Update(ctx, &fixture.project))

	provider := &fakeGitHubIssueRuntimeProvider{resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
		return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
	}}
	opts := githubIssueRuntimeOptions{ProjectID: fixture.project.ID, ProjectRepo: projectRepo, TaskRepo: fixture.taskRepo,
		AutomationRepo: fixture.repo, GitHub: provider}
	repoRef, err := provider.ResolveRepo(ctx, fixture.project.RepoURL, "")
	require.NoError(t, err)
	issue := GitHubIssue{Number: 91, URL: "https://github.com/example/runtime/issues/91", Title: "Created before inbox run", State: "open"}

	producerCtx := newAutomationGitHubIssueCausalContext(t, fixture, fixture.definition, fixture.task, "bug_finder", "producer-invocation-a")
	recorded, err := recordGitHubIssueCreated(producerCtx, opts, repoRef, &issue, "cross-invocation-issue")
	require.NoError(t, err)
	require.Equal(t, 1, recorded)
	var githubMailboxOwners int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_artifact_mailbox_owners WHERE artifact_type = 'github_issue'`).Scan(&githubMailboxOwners))
	require.Zero(t, githubMailboxOwners, "GitHub issue discovery no longer relies on durable issue-key mailbox owner mappings")

	inboxCtx := newAutomationGitHubIssueCausalContext(t, fixture, fixture.definition, fixture.task, "dev_inbox", "inbox-invocation-b")
	filtered, err := filterGitHubAssignedIssuesForAutomationInbox(inboxCtx, opts, repoRef, []GitHubIssue{issue})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	require.NoError(t, recordGitHubAssignedIssues(inboxCtx, opts, repoRef, filtered))

	var discoveredWorkItemID string
	var discoveredWorkStatus models.AutomationWorkItemStatus
	var discoveredPositionState models.AutomationTransitionState
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT work_item.id, work_item.status, position.state
		FROM automation_work_items work_item
		JOIN automation_work_item_positions position ON position.work_item_id = work_item.id
		JOIN automation_nodes node ON node.id = position.node_id
		WHERE work_item.project_id = ? AND work_item.automation_id = ? AND work_item.work_item_key = ?
			AND node.role = 'github_inbox'`, fixture.project.ID, fixture.definition.Automation.ID,
		githubIssueResourceID(repoRef, issue.Number)).Scan(&discoveredWorkItemID, &discoveredWorkStatus, &discoveredPositionState))
	require.Equal(t, models.AutomationWorkItemWaiting, discoveredWorkStatus, "completed issue discovery must wait at the inbox rather than remain running")
	require.Equal(t, models.AutomationTransitionWaiting, discoveredPositionState)
	liveAfterDiscovery, err := NewAutomationGraphService(fixture.repo).GetLive(ctx, fixture.project.ID, fixture.definition.Automation.ID, time.Now().UTC())
	require.NoError(t, err)
	devInbox := automationNodeByKey(t, fixture.definition, "dev_inbox")
	for _, node := range liveAfterDiscovery.Nodes {
		if node.ID == devInbox.ID {
			require.Zero(t, node.Counts.Running)
			require.Equal(t, 1, node.Counts.Waiting)
		}
	}
	require.NoError(t, fixture.repo.DB().QueryRow(`UPDATE automation_work_item_positions SET state = 'active'
		WHERE work_item_id = ? AND node_id = ? RETURNING work_item_id`, discoveredWorkItemID, devInbox.ID).Scan(new(string)))
	legacyLive, err := NewAutomationGraphService(fixture.repo).GetLive(ctx, fixture.project.ID, fixture.definition.Automation.ID, time.Now().UTC())
	require.NoError(t, err)
	for _, node := range legacyLive.Nodes {
		if node.ID == devInbox.ID {
			require.Zero(t, node.Counts.Running, "legacy active inbox positions must not leave the completed poll looking stuck")
			require.Zero(t, node.Counts.Waiting, "legacy active inbox positions predate explicit waiting semantics and must not look actionable forever")
		}
	}
	portfolioCounts, err := fixture.repo.PortfolioOperationalCounts(ctx, fixture.project.ID, time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.Zero(t, portfolioCounts[fixture.definition.Automation.ID].Running)
	require.Zero(t, portfolioCounts[fixture.definition.Automation.ID].Waiting)

	producerAutomationCtx, ok := AutomationContextFromContext(producerCtx)
	require.True(t, ok)
	inboxAutomationCtx, ok := AutomationContextFromContext(inboxCtx)
	require.True(t, ok)
	require.NotEqual(t, producerAutomationCtx.Bindings[0].InvocationID, inboxAutomationCtx.Bindings[0].InvocationID,
		"issue creation and later inbox discovery must use distinct invocations")
	var originInvocationID, discoveryInvocationID string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT work_item.origin_invocation_id, discovery.invocation_id
		FROM automation_work_items work_item
		JOIN automation_activities discovery ON discovery.work_item_id = work_item.id
			AND discovery.activity_type = 'discover_assigned_issue'
		JOIN automation_activity_resources resource ON resource.activity_id = discovery.id
			AND resource.resource_type = 'github_issue' AND resource.resource_id = ?
		WHERE work_item.project_id = ? AND work_item.automation_id = ?`, githubIssueResourceID(repoRef, issue.Number),
		fixture.project.ID, fixture.definition.Automation.ID).Scan(&originInvocationID, &discoveryInvocationID))
	require.Equal(t, producerAutomationCtx.Bindings[0].InvocationID, originInvocationID)
	require.Equal(t, inboxAutomationCtx.Bindings[0].InvocationID, discoveryInvocationID)

	workerSvc := newTestWorkerService(t)
	taskSvc := NewTaskService(fixture.taskRepo, nil, workerSvc)
	llmSvc := &LLMService{automationRepo: fixture.repo, githubIssueRuntime: provider, projectRepo: projectRepo,
		taskRepo: fixture.taskRepo, taskSvc: taskSvc}
	runtime := llmSvc.taskControlRuntimeTools(fixture.task)
	require.NotNil(t, runtime)
	wrongExecution := models.Execution{TaskID: fixture.task.ID, Status: models.ExecRunning, PromptSent: "wrong inbox execution"}
	require.NoError(t, repository.NewExecutionRepo(fixture.repo.DB()).Create(ctx, &wrongExecution))
	wrongExecutionCtx := withAutomationExecution(inboxCtx, fixture.task.ID, wrongExecution.ID)
	_, handled, isErr, err := runtime.Executor(wrongExecutionCtx, "create_task", json.RawMessage(`{
		"title":"Reject different execution","prompt":"must not persist","category":"backlog",
		"source_github_issue_number":91
	}`))
	require.True(t, handled)
	require.True(t, isErr)
	require.ErrorContains(t, err, "not discovered by this exact current Automation execution")
	rejectedTask, err := fixture.taskRepo.GetByProjectAndTitle(ctx, fixture.project.ID, "Reject different execution")
	require.NoError(t, err)
	require.Nil(t, rejectedTask)

	output, handled, isErr, err := runtime.Executor(inboxCtx, "create_task", json.RawMessage(`{
		"title":"Implement issue from prior invocation","prompt":"implement issue 91","category":"backlog",
		"source_github_issue_number":91
	}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr)
	implementationTask, err := fixture.taskRepo.GetByProjectAndTitle(ctx, fixture.project.ID, "Implement issue from prior invocation")
	require.NoError(t, err)
	require.Contains(t, output, implementationTask.ID)
	select {
	case submitted := <-workerSvc.Submitted():
		require.Equal(t, implementationTask.ID, submitted.ID)
	case <-time.After(time.Second):
		t.Fatal("approved GitHub issue task was not submitted")
	}
}

func TestAutomationRuntimeGitHubIssueInboxAndPRProvenance(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterGitHubSDLC)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(fixture.repo.DB())
	fixture.project.RepoURL = "https://github.example.com/example/runtime.git"
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
		globalAPIEndpoint: "https://github.example.com/api/v3",
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*GitHubRepoRef, error) {
			resolvedRepoMu.Lock()
			defer resolvedRepoMu.Unlock()
			resolvedRepoURLs = append(resolvedRepoURLs, repoURL)
			resolvedRepoPaths = append(resolvedRepoPaths, repoPath)
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.example.com/example/runtime"}, nil
		},
		createIssueFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
			createCalls.Add(1)
			return &GitHubIssue{Number: 42, URL: "https://github.example.com/example/runtime/issues/42", Title: req.Title, State: "open"}, nil
		},
		listMyIssuesFn: func(context.Context, *GitHubRepoRef) (*GitHubAuthenticatedUser, []GitHubIssue, error) {
			return &GitHubAuthenticatedUser{Login: "dev"}, []GitHubIssue{{Number: 42, Title: "Exact issue", State: "open"}, {Number: 43, Title: "Second owned issue", State: "open"}, {Number: 44, Title: "Local remote issue", State: "open"}, {Number: 45, Title: "Unrelated assigned issue", State: "open"}}, nil
		},
		createPRFn: func(_ context.Context, repo *GitHubRepoRef, req GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
			return &GitHubPullRequest{Number: 7, URL: "https://github.example.com/example/runtime/pull/7", State: "open", HeadRef: req.Head, HeadRepoFullName: repo.FullName, HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		}}
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

	ambiguousInput := githubCreateIssueRuntimeInput{Title: "Ambiguous issue", Body: "body", Labels: []string{"bug"}}
	repoRef, err := provider.ResolveRepo(ctx, fixture.project.RepoURL, fixture.project.RepoPath)
	require.NoError(t, err)
	for _, ownedIssue := range []GitHubIssue{{Number: 43, Title: "Second owned issue"}, {Number: 44, Title: "Local remote issue"}} {
		recorded, recordErr := recordGitHubIssueCreated(ctx, opts, repoRef, &ownedIssue, fmt.Sprintf("owned-issue-%d", ownedIssue.Number))
		require.NoError(t, recordErr)
		require.Equal(t, 1, recorded)
	}
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
	assignedOutput, err := handlers["github_list_my_assigned_issues"](inboxCtx, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Contains(t, assignedOutput, `"Number":42`)
	require.Contains(t, assignedOutput, `"Number":43`)
	require.Contains(t, assignedOutput, `"Number":44`)
	require.Contains(t, assignedOutput, `"Number":45`)
	var issueItems int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_work_items WHERE automation_id = ? AND kind = 'github_issue'`, fixture.definition.Automation.ID).Scan(&issueItems))
	require.Equal(t, 4, issueItems, "the inbox must record every issue returned by an authorized assignment scan, including manually created issues")

	workerSvc := newTestWorkerService(t)
	taskSvc := NewTaskService(fixture.taskRepo, nil, workerSvc)
	implementationNode := automationNodeByKey(t, fixture.definition, "implementation")
	var implementationConfig map[string]any
	require.NoError(t, json.Unmarshal([]byte(implementationNode.ConfigJSON), &implementationConfig))
	implementationConfig["goal"] = "Complete the assigned issue with focused regression coverage."
	updatedImplementationConfig, err := json.Marshal(implementationConfig)
	require.NoError(t, err)
	require.NoError(t, fixture.repo.DB().QueryRow(`UPDATE automation_nodes SET config_json = ? WHERE id = ? RETURNING id`, string(updatedImplementationConfig), implementationNode.ID).Scan(new(string)))
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

	manualOutput, handled, isErr, manualErr := runtime.Executor(inboxCtx, "create_task", json.RawMessage(`{
		"title":"Manual assigned issue task","prompt":"implement manually created issue 45","category":"backlog",
		"source_github_issue_number":45
	}`))
	require.NoError(t, manualErr)
	require.True(t, handled)
	require.False(t, isErr)
	manualTask, err := fixture.taskRepo.GetByProjectAndTitle(ctx, fixture.project.ID, "Manual assigned issue task")
	require.NoError(t, err)
	require.Contains(t, manualOutput, manualTask.ID)
	select {
	case submitted := <-workerSvc.Submitted():
		require.Equal(t, manualTask.ID, submitted.ID, "a manually created GitHub issue assigned to the PAT owner must be submitted")
	case <-time.After(time.Second):
		t.Fatal("manually assigned GitHub issue task was not submitted to the worker")
	}

	configuredRepoURL := fixture.project.RepoURL
	fixture.project.RepoURL = ""
	require.NoError(t, projectRepo.Update(ctx, &fixture.project))
	localRemoteOutput, handled, isErr, localRemoteErr := runtime.Executor(inboxCtx, "create_task", json.RawMessage(`{
		"title":"Local remote issue task","prompt":"use the project local remote","goal":"ignore this model-supplied goal","category":"backlog",
		"source_github_issue_number":44,"source_github_repo_url":"https://github.com/attacker/fallback"
	}`))
	require.NoError(t, localRemoteErr)
	require.True(t, handled)
	require.False(t, isErr)
	require.Contains(t, localRemoteOutput, "Local remote issue task")
	localRemoteTask, err := fixture.taskRepo.GetByProjectAndTitle(ctx, fixture.project.ID, "Local remote issue task")
	require.NoError(t, err)
	require.Equal(t, models.CategoryActive, localRemoteTask.Category, "GitHub assignment is approval, so the issue-specific task must be admitted immediately")
	localRemoteGoal, err := repository.NewTaskGoalRepo(fixture.repo.DB()).GetByTaskID(ctx, localRemoteTask.ID)
	require.NoError(t, err)
	require.NotNil(t, localRemoteGoal)
	require.Equal(t, "Complete the assigned issue with focused regression coverage.", localRemoteGoal.Objective,
		"the persisted implementation-node goal must override a model-supplied goal")
	select {
	case submitted := <-workerSvc.Submitted():
		require.Equal(t, localRemoteTask.ID, submitted.ID, "the approved issue-specific task must be submitted to the worker")
	case <-time.After(time.Second):
		t.Fatal("approved GitHub issue task was not submitted to the worker")
	}
	resolvedRepoMu.Lock()
	require.NotEmpty(t, resolvedRepoPaths)
	require.Equal(t, fixture.project.RepoPath, resolvedRepoPaths[len(resolvedRepoPaths)-1], "Automation issue-task creation must fall back to the project's local Git remote")
	require.Empty(t, resolvedRepoURLs[len(resolvedRepoURLs)-1], "a model-supplied repository override must remain ignored")
	resolvedRepoURLs = nil
	resolvedRepoPaths = nil
	resolvedRepoMu.Unlock()
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
	require.Len(t, afterDuplicateTasks, len(beforeTasks)+3, "one issue work item must have at most one implementation task")

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
	require.Len(t, afterChannelTasks, len(beforeTasks)+3, "task-bound channel create_task must use exact Automation provenance")

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
	require.Len(t, afterConcurrentTasks, len(beforeTasks)+4, "concurrent creation must persist one canonical task for issue 43")
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
	missingBodyOutput, err := handlers["github_open_pull_request"](implementationCtx, json.RawMessage(`{"task_id":"current","issue_number":42,"pr_title":"PR"}`))
	require.Empty(t, missingBodyOutput)
	require.ErrorContains(t, err, "require pr_body with a factual summary and validation")
	mismatchedIssueBody := "## Summary\n- Implements the wrong issue.\n\n## Validation\n- go test ./internal/service\n\nCloses #43"
	mismatchedIssueOutput, err := handlers["github_open_pull_request"](implementationCtx, json.RawMessage(fmt.Sprintf(`{"task_id":"current","issue_number":43,"pr_title":"PR","pr_body":%q}`, mismatchedIssueBody)))
	require.Empty(t, mismatchedIssueOutput)
	require.ErrorContains(t, err, "must match the task's source issue #42")
	forgedIssueURLBody := "## Summary\n- Implements the accepted issue.\n\n## Validation\n- go test ./internal/service\n\nCloses #42"
	forgedIssueURLOutput, err := handlers["github_open_pull_request"](implementationCtx, json.RawMessage(fmt.Sprintf(`{"task_id":"current","issue_number":42,"issue_url":"https://github.com/attacker/other/issues/42","pr_title":"PR","pr_body":%q}`, forgedIssueURLBody)))
	require.Empty(t, forgedIssueURLOutput)
	require.ErrorContains(t, err, "issue_url must match the task's source issue")
	prBody := "## Summary\n- Implements the accepted issue.\n\n## Validation\n- go test ./internal/service\n\nCloses #42"
	openedOutput, err := handlers["github_open_pull_request"](implementationCtx, json.RawMessage(fmt.Sprintf(`{"task_id":"current","issue_number":42,"pr_title":"PR","pr_body":%q}`, prBody)))
	require.NoError(t, err)
	require.Contains(t, openedOutput, `"created":true`)
	provider.getPullRequestFn = func(context.Context, *GitHubRepoRef, int) (*GitHubPullRequest, error) {
		return &GitHubPullRequest{Number: 7, URL: "https://github.example.com/example/runtime/pull/7", State: "open", HeadRef: implementationTask.WorktreeBranch, HeadRepoFullName: "example/runtime", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
	}
	var recordedIssueURL string
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT issue_url FROM task_pull_requests WHERE task_id = ?`, implementationTask.ID).Scan(&recordedIssueURL))
	require.Equal(t, "https://github.example.com/example/runtime/issues/42", recordedIssueURL)
	reusedOutput, err := handlers["github_open_pull_request"](implementationCtx, json.RawMessage(fmt.Sprintf(`{"task_id":"current","issue_number":42,"pr_title":"PR","pr_body":%q}`, prBody)))
	require.NoError(t, err)
	require.Contains(t, reusedOutput, `"reused_existing_record":true`)
	for _, repoPath := range resolvedRepoPaths {
		require.Empty(t, repoPath, "Automation GitHub repository resolution must never receive repo_path")
	}
	_, err = handlers["github_replace_pull_request_branch"](implementationCtx, json.RawMessage(fmt.Sprintf(`{"task_id":%q,"expected_head_sha":%q,"confirm_history_rewrite":true}`, fixture.task.ID, strings.Repeat("a", 40))))
	require.ErrorContains(t, err, "cannot mutate a different task")
	var prResources int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activity_resources WHERE resource_type = 'pull_request' AND resource_id = 'github:example/runtime:pull:7'`).Scan(&prResources))
	require.Equal(t, 1, prResources)
	edgeExpectations := map[string]int{
		"bug_to_issue": 3, "issue_to_assignment": 3, "assignment_to_inbox": 4,
		"inbox_to_implementation": 4, "implementation_to_pr": 1, "pr_to_review": 1,
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

func TestAutomationGitHubPRPublicationUsesDurableTaskProvenanceAfterGraphReplacement(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterGitHubSDLC)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(fixture.repo.DB())
	fixture.project.RepoURL = "https://github.com/example/runtime.git"
	fixture.project.RepoPath = t.TempDir()
	require.NoError(t, projectRepo.Update(ctx, &fixture.project))
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
		},
		listMyIssuesFn: func(context.Context, *GitHubRepoRef) (*GitHubAuthenticatedUser, []GitHubIssue, error) {
			return &GitHubAuthenticatedUser{Login: "dev"}, []GitHubIssue{{Number: 42, Title: "Replace-safe issue", State: "open"}}, nil
		},
		createPRFn: func(_ context.Context, repo *GitHubRepoRef, req GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
			return &GitHubPullRequest{Number: 77, URL: "https://github.com/example/runtime/pull/77", State: "open", HeadRef: req.Head, HeadRepoFullName: repo.FullName, HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		}}
	opts := githubIssueRuntimeOptions{ProjectID: fixture.project.ID, ProjectRepo: projectRepo, TaskRepo: fixture.taskRepo,
		TaskPullRequestRepo: repository.NewTaskPullRequestRepo(fixture.repo.DB()), AutomationRepo: fixture.repo, GitHub: provider}
	handlers := buildGitHubIssueRuntimeHandlers(opts)
	execution := models.Execution{TaskID: fixture.task.ID, Status: models.ExecRunning, PromptSent: "github inbox"}
	require.NoError(t, repository.NewExecutionRepo(fixture.repo.DB()).Create(ctx, &execution))
	invocation, _, err := fixture.repo.ClaimScheduledOccurrence(ctx, fixture.schedule, time.Now().UTC(), fixture.schedule.ComputeNextRun(time.Now().UTC()))
	require.NoError(t, err)
	devInbox := automationNodeByKey(t, fixture.definition, "dev_inbox")
	inboxBinding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, InvocationID: invocation.ID, NodeID: devInbox.ID}
	inboxCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{inboxBinding}})
	inboxCtx = withAutomationExecution(inboxCtx, fixture.task.ID, execution.ID)
	_, err = handlers["github_list_my_assigned_issues"](inboxCtx, json.RawMessage(`{}`))
	require.NoError(t, err)
	workerSvc := newTestWorkerService(t)
	llmSvc := &LLMService{automationRepo: fixture.repo, githubIssueRuntime: provider, projectRepo: projectRepo,
		taskRepo: fixture.taskRepo, taskSvc: NewTaskService(fixture.taskRepo, nil, workerSvc)}
	createRuntime := llmSvc.taskControlRuntimeTools(fixture.task)
	createOutput, handled, isErr, err := createRuntime.Executor(inboxCtx, "create_task", json.RawMessage(`{
		"title":"Implement issue 42 after graph replacement","prompt":"implement issue 42","category":"backlog",
		"source_github_issue_number":42
	}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr)
	implementationTask, err := fixture.taskRepo.GetByProjectAndTitle(ctx, fixture.project.ID, "Implement issue 42 after graph replacement")
	require.NoError(t, err)
	require.NotNil(t, implementationTask)
	require.Contains(t, createOutput, implementationTask.ID)
	select {
	case <-workerSvc.Submitted():
	case <-time.After(time.Second):
		t.Fatal("approved GitHub issue task was not submitted")
	}
	staleTaskContext, err := fixture.repo.ContextForTask(ctx, fixture.project.ID, implementationTask.ID)
	require.NoError(t, err)
	require.NotEmpty(t, staleTaskContext.Bindings)
	require.NoError(t, fixture.taskRepo.UpdateCategory(ctx, implementationTask.ID, models.CategoryActive))
	require.NoError(t, fixture.taskRepo.UpdateStatus(ctx, implementationTask.ID, models.StatusPending))
	dispatchClaim, claimed, err := fixture.taskRepo.ClaimTaskForDispatch(ctx, implementationTask.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.True(t, dispatchClaim.AutomationContext.OriginTask)
	require.NotEmpty(t, dispatchClaim.AutomationContext.Bindings)
	require.NoError(t, fixture.taskRepo.UpdateStatus(ctx, implementationTask.ID, models.StatusRunning))

	replaced := replaceAutomationGitHubIssueGraph(t, fixture)
	require.NotEqual(t, fixture.definition.Version.ID, replaced.Version.ID)
	worktreePath := t.TempDir()
	require.NoError(t, fixture.taskRepo.UpdateWorktreeInfo(ctx, implementationTask.ID, worktreePath, "task/issue-42-replaced"))
	implementationTask.WorktreePath = worktreePath
	implementationTask.WorktreeBranch = "task/issue-42-replaced"
	implementationExecution := models.Execution{TaskID: implementationTask.ID, Status: models.ExecRunning, PromptSent: "implementation"}
	require.NoError(t, repository.NewExecutionRepo(fixture.repo.DB()).Create(ctx, &implementationExecution))
	originCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: fixture.project.ID, OriginTask: true})
	originCtx = withAutomationExecution(originCtx, implementationTask.ID, implementationExecution.ID)
	staleOriginCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: fixture.project.ID, OriginTask: true, Bindings: staleTaskContext.Bindings})
	staleOriginCtx = withAutomationExecution(staleOriginCtx, implementationTask.ID, implementationExecution.ID)
	taskGoalSvc := NewTaskGoalService(repository.NewTaskGoalRepo(fixture.repo.DB()), fixture.taskRepo, nil)
	goal, err := taskGoalSvc.SetGoal(ctx, implementationTask.ID, "Publish a reviewable PR", GoalOptions{Actor: "test"})
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		_, err := taskGoalSvc.RecordBlockedReport(ctx, implementationTask.ID, goal.GoalID, GitHubPRPublicationBlockerKey, "PR publication failed")
		require.NoError(t, err)
	}
	publishSvc := &LLMService{automationRepo: fixture.repo, githubIssueRuntime: provider, projectRepo: projectRepo,
		taskRepo: fixture.taskRepo, taskPullRequestRepo: opts.TaskPullRequestRepo, githubPRFeedbackRepo: repository.NewGitHubPRFeedbackRepo(fixture.repo.DB()),
		githubAuthRepo: repository.NewGitHubAuthRepo(fixture.repo.DB()), threadInputRepo: repository.NewThreadInputRepo(fixture.repo.DB()), taskGoalSvc: taskGoalSvc}
	publishRuntime := publishSvc.AutomationGitHubRuntimeTools(staleOriginCtx, *implementationTask, gitHubIssueRuntimeToolDefs(true))
	require.NotNil(t, publishRuntime)
	prBody := "## Summary\n- Implements the accepted issue.\n\n## Validation\n- go test ./internal/service\n\nCloses #42"
	published, handled, isErr, err := publishRuntime.Executor(staleOriginCtx, "github_open_pull_request", []byte(fmt.Sprintf(`{"task_id":"current","issue_number":42,"pr_title":"PR","pr_body":%q}`, prBody)))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr)
	require.Contains(t, published, `"created":true`)
	clearedGoal, err := taskGoalSvc.GetGoal(ctx, implementationTask.ID)
	require.NoError(t, err)
	require.NotNil(t, clearedGoal)
	require.Equal(t, models.TaskGoalStatusActive, clearedGoal.Status)
	require.Empty(t, clearedGoal.BlockerKey)
	require.Zero(t, clearedGoal.BlockerCount)
	require.Equal(t, "GitHub PR publication succeeded with PR #77", clearedGoal.Reason)
	provider.getPullRequestFn = func(context.Context, *GitHubRepoRef, int) (*GitHubPullRequest, error) {
		return &GitHubPullRequest{Number: 77, URL: "https://github.com/example/runtime/pull/77", State: "open", HeadRef: implementationTask.WorktreeBranch, HeadRepoFullName: "example/runtime", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
	}
	reused, handled, isErr, err := publishRuntime.Executor(originCtx, "github_open_pull_request", []byte(fmt.Sprintf(`{"task_id":"current","issue_number":42,"pr_title":"PR","pr_body":%q}`, prBody)))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr)
	require.Contains(t, reused, `"reused_existing_record":true`)
	_, handled, isErr, err = publishRuntime.Executor(staleOriginCtx, "github_replace_pull_request_branch", []byte(fmt.Sprintf(`{"task_id":"current","expected_head_sha":%q,"confirm_history_rewrite":true}`, strings.Repeat("a", 40))))
	require.True(t, handled)
	require.True(t, isErr)
	require.ErrorContains(t, err, "current active Automation graph")
	record, err := opts.TaskPullRequestRepo.GetByTaskID(ctx, implementationTask.ID)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Equal(t, 77, record.PRNumber)
	require.NotNil(t, record.IssueNumber)
	require.Equal(t, 42, *record.IssueNumber)
	require.Equal(t, "https://github.com/example/runtime/issues/42", record.IssueURL)

	spoofed := &models.Task{ProjectID: fixture.project.ID, Title: "Spoofed stale Automation task", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "spoof", CreatedVia: "automation:" + fixture.definition.Automation.ID + ":implementation"}
	require.NoError(t, fixture.taskRepo.Create(ctx, spoofed))
	require.NoError(t, fixture.taskRepo.UpdateWorktreeInfo(ctx, spoofed.ID, t.TempDir(), "task/spoofed"))
	spoofed.WorktreeBranch = "task/spoofed"
	spoofExecution := models.Execution{TaskID: spoofed.ID, Status: models.ExecRunning, PromptSent: "spoof"}
	require.NoError(t, repository.NewExecutionRepo(fixture.repo.DB()).Create(ctx, &spoofExecution))
	spoofCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: fixture.project.ID, OriginTask: true})
	spoofCtx = withAutomationExecution(spoofCtx, spoofed.ID, spoofExecution.ID)
	spoofRuntime := publishSvc.AutomationGitHubRuntimeTools(spoofCtx, *spoofed, gitHubIssueRuntimeToolDefs(true))
	_, handled, isErr, err = spoofRuntime.Executor(spoofCtx, "github_open_pull_request", []byte(fmt.Sprintf(`{"task_id":"current","issue_number":42,"pr_title":"PR","pr_body":%q}`, prBody)))
	require.True(t, handled)
	require.True(t, isErr)
	require.ErrorContains(t, err, "trusted Automation task provenance")
	spoofRecord, err := opts.TaskPullRequestRepo.GetByTaskID(ctx, spoofed.ID)
	require.NoError(t, err)
	require.Nil(t, spoofRecord)
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
	assertAutomationGitHubIssueProjectionForDefinition(t, fixture, fixture.definition, issueNumber, activityKey)
}

func assertAutomationGitHubIssueProjectionForDefinition(t *testing.T, fixture automationRuntimeFixture, definition *models.AutomationDefinition, issueNumber int, activityKey string) {
	t.Helper()
	resourceID := fmt.Sprintf("github:example/runtime:issue:%d", issueNumber)
	var workItems int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_work_items
		WHERE project_id = ? AND automation_id = ? AND work_item_key = ? AND kind = 'github_issue' AND status = 'waiting'`,
		fixture.project.ID, definition.Automation.ID, resourceID).Scan(&workItems))
	require.Equal(t, 1, workItems, "the created issue must have one waiting Automation work item")

	var activities int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activities
		WHERE project_id = ? AND automation_id = ? AND version_id = ? AND activity_key = ?
			AND activity_type = 'create_github_issue' AND status = 'completed' AND work_item_id IS NOT NULL`,
		fixture.project.ID, definition.Automation.ID, definition.Version.ID, activityKey).Scan(&activities))
	require.Equal(t, 1, activities, "the original issue activity must be completed against the work item")
	var totalActivities int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activities
		WHERE project_id = ? AND automation_id = ? AND version_id = ? AND activity_type = 'create_github_issue'`,
		fixture.project.ID, definition.Automation.ID, definition.Version.ID).Scan(&totalActivities))
	require.Equal(t, 1, totalActivities, "retries must not leave duplicate or pending issue activities")

	var resources int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activity_resources ar
		JOIN automation_activities a ON a.id = ar.activity_id
		WHERE a.project_id = ? AND a.automation_id = ? AND a.version_id = ? AND a.activity_key = ?
			AND ((ar.resource_type = 'github_issue' AND ar.resource_id = ?)
				OR ar.resource_type = 'task' OR ar.resource_type = 'execution')`, fixture.project.ID,
		definition.Automation.ID, definition.Version.ID, activityKey, resourceID).Scan(&resources))
	require.Equal(t, 3, resources, "the repaired activity must retain issue, task, and execution provenance")

	for edgeKey, state := range map[string]string{"bug_to_issue": "entered", "issue_to_assignment": "waiting"} {
		var transitions int
		require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_transitions tr
			JOIN automation_edges e ON e.id = tr.edge_id
			WHERE tr.project_id = ? AND tr.automation_id = ? AND tr.version_id = ? AND e.edge_key = ? AND tr.state = ?`,
			fixture.project.ID, definition.Automation.ID, definition.Version.ID, edgeKey, state).Scan(&transitions))
		require.Equal(t, 1, transitions, edgeKey+" must be recorded exactly once")
	}
	var waitingAssignment int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_work_item_positions p
		JOIN automation_nodes n ON n.id = p.node_id
		JOIN automation_work_items wi ON wi.id = p.work_item_id
		WHERE p.project_id = ? AND p.automation_id = ? AND p.version_id = ? AND n.node_key = 'assignment'
			AND p.state = 'waiting' AND wi.work_item_key = ?`, fixture.project.ID, definition.Automation.ID,
		definition.Version.ID, resourceID).Scan(&waitingAssignment))
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
	require.NoError(t, githubAuthRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "automation-bot"}))
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

func TestAutomationGitHubIssueCreationDeduplicatesStableFindingKeyAcrossRewordedTitles(t *testing.T) {
	var createCalls atomic.Int32
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
		},
		createIssueFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
			createCalls.Add(1)
			return &GitHubIssue{Number: 96, URL: "https://github.com/example/runtime/issues/96", Title: req.Title, State: "open"}, nil
		},
	}
	_, newCausalContext, createIssue := newAutomationGitHubIssueDedupHarness(t, provider)

	firstCtx := newCausalContext("stable-finding-first")
	firstInput := json.RawMessage(`{
		"title":"Request validation panics for empty headers",
		"body":"body",
		"idempotency_key":"bug:internal/http/handler.go:validate-request-headers"
	}`)
	first, err := createIssue(firstCtx, firstInput)
	require.NoError(t, err)
	require.Contains(t, first, `"Number":96`)

	second, err := createIssue(newCausalContext("stable-finding-reworded"), json.RawMessage(`{
		"title":"Empty request headers crash the validation handler",
		"body":"rewritten body",
		"idempotency_key":"bug:internal/http/handler.go:validate-request-headers"
	}`))
	require.NoError(t, err)
	require.Contains(t, second, `"reused":true`)
	require.Equal(t, int32(1), createCalls.Load(), "a reworded duplicate finding must reuse the locally claimed issue")
}

func TestAutomationGitHubIssueCreationRejectsCompletedClaimFromDifferentAutomation(t *testing.T) {
	var createCalls atomic.Int32
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
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
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
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
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
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
	require.ErrorContains(t, repairErr, "current active Automation graph")
}

func TestAutomationGitHubIssueCreationFailsClosedAfterAmbiguousProviderOutcome(t *testing.T) {
	var createCalls atomic.Int32
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
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
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
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
		githubCreateIssueRuntimeInput{Title: "Canceled successful issue", Body: "body", Labels: []string{"bug"}})

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
					return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
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
				githubCreateIssueRuntimeInput{Title: "Projection repair issue", Body: "body", Labels: []string{"bug"}})
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
				githubCreateIssueRuntimeInput{Title: "Projection repair issue", Body: "body", Labels: []string{"bug"}})
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

func TestAutomationGitHubIssueCreationRepairsPartialMultiBindingProjection(t *testing.T) {
	var createCalls atomic.Int32
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
		},
		createIssueFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
			createCalls.Add(1)
			return &GitHubIssue{Number: 96, URL: "https://github.com/example/runtime/issues/96", Title: req.Title, State: "open"}, nil
		},
	}
	fixture, _, createIssue := newAutomationGitHubIssueDedupHarness(t, provider)
	ctx := context.Background()
	secondSchedule := models.Schedule{TaskID: fixture.task.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatHours, RepeatInterval: 1, Enabled: true}
	require.NoError(t, fixture.schedRepo.Create(ctx, &secondSchedule))
	secondDefinition, _, err := NewAutomationRegistrationService(fixture.repo, NewAutomationAdapterRegistry()).Register(ctx, AutomationRegistrationRequest{
		ProjectID: fixture.project.ID, AdapterKey: AutomationAdapterGitHubSDLC, StableKey: "github-sdlc/partial-multi-binding",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "dev_inbox", ResourceType: "schedule", ResourceID: secondSchedule.ID},
			{NodeKey: "dev_inbox", ResourceType: "task", ResourceID: fixture.task.ID},
		},
	})
	require.NoError(t, err)

	definitions := []*models.AutomationDefinition{fixture.definition, secondDefinition}
	schedules := []models.Schedule{fixture.schedule, secondSchedule}
	bindings := make([]models.AutomationBinding, 0, len(definitions))
	for i, definition := range definitions {
		sourceNode := automationNodeByKey(t, definition, "bug_finder")
		var invocationID string
		require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `INSERT INTO automation_invocations
			(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status, started_at)
			VALUES (?, ?, ?, ?, 'schedule', ?, ?, 'running', CURRENT_TIMESTAMP) RETURNING id`, fixture.project.ID,
			definition.Automation.ID, definition.Version.ID, sourceNode.ID, schedules[i].ID, fmt.Sprintf("partial-multi-binding-%d", i)).Scan(&invocationID))
		bindings = append(bindings, models.AutomationBinding{AutomationID: definition.Automation.ID, VersionID: definition.Version.ID,
			InvocationID: invocationID, NodeID: sourceNode.ID})
	}
	execution := models.Execution{TaskID: fixture.task.ID, Status: models.ExecRunning, PromptSent: "partial multi-binding projection"}
	require.NoError(t, repository.NewExecutionRepo(fixture.repo.DB()).Create(ctx, &execution))
	causalCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: fixture.project.ID, Bindings: bindings})
	causalCtx = withAutomationExecution(causalCtx, fixture.task.ID, execution.ID)
	input := json.RawMessage(`{"title":"Partial multi-binding projection","body":"body"}`)
	activityKey := githubIssueCreationActivityKey(causalCtx, &GitHubRepoRef{FullName: "example/runtime"},
		githubCreateIssueRuntimeInput{Title: "Partial multi-binding projection", Body: "body", Labels: []string{"bug"}})

	_, err = fixture.repo.DB().Exec(fmt.Sprintf(`CREATE TRIGGER fail_second_automation_issue_projection
		BEFORE INSERT ON automation_work_items WHEN NEW.automation_id = '%s' BEGIN
			SELECT RAISE(FAIL, 'injected second binding projection failure');
		END`, secondDefinition.Automation.ID))
	require.NoError(t, err)
	_, firstErr := createIssue(causalCtx, input)
	require.ErrorContains(t, firstErr, "injected second binding projection failure")
	var firstProjectedItems, secondProjectedItems int
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_work_items WHERE automation_id = ?`,
		fixture.definition.Automation.ID).Scan(&firstProjectedItems))
	require.Equal(t, 1, firstProjectedItems, "the injected failure must happen after the first binding projects")
	require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_work_items WHERE automation_id = ?`,
		secondDefinition.Automation.ID).Scan(&secondProjectedItems))
	require.Zero(t, secondProjectedItems, "the injected failure must leave the second binding unprojected")
	_, err = fixture.repo.DB().Exec(`DROP TRIGGER fail_second_automation_issue_projection`)
	require.NoError(t, err)

	retryOutput, retryErr := createIssue(causalCtx, input)
	require.NoError(t, retryErr)
	require.Contains(t, retryOutput, `"Number":96`)
	require.Contains(t, retryOutput, `"reused":true`)
	assertAutomationGitHubIssueProjectionForDefinition(t, fixture, fixture.definition, 96, activityKey)
	assertAutomationGitHubIssueProjectionForDefinition(t, fixture, secondDefinition, 96, activityKey)
	require.Equal(t, int32(1), createCalls.Load(), "partial multi-binding repair must not mutate GitHub again")
}

func TestAutomationGitHubIssueCreationUsesLaunchAuthorizationAfterGraphReplacement(t *testing.T) {
	var gotCreateLabels []string
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
		},
		ensureIssueLabelsFn: func(_ context.Context, _ *GitHubRepoRef, labels []string) error {
			require.Equal(t, []string{"bug"}, labels)
			return nil
		},
		createIssueFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
			gotCreateLabels = append([]string(nil), req.Labels...)
			return &GitHubIssue{Number: 212, URL: "https://github.com/example/runtime/issues/212", Title: req.Title, State: "open", Labels: req.Labels}, nil
		},
	}
	fixture, newCausalContext, _ := newAutomationGitHubIssueDedupHarness(t, provider)
	ctx := newCausalContext("launch-safe-after-save")
	replaced := replaceAutomationGitHubIssueGraph(t, fixture)
	fixture.task.CreatedVia = repository.AutomationCompilerTaskCreatedVia(fixture.definition.Automation.ID, "bug_finder")
	_, err := fixture.repo.DB().ExecContext(context.Background(), `UPDATE tasks SET created_via = ? WHERE id = ?`, fixture.task.CreatedVia, fixture.task.ID)
	require.NoError(t, err)
	activityKey := githubIssueCreationActivityKey(ctx, &GitHubRepoRef{FullName: "example/runtime"}, githubCreateIssueRuntimeInput{
		Title: "Launch-safe bug", Body: "## Summary\nA user-visible bug.", Labels: []string{"bug"},
	})
	llmSvc := &LLMService{automationRepo: fixture.repo, githubIssueRuntime: provider, projectRepo: repository.NewProjectRepo(fixture.repo.DB()), taskRepo: fixture.taskRepo}
	runtime := llmSvc.AutomationGitHubRuntimeTools(ctx, fixture.task, gitHubIssueRuntimeToolDefs(true))
	require.NotNil(t, runtime)
	output, handled, isErr, err := runtime.Executor(ctx, "github_create_issue", json.RawMessage(`{"title":"Launch-safe bug","body":"## Summary\nA user-visible bug."}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isErr)
	require.Contains(t, output, `"Number":212`)
	require.Equal(t, []string{"bug"}, gotCreateLabels)
	assertAutomationGitHubIssueProjectionForDefinition(t, fixture, replaced, 212, activityKey)
}

func TestMaintainedGitHubSDLCAppliesFinderCategoryLabels(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterGitHubSDLC)
	ctx := context.Background()
	tests := map[string][]string{
		"vision_suggestions":  {"feature", "suggestion"},
		"bug_finder":          {"bug"},
		"optimization_finder": {"performance"},
		"redundancy_finder":   {"duplication"},
	}
	for nodeKey, expected := range tests {
		t.Run(nodeKey, func(t *testing.T) {
			node := automationNodeByKey(t, fixture.definition, nodeKey)
			binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, NodeID: node.ID}
			boundCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}})
			req := &githubCreateIssueRuntimeInput{Title: "Readable finding", Body: "Readable body"}
			authorized, err := applyAutomationGitHubIssueConfiguration(boundCtx, githubIssueRuntimeOptions{ProjectID: fixture.project.ID, AutomationRepo: fixture.repo}, req)
			require.NoError(t, err)
			require.True(t, authorized)
			require.Equal(t, expected, req.Labels, "maintained finder category must remain visible on the GitHub issue")
		})
	}
}

func TestMaintainedGitHubSDLCCategoryLabelsAreProvisionedAndVerified(t *testing.T) {
	tests := []struct {
		name               string
		ensureErr          error
		returnedLabels     []string
		wantErr            string
		wantCreateCalls    int32
		wantProjectionRows int
	}{
		{name: "required label is visible", returnedLabels: []string{"bug"}, wantCreateCalls: 1, wantProjectionRows: 1},
		{name: "provisioning failure prevents creation", ensureErr: errors.New("label permission denied"), wantErr: "ensuring GitHub issue labels", wantCreateCalls: 0},
		{name: "provider omits required label", returnedLabels: []string{}, wantErr: "missing required category labels", wantCreateCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newAutomationRuntimeFixture(t, AutomationAdapterGitHubSDLC)
			ctx := context.Background()
			projectRepo := repository.NewProjectRepo(fixture.repo.DB())
			fixture.project.RepoURL = "https://github.com/example/runtime.git"
			require.NoError(t, projectRepo.Update(ctx, &fixture.project))
			bugFinder := automationNodeByKey(t, fixture.definition, "bug_finder")
			var invocationID string
			require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `INSERT INTO automation_invocations
				(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id,
				 occurrence_key, status, started_at) VALUES (?, ?, ?, ?, 'manual', ?, ?, 'running', CURRENT_TIMESTAMP)
				 RETURNING id`, fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID,
				bugFinder.ID, fixture.schedule.ID, "labels:"+tt.name).Scan(&invocationID))
			execution := models.Execution{TaskID: fixture.task.ID, Status: models.ExecRunning, PromptSent: "category label test"}
			require.NoError(t, repository.NewExecutionRepo(fixture.repo.DB()).Create(ctx, &execution))
			binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID,
				InvocationID: invocationID, NodeID: bugFinder.ID}
			boundCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}})
			boundCtx = withAutomationExecution(boundCtx, fixture.task.ID, execution.ID)

			var ensureCalls, createCalls atomic.Int32
			provider := &fakeGitHubIssueRuntimeProvider{
				resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
					return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
				},
				ensureIssueLabelsFn: func(_ context.Context, _ *GitHubRepoRef, labels []string) error {
					ensureCalls.Add(1)
					require.Equal(t, []string{"bug"}, labels)
					return tt.ensureErr
				},
				createIssueFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
					createCalls.Add(1)
					require.Equal(t, []string{"bug"}, req.Labels)
					return &GitHubIssue{Number: 188, URL: "https://github.com/example/runtime/issues/188", Title: req.Title,
						State: "open", Labels: tt.returnedLabels}, nil
				},
			}
			handlers := buildGitHubIssueRuntimeHandlers(githubIssueRuntimeOptions{ProjectID: fixture.project.ID,
				ProjectRepo: projectRepo, TaskRepo: fixture.taskRepo, AutomationRepo: fixture.repo, GitHub: provider})
			output, err := handlers["github_create_issue"](boundCtx, json.RawMessage(`{"title":"Readable bug finding","body":"## Summary\\nA user-visible problem."}`))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Empty(t, output)
			} else {
				require.NoError(t, err)
				require.Contains(t, output, `"Number":188`)
			}
			require.Equal(t, int32(1), ensureCalls.Load())
			require.Equal(t, tt.wantCreateCalls, createCalls.Load())
			var projectionRows int
			require.NoError(t, fixture.repo.DB().QueryRow(`SELECT COUNT(*) FROM automation_activities
				WHERE project_id = ? AND automation_id = ? AND activity_type = 'create_github_issue' AND status = 'completed'`,
				fixture.project.ID, fixture.definition.Automation.ID).Scan(&projectionRows))
			require.Equal(t, tt.wantProjectionRows, projectionRows)
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
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
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
	var createCalls, getIssueCalls, addLabelCalls atomic.Int32
	var issueHasBugLabel, denyLabelRepair, omitRepairedLabel atomic.Bool
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) {
			return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
		},
		createIssueFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
			if createCalls.Add(1) == 1 {
				close(createStarted)
			}
			<-releaseCreate
			issueHasBugLabel.Store(true)
			return &GitHubIssue{Number: 88, URL: "https://github.com/example/runtime/issues/88", Title: req.Title, State: "open", Labels: req.Labels}, nil
		},
		getIssueFn: func(_ context.Context, _ *GitHubRepoRef, issueNumber int) (*GitHubIssue, error) {
			getIssueCalls.Add(1)
			require.Equal(t, 88, issueNumber)
			labels := []string{}
			if issueHasBugLabel.Load() {
				labels = []string{"bug"}
			}
			return &GitHubIssue{Number: 88, URL: "https://github.com/example/runtime/issues/88", Title: "Concurrent duplicate", State: "open", Labels: labels}, nil
		},
		addLabelsFn: func(_ context.Context, _ *GitHubRepoRef, issueNumber int, labels []string) error {
			addLabelCalls.Add(1)
			require.Equal(t, 88, issueNumber)
			require.Equal(t, []string{"bug"}, labels)
			if denyLabelRepair.Load() {
				return errors.New("label repair denied")
			}
			if !omitRepairedLabel.Load() {
				issueHasBugLabel.Store(true)
			}
			return nil
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

	issueHasBugLabel.Store(false) // Simulate the category label being removed after the first successful creation.
	duplicateOutput, duplicateErr := handlers["github_create_issue"](secondCtx, json.RawMessage(`{"title":"CONCURRENT DUPLICATE","body":"retry body"}`))
	require.NoError(t, duplicateErr)
	require.Contains(t, duplicateOutput, `"Number":88`)
	require.Contains(t, duplicateOutput, `"URL":"https://github.com/example/runtime/issues/88"`)
	require.Contains(t, duplicateOutput, `"Labels":["bug"]`)
	require.Contains(t, duplicateOutput, `"reused":true`)
	require.Equal(t, int32(1), createCalls.Load(), "local deduplication must reuse the canonical issue instead of creating another")
	require.Equal(t, int32(1), addLabelCalls.Load(), "reuse must restore a missing required category label")
	require.Equal(t, int32(2), getIssueCalls.Load(), "reuse must read before repair and refetch to confirm the label")

	issueHasBugLabel.Store(false)
	denyLabelRepair.Store(true)
	failedOutput, failedErr := handlers["github_create_issue"](newCausalContext("failed-label-repair"), input)
	require.ErrorContains(t, failedErr, "label repair denied")
	require.Empty(t, failedOutput, "unconfirmed labels must not report successful issue reuse")
	require.Equal(t, int32(1), createCalls.Load(), "failed label repair must not create another issue")
	require.Equal(t, int32(2), addLabelCalls.Load())
	require.Equal(t, int32(3), getIssueCalls.Load(), "failed repair stops before a confirmation fetch")

	denyLabelRepair.Store(false)
	omitRepairedLabel.Store(true)
	unconfirmedOutput, unconfirmedErr := handlers["github_create_issue"](newCausalContext("unconfirmed-label-repair"), input)
	require.ErrorContains(t, unconfirmedErr, "missing required category labels after repair")
	require.Empty(t, unconfirmedOutput, "a successful label API call is insufficient without confirmed issue labels")
	require.Equal(t, int32(1), createCalls.Load(), "unconfirmed label repair must not create another issue")
	require.Equal(t, int32(3), addLabelCalls.Load())
	require.Equal(t, int32(5), getIssueCalls.Load(), "unconfirmed repair must perform the post-mutation confirmation fetch")
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

func TestAutomationLiveTaskRetryReplacesEarlierFailedDispatchState(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	producer := automationNodeByKey(t, fixture.definition, "vision_suggestions")
	newInvocationBinding := func(occurrence string) models.AutomationBinding {
		t.Helper()
		var invocationID string
		require.NoError(t, fixture.repo.DB().QueryRowContext(ctx, `INSERT INTO automation_invocations
			(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status, started_at)
			VALUES (?, ?, ?, ?, 'schedule', ?, ?, 'running', CURRENT_TIMESTAMP) RETURNING id`, fixture.project.ID,
			fixture.definition.Automation.ID, fixture.definition.Version.ID, producer.ID, fixture.schedule.ID, occurrence).Scan(&invocationID))
		return models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID,
			InvocationID: invocationID, NodeID: producer.ID}
	}
	failedBinding := newInvocationBinding("retry:failed")
	const (
		failedActivityID    = "ffffffffffffffffffffffffffffffff"
		completedActivityID = "00000000000000000000000000000000"
	)
	sameStartedAt := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")
	_, err := fixture.repo.DB().ExecContext(ctx, `INSERT INTO automation_activities
		(id, project_id, automation_id, version_id, node_id, invocation_id, activity_key, activity_type, status, started_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, 'retry:failed', 'task_execution', 'failed', ?, ?)`, failedActivityID,
		fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID, producer.ID,
		failedBinding.InvocationID, sameStartedAt, sameStartedAt)
	require.NoError(t, err)
	_, err = fixture.repo.DB().ExecContext(ctx, `INSERT INTO automation_activity_resources
		(activity_id, resource_type, resource_id, relation) VALUES (?, 'task', ?, 'subject')`, failedActivityID, fixture.task.ID)
	require.NoError(t, err)

	completedBinding := newInvocationBinding("retry:completed")
	_, err = fixture.repo.DB().ExecContext(ctx, `INSERT INTO automation_activities
		(id, project_id, automation_id, version_id, node_id, invocation_id, activity_key, activity_type, status, started_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, 'retry:completed', 'task_execution', 'completed', ?, ?)`, completedActivityID,
		fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID, producer.ID,
		completedBinding.InvocationID, sameStartedAt, sameStartedAt)
	require.NoError(t, err)
	_, err = fixture.repo.DB().ExecContext(ctx, `INSERT INTO automation_activity_resources
		(activity_id, resource_type, resource_id, relation) VALUES (?, 'task', ?, 'subject')`, completedActivityID, fixture.task.ID)
	require.NoError(t, err)

	graph, err := NewAutomationGraphService(fixture.repo).GetLive(ctx, fixture.project.ID, fixture.definition.Automation.ID, time.Now())
	require.NoError(t, err)
	for _, node := range graph.Nodes {
		if node.ID != producer.ID {
			continue
		}
		require.Zero(t, node.Counts.Failed, "a successful retry of the same task must replace its earlier failed dispatch state")
		require.Equal(t, 1, node.Counts.CompletedRecently)
		require.Equal(t, "recently_completed", node.DisplayState)
	}

	cards, err := NewAutomationGraphService(fixture.repo).List(ctx, fixture.project.ID)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	require.Zero(t, cards[0].Counts.Failed, "portfolio state must use the latest dispatch for the same task")
	require.Equal(t, 1, cards[0].Counts.CompletedRecently)
}

func TestAutomationLiveWorkItemSuccessReplacesEarlierFailedActivityState(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterGitHubSDLC)
	ctx := context.Background()
	implementation := automationNodeByKey(t, fixture.definition, "implementation")
	workItemID := "feedfacefeedfacefeedfacefeedface"
	failedActivityID := "feedfacefeedfacefeedfacefeedfac1"
	completedActivityID := "feedfacefeedfacefeedfacefeedfac2"
	completedAt := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err := fixture.repo.DB().ExecContext(ctx, `INSERT INTO automation_work_items
		(id, project_id, automation_id, origin_version_id, work_item_key, kind, title, status)
		VALUES (?, ?, ?, ?, 'github:example/repo:issue:99', 'github_issue', 'Issue 99', 'completed')`, workItemID,
		fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID)
	require.NoError(t, err)
	_, err = fixture.repo.DB().ExecContext(ctx, `INSERT INTO automation_activities
		(id, project_id, automation_id, version_id, node_id, work_item_id, activity_key, activity_type, status, started_at, completed_at, error_message)
		VALUES (?, ?, ?, ?, ?, ?, 'issue:99:failed', 'task_execution', 'failed', ?, ?, 'oauth expired')`, failedActivityID,
		fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID, implementation.ID, workItemID, completedAt, completedAt)
	require.NoError(t, err)
	_, err = fixture.repo.DB().ExecContext(ctx, `INSERT INTO automation_activities
		(id, project_id, automation_id, version_id, node_id, work_item_id, activity_key, activity_type, status, started_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, 'issue:99:completed', 'implementation_task', 'completed', ?, ?)`, completedActivityID,
		fixture.project.ID, fixture.definition.Automation.ID, fixture.definition.Version.ID, implementation.ID, workItemID, completedAt, completedAt)
	require.NoError(t, err)

	graph, err := NewAutomationGraphService(fixture.repo).GetLive(ctx, fixture.project.ID, fixture.definition.Automation.ID, time.Now())
	require.NoError(t, err)
	for _, node := range graph.Nodes {
		if node.ID != implementation.ID {
			continue
		}
		require.Zero(t, node.Counts.Failed, "a later successful work-item activity must replace the earlier failed activity for the same node")
		require.Equal(t, 1, node.Counts.CompletedRecently)
		require.Equal(t, "recently_completed", node.DisplayState)
	}
	cards, err := NewAutomationGraphService(fixture.repo).List(ctx, fixture.project.ID)
	require.NoError(t, err)
	require.Len(t, cards, 1)
	require.Zero(t, cards[0].Counts.Failed, "portfolio counters must use the latest work-item activity state")
	require.Equal(t, 1, cards[0].Counts.CompletedRecently)
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

func TestAutomationLiveDisplayStateShowsRunningWhenMixedWithWaiting(t *testing.T) {
	fixture := newAutomationRuntimeFixture(t, AutomationAdapterNativeSDLC)
	ctx := context.Background()
	approval := automationNodeByKey(t, fixture.definition, "approval")
	binding := models.AutomationBinding{AutomationID: fixture.definition.Automation.ID, VersionID: fixture.definition.Version.ID, NodeID: approval.ID}
	_, _, err := fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "mixed-display:waiting", ActivityKey: "mixed-display:waiting", ActivityType: "test", ActivityStatus: models.AutomationActivityRunning,
		EventKey: "mixed-display:waiting", ToNodeID: approval.ID, Transition: models.AutomationTransitionWaiting,
	})
	require.NoError(t, err)
	_, _, err = fixture.repo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "mixed-display:running", ActivityKey: "mixed-display:running", ActivityType: "test", ActivityStatus: models.AutomationActivityRunning,
		EventKey: "mixed-display:running", ToNodeID: approval.ID, Transition: models.AutomationTransitionEntered,
	})
	require.NoError(t, err)

	graph, err := NewAutomationGraphService(fixture.repo).GetLive(ctx, fixture.project.ID, fixture.definition.Automation.ID, time.Now())
	require.NoError(t, err)
	for _, node := range graph.Nodes {
		if node.ID != approval.ID {
			continue
		}
		require.Equal(t, 1, node.Counts.Running)
		require.Equal(t, 1, node.Counts.Waiting)
		require.Equal(t, "running", node.DisplayState)
		return
	}
	t.Fatalf("approval node not found")
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
	return &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
}

func (f *fakeAutomationPullRequestProvider) GlobalAPIEndpoint(context.Context) string { return "" }

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
	fixture.project.RepoPath = "/projects/example-runtime"
	require.NoError(t, projectRepo.Update(ctx, &fixture.project))
	provider.err = nil
	_, err = external.Refresh(ctx, fixture.project.ID, fixture.definition.Automation.ID, now)
	require.NoError(t, err)
	require.Empty(t, provider.resolvedURL)
	require.Equal(t, fixture.project.RepoPath, provider.resolvedPath, "Automation external refresh must fall back to the project's local Git remote")
}

func TestAutomationReconcilerRefreshesStaleExternalPullRequestStateInBackground(t *testing.T) {
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
		Context: contextValue, Binding: binding, WorkItemKey: "github:example/runtime:issue:99",
		ActivityKey: "github:example/runtime:pull:9:open", ActivityType: "open_pull_request", ActivityStatus: models.AutomationActivityCompleted,
		Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: fixture.task.ID}, {ResourceType: "pull_request", ResourceID: "github:example/runtime:pull:9"}},
		EventKey:  "github:example/runtime:pull:9:review", FromNodeID: openPR.ID, ToNodeID: review.ID, Transition: models.AutomationTransitionWaiting,
	})
	require.NoError(t, err)

	pullRequests := repository.NewTaskPullRequestRepo(fixture.repo.DB())
	record := models.TaskPullRequest{TaskID: fixture.task.ID, PRNumber: 9, PRURL: "https://github.com/example/runtime/pull/9", PRState: "open"}
	require.NoError(t, pullRequests.Upsert(ctx, &record))
	now := time.Now().UTC().Truncate(time.Second)
	_, err = fixture.repo.DB().ExecContext(ctx, `UPDATE task_pull_requests SET updated_at = datetime(?) WHERE id = ?`,
		now.Add(-time.Hour).Format("2006-01-02 15:04:05"), record.ID)
	require.NoError(t, err)

	stale, err := fixture.repo.ListAutomationsWithStaleExternalPullRequests(ctx, now.Add(-5*time.Minute), 100)
	require.NoError(t, err)
	require.Len(t, stale, 1, "an Automation with a stale tracked pull request must be discovered for background refresh")
	require.Equal(t, [2]string{fixture.project.ID, fixture.definition.Automation.ID}, stale[0])

	provider := &fakeAutomationPullRequestProvider{pull: GitHubPullRequest{Number: 9, URL: record.PRURL, State: "closed", Merged: true}}
	external := NewAutomationExternalStateService(fixture.repo, pullRequests, projectRepo, provider)
	reconciler := NewAutomationReconciler(fixture.repo, repository.NewExecutionRepo(fixture.repo.DB()), NewWorkerService(nil, 1, nil))
	reconciler.SetAutomationExternalStateService(external)
	require.NoError(t, reconciler.ReconcileOnce(ctx))

	require.Equal(t, 1, provider.calls, "the reconciler must refresh stale tracked pull request state automatically, without a manual click")
	stored, err := pullRequests.GetByTaskID(ctx, fixture.task.ID)
	require.NoError(t, err)
	require.Equal(t, "merged", stored.PRState)

	stale, err = fixture.repo.ListAutomationsWithStaleExternalPullRequests(ctx, now.Add(-5*time.Minute), 100)
	require.NoError(t, err)
	require.Empty(t, stale, "a background refresh must clear the automation from the stale list")

	require.NoError(t, reconciler.ReconcileOnce(ctx))
	require.Equal(t, 1, provider.calls, "a background refresh must not repeatedly hit GitHub once the state is no longer stale")
}

func TestAutomationReconcilerSkipsExternalStateRefreshWhenLiveViewNotRecentlyOpen(t *testing.T) {
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
		Context: contextValue, Binding: binding, WorkItemKey: "github:example/runtime:issue:99",
		ActivityKey: "github:example/runtime:pull:9:open", ActivityType: "open_pull_request", ActivityStatus: models.AutomationActivityCompleted,
		Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: fixture.task.ID}, {ResourceType: "pull_request", ResourceID: "github:example/runtime:pull:9"}},
		EventKey:  "github:example/runtime:pull:9:review", FromNodeID: openPR.ID, ToNodeID: review.ID, Transition: models.AutomationTransitionWaiting,
	})
	require.NoError(t, err)

	pullRequests := repository.NewTaskPullRequestRepo(fixture.repo.DB())
	record := models.TaskPullRequest{TaskID: fixture.task.ID, PRNumber: 9, PRURL: "https://github.com/example/runtime/pull/9", PRState: "open"}
	require.NoError(t, pullRequests.Upsert(ctx, &record))
	now := time.Now().UTC().Truncate(time.Second)
	_, err = fixture.repo.DB().ExecContext(ctx, `UPDATE task_pull_requests SET updated_at = datetime(?) WHERE id = ?`,
		now.Add(-time.Hour).Format("2006-01-02 15:04:05"), record.ID)
	require.NoError(t, err)

	provider := &fakeAutomationPullRequestProvider{pull: GitHubPullRequest{Number: 9, URL: record.PRURL, State: "closed", Merged: true}}
	external := NewAutomationExternalStateService(fixture.repo, pullRequests, projectRepo, provider)
	tracker := NewAutomationLiveViewTracker()
	reconciler := NewAutomationReconciler(fixture.repo, repository.NewExecutionRepo(fixture.repo.DB()), NewWorkerService(nil, 1, nil))
	reconciler.SetAutomationExternalStateService(external)
	reconciler.SetAutomationLiveViewTracker(tracker)

	require.NoError(t, reconciler.ReconcileOnce(ctx))
	require.Zero(t, provider.calls, "background refresh must not call GitHub for an Automation whose Live page has not been opened")

	tracker.MarkViewed(fixture.project.ID, fixture.definition.Automation.ID)
	require.NoError(t, reconciler.ReconcileOnce(ctx))
	require.Equal(t, 1, provider.calls, "background refresh must call GitHub once the Automation's Live page has been recently viewed")
}

func TestAutomationRuntimeNativeNotificationRequiresProducerActionEdge(t *testing.T) {
	h := newAutomationSaveHarness(t, "Native notification producer authorization")
	ctx := context.Background()
	candidate, err := h.drafts.TemplateCandidate(AutomationAdapterNativeSDLC)
	require.NoError(t, err)
	candidate = automationCandidateWithoutEdge(candidate, "vision_to_notification")
	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{
		ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: candidate,
	})
	require.NoError(t, err)

	alertRepo := repository.NewAlertRepo(h.db)
	alertRepo.SetAutomationRepo(h.automationRepo)
	alertSvc := NewAlertService(alertRepo, nil)
	create := func(nodeKey, key string) (*models.Alert, error) {
		node := automationNodeByKey(t, saved.Definition, nodeKey)
		boundCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{{
			AutomationID: saved.Definition.Automation.ID, VersionID: saved.Definition.Version.ID, NodeID: node.ID,
		}}})
		return alertSvc.CreateActionable(boundCtx, &models.Alert{
			ProjectID: h.project.ID, Type: "suggestion", Title: nodeKey, IdempotencyKey: key,
		})
	}

	_, err = create("vision_suggestions", "disconnected-native-producer")
	require.ErrorContains(t, err, "not authorized")
	require.Zero(t, countRows(t, h.db, `SELECT COUNT(*) FROM alerts WHERE project_id = ? AND idempotency_key = ?`, h.project.ID, "disconnected-native-producer"))
	require.Zero(t, countRows(t, h.db, `SELECT COUNT(*) FROM automation_work_items WHERE automation_id = ?`, saved.Definition.Automation.ID))
	require.Zero(t, countRows(t, h.db, `SELECT COUNT(*) FROM automation_activities WHERE automation_id = ?`, saved.Definition.Automation.ID))
	require.Zero(t, countRows(t, h.db, `SELECT COUNT(*) FROM automation_transitions WHERE automation_id = ?`, saved.Definition.Automation.ID))

	alert, err := create("bug_finder", "connected-native-producer")
	require.NoError(t, err)
	require.NotNil(t, alert)
	require.Equal(t, 1, countRows(t, h.db, `SELECT COUNT(*) FROM alerts WHERE id = ?`, alert.ID))
	require.Equal(t, 1, countRows(t, h.db, `SELECT COUNT(*) FROM automation_work_items WHERE automation_id = ?`, saved.Definition.Automation.ID))
	require.Equal(t, 1, countRows(t, h.db, `SELECT COUNT(*) FROM automation_activities WHERE automation_id = ? AND activity_type = 'create_notification'`, saved.Definition.Automation.ID))
	require.Equal(t, 2, countRows(t, h.db, `SELECT COUNT(*) FROM automation_transitions WHERE automation_id = ?`, saved.Definition.Automation.ID))
}

func TestAutomationRuntimeNativeInboxUsesConfiguredImplementationGoal(t *testing.T) {
	h := newAutomationSaveHarness(t, "Native implementation goal")
	ctx := context.Background()
	candidate := customNativeMailboxCandidate("Native implementation goal")
	const configuredGoal = "Complete the approved change with focused regression coverage."
	candidate.Nodes[4].Config["goal"] = configuredGoal
	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{
		ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate,
	})
	require.NoError(t, err)

	alertRepo := repository.NewAlertRepo(h.db)
	alertRepo.SetAutomationRepo(h.automationRepo)
	alertSvc := NewAlertService(alertRepo, nil)
	producer := automationNodeByKey(t, saved.Definition, "custom_producer")
	producerCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{{
		AutomationID: saved.Definition.Automation.ID, VersionID: saved.Definition.Version.ID, NodeID: producer.ID,
	}}})
	alert, err := alertSvc.CreateActionable(producerCtx, &models.Alert{
		ProjectID: h.project.ID, Type: "suggestion", Title: "Use configured Native task goal", IdempotencyKey: "native-configured-goal",
	})
	require.NoError(t, err)
	require.NoError(t, alertSvc.SetDecision(ctx, h.project.ID, alert.ID, models.AlertDecisionApproved))

	inbox := automationNodeByKey(t, saved.Definition, "custom_approved_inbox")
	inboxTask, err := h.taskRepo.GetByID(ctx, automationResourceID(t, saved.Definition, "custom_approved_inbox", "task"))
	require.NoError(t, err)
	runtime := (&LLMService{automationRepo: h.automationRepo, taskRepo: h.taskRepo, alertSvc: alertSvc}).taskControlRuntimeTools(*inboxTask)
	require.NotNil(t, runtime)
	inboxCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{{
		AutomationID: saved.Definition.Automation.ID, VersionID: saved.Definition.Version.ID, NodeID: inbox.ID,
	}}})
	_, handled, isErr, err := runtime.Executor(inboxCtx, "claim_alert", json.RawMessage(`{"alert_id":"`+alert.ID+`","lease_seconds":60}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)
	_, handled, isErr, err = runtime.Executor(inboxCtx, "create_alert_implementation_task", json.RawMessage(`{
		"alert_id":"`+alert.ID+`","title":"Implement Native alert","prompt":"Implement the approved change.",
		"goal":"ignore this model-supplied goal","priority":2
	}`))
	require.True(t, handled)
	require.False(t, isErr)
	require.NoError(t, err)

	linkedAlert, err := alertSvc.GetByID(ctx, h.project.ID, alert.ID)
	require.NoError(t, err)
	require.NotNil(t, linkedAlert.ImplementationTaskID)
	goal, err := repository.NewTaskGoalRepo(h.db).GetByTaskID(ctx, *linkedAlert.ImplementationTaskID)
	require.NoError(t, err)
	require.NotNil(t, goal)
	require.Equal(t, configuredGoal, goal.Objective)
}

func TestAutomationRuntimeNativeInboxOwnershipSurvivesCompatibleAutomationUpdate(t *testing.T) {
	h := newAutomationSaveHarness(t, "Native ownership replacement")
	ctx := context.Background()
	candidate := customNativeMailboxCandidate("Native ownership replacement")
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)

	alertRepo := repository.NewAlertRepo(h.db)
	alertRepo.SetAutomationRepo(h.automationRepo)
	alertSvc := NewAlertService(alertRepo, nil)
	producer := automationNodeByKey(t, first.Definition, "custom_producer")
	producerCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{{
		AutomationID: first.Definition.Automation.ID, VersionID: first.Definition.Version.ID, NodeID: producer.ID,
	}}})
	alert, err := alertSvc.CreateActionable(producerCtx, &models.Alert{ProjectID: h.project.ID, Type: "suggestion", Title: "Survive Native edit", IdempotencyKey: "survive-native-edit"})
	require.NoError(t, err)
	var producerKey, actionKey, gateKey, mailboxKey string
	require.NoError(t, h.db.QueryRow(`SELECT producer_node_key, action_node_key, gate_node_key, mailbox_node_key
		FROM automation_artifact_mailbox_owners WHERE project_id = ? AND automation_id = ? AND artifact_type = 'alert' AND artifact_id = ?`,
		h.project.ID, first.Definition.Automation.ID, alert.ID).Scan(&producerKey, &actionKey, &gateKey, &mailboxKey))
	require.Empty(t, producerKey)
	require.Empty(t, actionKey)
	require.Empty(t, gateKey)
	require.Empty(t, mailboxKey)
	require.NoError(t, alertSvc.SetDecision(ctx, h.project.ID, alert.ID, models.AlertDecisionApproved))

	candidate.Description = "Updated configuration with the same logical mailbox."
	second, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.NotEqual(t, first.Definition.Version.ID, second.Definition.Version.ID)
	require.Equal(t, 0, countRows(t, h.db, `SELECT COUNT(*) FROM automation_versions WHERE id = ?`, first.Definition.Version.ID), "Save must still delete the replaced graph revision")

	inbox := automationNodeByKey(t, second.Definition, "custom_approved_inbox")
	inboxCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{{
		AutomationID: second.Definition.Automation.ID, VersionID: second.Definition.Version.ID, NodeID: inbox.ID,
	}}})
	handlers := BuildAlertRuntimeActionHandlers(AlertRuntimeOptions{ProjectID: h.project.ID, CallerTaskID: "updated-native-inbox", AlertSvc: alertSvc, TaskRepo: h.taskRepo})
	output, err := handlers["list_alerts"](inboxCtx, json.RawMessage(`{"decision_state":"approved","processing_state":"unclaimed"}`))
	require.NoError(t, err)
	require.Contains(t, output, alert.ID, "the same logical inbox must retain approved work across Save replacement")
	_, err = handlers["claim_alert"](inboxCtx, json.RawMessage(`{"alert_id":"`+alert.ID+`","lease_seconds":60}`))
	require.NoError(t, err, "the updated logical inbox must be allowed to process its retained alert")
	_, err = handlers["create_alert_implementation_task"](inboxCtx, json.RawMessage(`{
		"alert_id":"`+alert.ID+`","title":"Implement retained Native alert","prompt":"Implement the approved change.","priority":2
	}`))
	require.NoError(t, err)
	linkedAlert, err := alertSvc.GetByID(ctx, h.project.ID, alert.ID)
	require.NoError(t, err)
	require.NotNil(t, linkedAlert.ImplementationTaskID)
	implementationContext, err := h.automationRepo.ContextForTask(ctx, h.project.ID, *linkedAlert.ImplementationTaskID)
	require.NoError(t, err)
	require.Len(t, implementationContext.Bindings, 1)
	require.Equal(t, second.Definition.Version.ID, implementationContext.Bindings[0].VersionID, "retained work must project onto the current graph revision")
}

func customGitHubMailboxCandidate(name string) models.AutomationDraftCandidate {
	return models.AutomationDraftCandidate{SchemaVersion: 1, Name: name, AutomationType: "custom", AdapterKey: AutomationAdapterCustom, Nodes: []models.AutomationDraftNode{
		{Key: "producer_schedule", Name: "Daily suggestions", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Run the scheduled work.", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "producer", Name: "Find improvements", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Find one focused improvement.", "category": "backlog", "priority": 2}},
		{Key: "issue", Name: "Create issue", Type: models.AutomationNodeAction, Role: "create_github_issue", Config: map[string]any{"instructions": "Open one reviewable suggestion issue.", "labels": []any{"suggestion"}}},
		{Key: "assignment", Name: "Human assignment", Type: models.AutomationNodeHumanGate, Role: "github_assignment", Config: map[string]any{"approval_method": "github_assignment"}},
		{Key: "inbox_schedule", Name: "Hourly inbox", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Run the scheduled work.", "category": "scheduled", "priority": 2, "run_at": "09:15", "repeat_type": "hours", "repeat_interval": 1, "enabled": true}},
		{Key: "inbox", Name: "Process assigned issues", Type: models.AutomationNodeAgentTask, Role: "github_inbox", Config: map[string]any{"prompt": "Process newly assigned issues.", "category": "backlog", "priority": 3}},
		{Key: "implementation", Name: "Implementation", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Implement the accepted issue and run relevant validation.", "category": "active", "priority": 3}},
		{Key: "open_pr", Name: "Open pull request", Type: models.AutomationNodeAction, Role: "open_pull_request", Config: map[string]any{"instructions": "Open a reviewable pull request linked to the source issue.", "base": "main", "draft": false}},
		{Key: "review", Name: "Human review", Type: models.AutomationNodeHumanGate, Role: "pull_request_review", Config: map[string]any{"approval_method": "pull_request_review"}},
		{Key: "complete", Name: "Merged", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
	}, Edges: []models.AutomationDraftEdge{
		{Key: "producer_schedule_to_producer", From: "producer_schedule", To: "producer", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "producer_to_issue", From: "producer", To: "issue", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "issue_to_assignment", From: "issue", To: "assignment", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "inbox_schedule_to_inbox", From: "inbox_schedule", To: "inbox", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "assignment_to_inbox", From: "assignment", To: "inbox", FromPort: "right", ToPort: "left", Label: "assigned", Condition: map[string]any{"state": "assigned"}},
		{Key: "inbox_to_implementation", From: "inbox", To: "implementation", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "implementation_to_pr", From: "implementation", To: "open_pr", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "pr_to_review", From: "open_pr", To: "review", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "review_to_complete", From: "review", To: "complete", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
	}}
}

func TestAutomationRuntimeGitHubInboxOwnershipSurvivesCompatibleAutomationUpdate(t *testing.T) {
	h := newAutomationSaveHarness(t, "GitHub ownership replacement")
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(h.db)
	h.project.RepoURL = "https://github.com/example/runtime.git"
	require.NoError(t, projectRepo.Update(ctx, &h.project))
	settingsRepo := repository.NewSettingsRepo(h.db)
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT))
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingPAT, "test-token"))
	githubAuthRepo := repository.NewGitHubAuthRepo(h.db)
	require.NoError(t, githubAuthRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "automation-bot"}))
	h.compiler.validator.SetCapabilityDependencies(projectRepo, settingsRepo, githubAuthRepo)
	candidate := customGitHubMailboxCandidate("GitHub ownership replacement")
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)

	repoRef := &GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}
	issue := GitHubIssue{Number: 42, URL: "https://github.com/example/runtime/issues/42", Title: "Survive GitHub edit", State: "open"}
	producer := automationNodeByKey(t, first.Definition, "producer")
	producerCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{{
		AutomationID: first.Definition.Automation.ID, VersionID: first.Definition.Version.ID, NodeID: producer.ID,
	}}})
	opts := githubIssueRuntimeOptions{ProjectID: h.project.ID, AutomationRepo: h.automationRepo}
	recorded, err := recordGitHubIssueCreated(producerCtx, opts, repoRef, &issue, "survive-github-edit")
	require.NoError(t, err)
	require.Equal(t, 1, recorded)

	candidate.Description = "Updated configuration with the same logical mailbox."
	second, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.NotEqual(t, first.Definition.Version.ID, second.Definition.Version.ID)
	require.Equal(t, 0, countRows(t, h.db, `SELECT COUNT(*) FROM automation_versions WHERE id = ?`, first.Definition.Version.ID), "Save must still delete the replaced graph revision")

	inbox := automationNodeByKey(t, second.Definition, "inbox")
	inboxCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{{
		AutomationID: second.Definition.Automation.ID, VersionID: second.Definition.Version.ID, NodeID: inbox.ID,
	}}})
	filtered, err := filterGitHubAssignedIssuesForAutomationInbox(inboxCtx, opts, repoRef, []GitHubIssue{issue})
	require.NoError(t, err)
	require.Len(t, filtered, 1, "the same logical inbox must retain assigned work across Save replacement")
	require.Equal(t, issue.Number, filtered[0].Number)
	require.NoError(t, recordGitHubAssignedIssues(inboxCtx, opts, repoRef, filtered))
	issueContext, err := h.automationRepo.BindingsForWorkItemKey(ctx, h.project.ID, githubIssueResourceID(repoRef, issue.Number))
	require.NoError(t, err)
	require.Len(t, issueContext.Bindings, 1)
	require.Equal(t, second.Definition.Version.ID, issueContext.Bindings[0].VersionID, "retained issue work must project onto the current graph revision")
}

func TestAutomationRuntimeNativeInboxOwnershipMovesToCurrentRenamedMailbox(t *testing.T) {
	h := newAutomationSaveHarness(t, "Renamed Native mailbox")
	ctx := context.Background()
	candidate := customNativeMailboxCandidate("Renamed Native mailbox")
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)

	alertRepo := repository.NewAlertRepo(h.db)
	alertRepo.SetAutomationRepo(h.automationRepo)
	alertSvc := NewAlertService(alertRepo, nil)
	producer := automationNodeByKey(t, first.Definition, "custom_producer")
	producerCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{{
		AutomationID: first.Definition.Automation.ID, VersionID: first.Definition.Version.ID, NodeID: producer.ID,
	}}})
	alert, err := alertSvc.CreateActionable(producerCtx, &models.Alert{ProjectID: h.project.ID, Type: "suggestion", Title: "Do not move", IdempotencyKey: "do-not-move"})
	require.NoError(t, err)
	require.NoError(t, alertSvc.SetDecision(ctx, h.project.ID, alert.ID, models.AlertDecisionApproved))

	for i := range candidate.Nodes {
		if candidate.Nodes[i].Key == "custom_approved_inbox" {
			candidate.Nodes[i].Key = "replacement_inbox"
			candidate.Nodes[i].Name = "Replacement approved inbox"
		}
	}
	for i := range candidate.Edges {
		if candidate.Edges[i].From == "custom_approved_inbox" {
			candidate.Edges[i].From = "replacement_inbox"
		}
		if candidate.Edges[i].To == "custom_approved_inbox" {
			candidate.Edges[i].To = "replacement_inbox"
		}
	}
	second, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	inbox := automationNodeByKey(t, second.Definition, "replacement_inbox")
	inboxCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{{
		AutomationID: second.Definition.Automation.ID, VersionID: second.Definition.Version.ID, NodeID: inbox.ID,
	}}})
	handlers := BuildAlertRuntimeActionHandlers(AlertRuntimeOptions{ProjectID: h.project.ID, CallerTaskID: "replacement-native-inbox", AlertSvc: alertSvc, TaskRepo: h.taskRepo})
	output, err := handlers["list_alerts"](inboxCtx, json.RawMessage(`{"decision_state":"approved","processing_state":"unclaimed"}`))
	require.NoError(t, err)
	require.Contains(t, output, alert.ID)
	_, err = handlers["claim_alert"](inboxCtx, json.RawMessage(`{"alert_id":"`+alert.ID+`","lease_seconds":60}`))
	require.NoError(t, err, "a current Native inbox in the same Automation must be allowed to process owned notifications after inbox replacement")
}

func TestAutomationRuntimeCustomNativeInboxRequiresSameAutomationOwnership(t *testing.T) {
	h := newAutomationSaveHarness(t, "Scoped Native runtime")
	ctx := context.Background()
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: customNativeMailboxCandidate("First Native mailbox")})
	require.NoError(t, err)
	second, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: customNativeMailboxCandidate("Second Native mailbox")})
	require.NoError(t, err)

	alertRepo := repository.NewAlertRepo(h.db)
	alertRepo.SetAutomationRepo(h.automationRepo)
	alertSvc := NewAlertService(alertRepo, nil)
	createApproved := func(definition *models.AutomationDefinition, title, key string) *models.Alert {
		producer := automationNodeByKey(t, definition, "custom_producer")
		producerCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{{
			AutomationID: definition.Automation.ID, VersionID: definition.Version.ID, NodeID: producer.ID,
		}}})
		alert, createErr := alertSvc.CreateActionable(producerCtx, &models.Alert{
			ProjectID: h.project.ID, Type: "suggestion", Title: title, Body: key + " private body", IdempotencyKey: key,
			Metadata: map[string]any{"private_metadata": key + " private metadata"},
		})
		require.NoError(t, createErr)
		require.NoError(t, alertSvc.SetDecision(ctx, h.project.ID, alert.ID, models.AlertDecisionApproved))
		return alert
	}
	firstAlert := createApproved(first.Definition, "First finding", "first-finding")
	secondAlert := createApproved(second.Definition, "Second finding", "second-finding")

	provenance, ok := firstAlert.Metadata[models.AlertAutomationProvenanceMetadataKey].([]any)
	require.True(t, ok)
	require.Len(t, provenance, 1)
	entry, ok := provenance[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, first.Definition.Automation.ID, entry["automation_id"])
	require.Equal(t, automationNodeByKey(t, first.Definition, "custom_approved_inbox").ID, entry["inbox_node_id"])

	firstInbox := automationNodeByKey(t, first.Definition, "custom_approved_inbox")
	forgedAlert, err := alertSvc.CreateActionable(ctx, &models.Alert{ProjectID: h.project.ID, Type: "suggestion", Title: "Forged finding", Body: "forged private body", IdempotencyKey: "forged-finding", Metadata: map[string]any{
		"private_metadata":                          "forged private metadata",
		models.AlertAutomationProvenanceMetadataKey: []any{map[string]any{"automation_id": first.Definition.Automation.ID, "version_id": first.Definition.Version.ID, "inbox_node_id": firstInbox.ID}},
	}})
	require.NoError(t, err)
	require.NoError(t, alertSvc.SetDecision(ctx, h.project.ID, forgedAlert.ID, models.AlertDecisionApproved))
	inboxCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{{
		AutomationID: first.Definition.Automation.ID, VersionID: first.Definition.Version.ID, NodeID: firstInbox.ID,
	}}})
	handlers := BuildAlertRuntimeActionHandlers(AlertRuntimeOptions{ProjectID: h.project.ID, CallerTaskID: "first-inbox", AlertSvc: alertSvc, TaskRepo: h.taskRepo})
	output, err := handlers["list_alerts"](inboxCtx, json.RawMessage(`{"decision_state":"approved","processing_state":"unclaimed"}`))
	require.NoError(t, err)
	require.Contains(t, output, firstAlert.ID)
	require.NotContains(t, output, secondAlert.ID)
	require.NotContains(t, output, forgedAlert.ID)

	output, err = handlers["get_alert"](inboxCtx, json.RawMessage(`{"alert_id":"`+firstAlert.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, output, firstAlert.ID)
	require.Contains(t, output, firstAlert.Body)
	require.Contains(t, output, "first-finding private metadata")

	for _, target := range []*models.Alert{secondAlert, forgedAlert} {
		output, err = handlers["get_alert"](inboxCtx, json.RawMessage(`{"alert_id":"`+target.ID+`"}`))
		require.ErrorContains(t, err, "not owned by this Automation inbox")
		require.NotContains(t, err.Error(), target.ID)
		require.NotContains(t, err.Error(), target.Body)
		require.NotContains(t, err.Error(), target.Metadata["private_metadata"])
		require.Empty(t, output)
		require.NotContains(t, output, target.ID)
		require.NotContains(t, output, target.Body)
		require.NotContains(t, output, target.Metadata["private_metadata"])
	}

	output, err = handlers["get_alert"](ctx, json.RawMessage(`{"alert_id":"`+forgedAlert.ID+`"}`))
	require.NoError(t, err)
	require.Contains(t, output, forgedAlert.ID)
	require.Contains(t, output, forgedAlert.Body)
	require.Contains(t, output, "forged private metadata")

	_, err = handlers["claim_alert"](inboxCtx, json.RawMessage(`{"alert_id":"`+secondAlert.ID+`","lease_seconds":60}`))
	require.ErrorContains(t, err, "not owned by this Automation inbox")
	_, err = handlers["claim_alert"](inboxCtx, json.RawMessage(`{"alert_id":"`+forgedAlert.ID+`","lease_seconds":60}`))
	require.ErrorContains(t, err, "not owned by this Automation inbox")
}

func TestAutomationRuntimeCustomNativeMailboxUsesRoleTopologyInsteadOfMaintainedNodeKeys(t *testing.T) {
	h := newAutomationSaveHarness(t, "Custom Native runtime")
	ctx := context.Background()
	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: customNativeMailboxCandidate("Custom Native runtime")})
	require.NoError(t, err)
	producer := automationNodeByKey(t, saved.Definition, "custom_producer")
	producerTaskID := automationResourceID(t, saved.Definition, "custom_producer", "task")
	alertRepo := repository.NewAlertRepo(h.db)
	alertRepo.SetAutomationRepo(h.automationRepo)
	alertSvc := NewAlertService(alertRepo, nil)
	ctx = WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{{AutomationID: saved.Definition.Automation.ID, VersionID: saved.Definition.Version.ID, NodeID: producer.ID}}})
	alert, err := alertSvc.CreateActionable(ctx, &models.Alert{ProjectID: h.project.ID, SourceTaskID: &producerTaskID, Type: "suggestion", Title: "Custom Native finding", Body: "Review me", IdempotencyKey: "custom-native-finding"})
	require.NoError(t, err)
	require.NoError(t, alertSvc.SetDecision(ctx, h.project.ID, alert.ID, models.AlertDecisionApproved))
	_, err = alertSvc.ClaimApproved(ctx, h.project.ID, alert.ID, "custom-inbox", time.Minute)
	require.NoError(t, err)
	implementation, err := alertSvc.CreateImplementationTask(ctx, h.project.ID, alert.ID, "custom-inbox", models.AlertImplementationTaskInput{Title: "Implement custom finding", Prompt: "Implement safely", Priority: 2})
	require.NoError(t, err)
	require.NoError(t, alertSvc.MarkProcessing(ctx, h.project.ID, alert.ID, "custom-inbox", models.AlertProcessingCompleted, ""))
	implementationNode := automationNodeByKey(t, saved.Definition, "custom_implementation")
	taskContext, err := h.automationRepo.ContextForTask(ctx, h.project.ID, implementation.ID)
	require.NoError(t, err)
	require.Len(t, taskContext.Bindings, 1)
	require.Equal(t, implementationNode.ID, taskContext.Bindings[0].NodeID)
	for _, edgeKey := range []string{"custom_approval_inbox", "custom_inbox_implementation"} {
		require.Equal(t, 1, countRows(t, h.db, `SELECT COUNT(*) FROM automation_transitions tr JOIN automation_edges e ON e.id = tr.edge_id WHERE tr.automation_id = ? AND e.edge_key = ?`, saved.Definition.Automation.ID, edgeKey))
	}
}

func TestAutomationRuntimeReducedNativeApprovalBranchesEndAtGate(t *testing.T) {
	for _, test := range []struct {
		name       string
		decision   models.AlertDecisionState
		removeKeys []string
	}{
		{name: "rejection without rejected outcome", decision: models.AlertDecisionRejected, removeKeys: []string{"rejected"}},
		{name: "approval without inbox branch", decision: models.AlertDecisionApproved, removeKeys: []string{"inbox", "implementation", "completed"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newAutomationSaveHarness(t, "Reduced Native decision")
			ctx := context.Background()
			candidate, err := h.drafts.TemplateCandidate(AutomationAdapterNativeSDLC)
			require.NoError(t, err)
			for _, key := range test.removeKeys {
				candidate = automationCandidateWithoutNode(candidate, key)
			}
			saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: candidate})
			require.NoError(t, err)

			alertRepo := repository.NewAlertRepo(h.db)
			alertRepo.SetAutomationRepo(h.automationRepo)
			alertSvc := NewAlertService(alertRepo, nil)
			producer := automationNodeByKey(t, saved.Definition, "vision_suggestions")
			producerCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{{
				AutomationID: saved.Definition.Automation.ID, VersionID: saved.Definition.Version.ID, NodeID: producer.ID,
			}}})
			alert, err := alertSvc.CreateActionable(producerCtx, &models.Alert{ProjectID: h.project.ID, Type: "suggestion", Title: test.name, IdempotencyKey: test.name})
			require.NoError(t, err)
			require.NoError(t, alertSvc.SetDecision(ctx, h.project.ID, alert.ID, test.decision))

			stored, err := alertSvc.GetByID(ctx, h.project.ID, alert.ID)
			require.NoError(t, err)
			require.Equal(t, test.decision, stored.DecisionState)
			require.Equal(t, 1, countRows(t, h.db, `SELECT COUNT(*) FROM automation_work_items WHERE automation_id = ? AND status = 'completed'`, saved.Definition.Automation.ID))
		})
	}
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

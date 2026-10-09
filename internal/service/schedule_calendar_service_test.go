package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSchedulerHonorsCalendarSkipsAndPauseBeforeDispatch(t *testing.T) {
	for _, mode := range []string{"skip", "pause_all"} {
		for _, repeat := range []models.RepeatType{models.RepeatOnce, models.RepeatMinutes} {
			t.Run(mode+"/"+string(repeat), func(t *testing.T) {
				db := testutil.NewTestDB(t)
				ctx := context.Background()
				schedules := repository.NewScheduleRepo(db)
				tasks := repository.NewTaskRepo(db, nil)
				worker := newTestWorkerService(t)
				svc := NewSchedulerService(schedules, tasks, worker)
				now := time.Now().UTC().Truncate(time.Second)
				svc.now = func() time.Time { return now }
				task := &models.Task{ProjectID: "default", Title: "Excluded run", Prompt: "test", Category: models.CategoryScheduled, Status: models.StatusPending}
				require.NoError(t, tasks.Create(ctx, task))
				sched := &models.Schedule{TaskID: task.ID, RunAt: now.Add(-time.Minute), RepeatType: repeat, RepeatInterval: 1, Enabled: true}
				require.NoError(t, schedules.Create(ctx, sched))
				action := models.ScheduleCalendarAction{Action: mode}
				if mode == "skip" {
					action.Skips = []models.ScheduleSkip{{ScheduleID: sched.ID, StartAt: now.Add(-time.Hour).Unix(), EndAt: now.Add(time.Hour).Unix()}}
				}
				_, err := ApplyScheduleCalendarAction(ctx, schedules, "default", action)
				require.NoError(t, err)
				svc.checkDueTasks(ctx)
				select {
				case <-worker.Submitted():
					t.Fatal("excluded run dispatched")
				default:
				}
				got, err := schedules.GetByID(ctx, sched.ID)
				require.NoError(t, err)
				require.Nil(t, got.LastRun)
				if repeat == models.RepeatOnce {
					require.Nil(t, got.NextRun)
				} else {
					require.True(t, got.NextRun.After(now))
					require.True(t, got.NextRun.Before(now.Add(time.Hour)), "restoring an active exclusion must not lose future runs")
				}
			})
		}
	}
}

func TestScheduleCalendarActionRejectsMalformedSelections(t *testing.T) {
	repo := repository.NewScheduleRepo(testutil.NewTestDB(t))
	for _, action := range []models.ScheduleCalendarAction{
		{Action: "pause"}, {Action: "skip"}, {Action: "unexpected"},
		{Action: "skip", Skips: []models.ScheduleSkip{{StartAt: 10, EndAt: 9}}},
		{Action: "pause_all", ScheduleIDs: []string{"unexpected"}},
	} {
		_, err := ApplyScheduleCalendarAction(context.Background(), repo, "default", action)
		require.ErrorIs(t, err, repository.ErrScheduleCalendarSelection)
	}
}

func TestSchedulerPreservesRestoredRunAfterDowntime(t *testing.T) {
	for _, afterDay := range []bool{false, true} {
		t.Run(fmt.Sprint(afterDay), func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := context.Background()
			repo := repository.NewScheduleRepo(db)
			tasks := repository.NewTaskRepo(db, nil)
			worker := newTestWorkerService(t)
			svc := NewSchedulerService(repo, tasks, worker)
			day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
			now := day.Add(9*time.Hour + 5*time.Minute)
			if afterDay {
				now = day.Add(25 * time.Hour)
			}
			svc.now = func() time.Time { return now }
			task := &models.Task{ProjectID: "default", Title: "Restored run", Prompt: "test", Category: models.CategoryScheduled, Status: models.StatusPending}
			require.NoError(t, tasks.Create(ctx, task))
			sched := &models.Schedule{TaskID: task.ID, RunAt: day.Add(8 * time.Hour), RepeatType: models.RepeatHours, RepeatInterval: 1, Enabled: true}
			require.NoError(t, repo.Create(ctx, sched))
			_, err := repo.ApplyCalendarAction(ctx, "default", models.ScheduleCalendarAction{Action: "skip", Skips: []models.ScheduleSkip{{StartAt: day.Unix(), EndAt: day.Add(24 * time.Hour).Unix()}}}, day)
			require.NoError(t, err)
			restored := day.Add(9 * time.Hour)
			_, err = repo.ApplyCalendarAction(ctx, "default", models.ScheduleCalendarAction{Action: "restore", Skips: []models.ScheduleSkip{{ScheduleID: sched.ID, StartAt: restored.Unix(), EndAt: restored.Add(time.Hour).Unix()}}}, day)
			require.NoError(t, err)
			svc.checkDueTasks(ctx)
			got, err := repo.GetByID(ctx, sched.ID)
			require.NoError(t, err)
			require.NotNil(t, got.NextRun)
			require.True(t, got.NextRun.Equal(restored), "restored run must remain eligible")
			svc.checkDueTasks(ctx)
			select {
			case submitted := <-worker.Submitted():
				require.Equal(t, task.ID, submitted.ID)
			default:
				t.Fatal("restored run was not dispatched")
			}
		})
	}
}

func TestSchedulerRecoversAdmissionBeforeWorkerSubmission(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := repository.NewScheduleRepo(db)
	tasks := repository.NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: "default", Title: "Restart recovery", Prompt: "test", Category: models.CategoryCompleted, Status: models.StatusCompleted}
	require.NoError(t, tasks.Create(ctx, task))
	now := time.Now().UTC().Truncate(time.Second)
	schedule := &models.Schedule{TaskID: task.ID, RunAt: now, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}
	require.NoError(t, repo.Create(ctx, schedule))
	claimed, err := repo.ClaimCalendarOccurrence(ctx, *schedule, now, nil)
	require.NoError(t, err)
	require.True(t, claimed)
	// Simulate losing the old scheduler and its in-memory worker queue.
	worker := newTestWorkerService(t)
	worker.SetTaskRepo(tasks)
	worker.SetExecutionRepo(repository.NewExecutionRepo(db))
	svc := NewSchedulerService(repository.NewScheduleRepo(db), tasks, worker)
	svc.now = func() time.Time { return now.Add(time.Minute) }
	svc.checkDueTasks(ctx)
	select {
	case submitted := <-worker.Submitted():
		require.Equal(t, task.ID, submitted.ID)
		require.True(t, submitted.StartsNewContext)
	default:
		t.Fatal("durable admission was lost after restart")
	}
	worker.mu.Lock()
	executionID := worker.reserved[task.ID]
	worker.mu.Unlock()
	require.NotEmpty(t, executionID, "recovered work must use the existing execution reservation")
	executions, err := repository.NewExecutionRepo(db).ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, executions, 1)
	require.Equal(t, executionID, executions[0].ID)
}

func TestCalendarWorkerRestartExecutesReservedRun(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tasks := repository.NewTaskRepo(db, nil)
	schedules := repository.NewScheduleRepo(db)
	executions := repository.NewExecutionRepo(db)
	projects := repository.NewProjectRepo(db)
	configs := repository.NewLLMConfigRepo(db)
	model := &models.LLMConfig{Name: "Calendar recovery model", Provider: models.ProviderTest, Model: "test-model", AuthMethod: models.AuthMethodAPIKey}
	require.NoError(t, configs.Create(ctx, model))
	task := &models.Task{ProjectID: "default", Title: "Durable calendar worker", Prompt: "test", AgentID: &model.ID, Category: models.CategoryScheduled, Status: models.StatusPending}
	require.NoError(t, tasks.Create(ctx, task))
	now := time.Now().UTC().Truncate(time.Second)
	schedule := &models.Schedule{TaskID: task.ID, RunAt: now, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}
	require.NoError(t, schedules.Create(ctx, schedule))
	admitted, err := schedules.ClaimCalendarOccurrence(ctx, *schedule, now, nil)
	require.NoError(t, err)
	require.True(t, admitted)
	before, err := executions.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, before, 1)
	llm := NewLLMService(configs, executions, tasks, projects, schedules, repository.NewAttachmentRepo(db))
	mock := testutil.NewMockLLMCaller()
	mock.Response = "Recovered run complete"
	mock.TextOnly = mock.Response
	llm.SetLLMCaller(mock)
	llm.SetQueuedTaskThreadPromoter(func(string) {})
	worker := NewWorkerService(llm, 1, projects)
	worker.SetTaskRepo(tasks)
	worker.SetExecutionRepo(executions)
	worker.SetLLMConfigRepo(configs)
	// Worker startup alone recovers the durable reservation; no scheduler Submit.
	worker.Start(ctx)
	defer worker.Stop()
	require.Eventually(t, func() bool {
		stored, err := tasks.GetByID(ctx, task.ID)
		return err == nil && stored.Status == models.StatusCompleted
	}, 5*time.Second, 10*time.Millisecond)
	after, err := executions.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.Equal(t, before[0].ID, after[0].ID)
	require.True(t, after[0].StartsNewContext)
	admissions, err := schedules.ListCalendarAdmissions(ctx)
	require.NoError(t, err)
	require.Empty(t, admissions)
}

// These tests use the real swarm handoff, not a starter stub.
func TestCalendarPlannerAdmissionRecovery(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := context.Background()
			tasks := repository.NewTaskRepo(db, nil)
			schedules := repository.NewScheduleRepo(db)
			worker := newTestWorkerService(t)
			swarm := NewSwarmService(NewTaskService(tasks, nil, worker), tasks, repository.NewExecutionRepo(db), worker)
			parent := &models.Task{ProjectID: "default", Title: "Planner recovery", Prompt: "plan", Category: models.CategoryScheduled, Status: models.StatusPending, SwarmRole: models.SwarmRoleParent}
			require.NoError(t, tasks.Create(ctx, parent))
			if existing {
				child := &models.Task{ProjectID: parent.ProjectID, Title: "Existing planner", Prompt: "plan again", Category: models.CategoryBacklog, Status: models.StatusFailed, ParentTaskID: &parent.ID, SwarmRole: models.SwarmRolePlanner}
				require.NoError(t, tasks.Create(ctx, child))
			}
			now := time.Now().UTC().Truncate(time.Second)
			schedule := &models.Schedule{TaskID: parent.ID, RunAt: now, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}
			require.NoError(t, schedules.Create(ctx, schedule))
			claimed, err := schedules.ClaimCalendarOccurrence(ctx, *schedule, now, nil)
			require.NoError(t, err)
			require.True(t, claimed)
			// The persisted flag wins over stale handoff metadata.
			require.NoError(t, swarm.StartPlannerForScheduledRun(ctx, parent.ID, false))
			admissions, err := schedules.ListCalendarAdmissions(ctx)
			require.NoError(t, err)
			require.Len(t, admissions, 1)
			admission := admissions[0]
			require.NotEmpty(t, admission.ExecutionID)
			require.NotEqual(t, parent.ID, admission.Task.ID)
			require.True(t, admission.Task.StartsNewContext)
			require.Equal(t, models.SwarmRolePlanner, admission.Task.SwarmRole)
			require.NoError(t, swarm.StartPlannerForScheduledRun(ctx, parent.ID, true))
			// Lose all in-memory queues, then use actual startup reservation recovery.
			recovered := newTestWorkerService(t)
			recovered.SetTaskRepo(repository.NewTaskRepo(db, nil))
			recovered.SetExecutionRepo(repository.NewExecutionRepo(db))
			recovered.ReconcileReservedTasks(ctx)
			select {
			case task := <-recovered.Submitted():
				require.Equal(t, admission.Task.ID, task.ID)
				require.True(t, task.StartsNewContext)
			default:
				t.Fatal("planner reservation was not recovered")
			}
			dispatch, started, err := tasks.ClaimReservedTaskForDispatch(ctx, admission.Task.ID, admission.ExecutionID)
			require.NoError(t, err)
			require.True(t, started)
			require.True(t, dispatch.Task.StartsNewContext)
			_, started, err = tasks.ClaimReservedTaskForDispatch(ctx, admission.Task.ID, admission.ExecutionID)
			require.NoError(t, err)
			require.False(t, started)
			var executions int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM executions WHERE task_id = ?`, admission.Task.ID).Scan(&executions))
			require.Equal(t, 1, executions)
		})
	}
}

func TestCalendarPlannerCancelledHandoff(t *testing.T) {
	for _, transferred := range []bool{false, true} {
		t.Run(fmt.Sprint(transferred), func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := context.Background()
			tasks := repository.NewTaskRepo(db, nil)
			schedules := repository.NewScheduleRepo(db)
			worker := newTestWorkerService(t)
			swarm := NewSwarmService(NewTaskService(tasks, nil, worker), tasks, repository.NewExecutionRepo(db), worker)
			parent := &models.Task{ProjectID: "default", Title: "Cancelled calendar swarm", Prompt: "plan", Category: models.CategoryScheduled, Status: models.StatusPending, SwarmRole: models.SwarmRoleParent}
			require.NoError(t, tasks.Create(ctx, parent))
			now := time.Now().UTC().Truncate(time.Second)
			schedule := &models.Schedule{TaskID: parent.ID, RunAt: now, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}
			require.NoError(t, schedules.Create(ctx, schedule))
			claimed, err := schedules.ClaimCalendarOccurrence(ctx, *schedule, now, nil)
			require.NoError(t, err)
			require.True(t, claimed)
			stale, err := schedules.ListCalendarAdmissions(ctx)
			require.NoError(t, err)
			require.Len(t, stale, 1)
			var queued repository.ActiveLaneTaskAdmission
			if transferred {
				require.NoError(t, swarm.StartPlannerForScheduledRun(ctx, parent.ID, true))
				rows, err := schedules.ListCalendarAdmissions(ctx)
				require.NoError(t, err)
				require.Len(t, rows, 1)
				queued = rows[0]
			}
			if transferred {
				// Parent withdrawal must cancel the durable planner reservation
				// even before the service cascades cancellation to its children.
				require.NoError(t, tasks.UpdateStatus(ctx, parent.ID, models.StatusCancelled))
				var status string
				require.NoError(t, db.QueryRow(`SELECT status FROM executions WHERE id = ?`, queued.ExecutionID).Scan(&status))
				require.Equal(t, string(models.ExecCancelled), status)
				child, err := tasks.GetByID(ctx, queued.Task.ID)
				require.NoError(t, err)
				require.Equal(t, models.StatusCancelled, child.Status)
			}
			require.NoError(t, swarm.CancelSwarm(ctx, parent.ID))
			require.NoError(t, swarm.StartPlannerForScheduledRun(ctx, stale[0].Task.ID, stale[0].Task.StartsNewContext))
			current, err := tasks.GetByID(ctx, parent.ID)
			require.NoError(t, err)
			require.Equal(t, models.StatusCancelled, current.Status)
			admissions, err := schedules.ListCalendarAdmissions(ctx)
			require.NoError(t, err)
			require.Empty(t, admissions)
			if transferred {
				_, started, err := tasks.ClaimReservedTaskForDispatch(ctx, queued.Task.ID, queued.ExecutionID)
				require.NoError(t, err)
				require.False(t, started)
			} else {
				children, err := tasks.ListSwarmChildren(ctx, parent.ID)
				require.NoError(t, err)
				require.Empty(t, children)
				select {
				case <-worker.Submitted():
					t.Fatal("cancelled handoff submitted a planner")
				default:
				}
			}
		})
	}
}

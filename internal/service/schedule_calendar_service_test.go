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

type calendarRetryPlanner struct {
	fail  bool
	calls int
	clear bool
}

func (p *calendarRetryPlanner) StartPlanner(context.Context, string) error {
	return fmt.Errorf("unexpected unscheduled handoff")
}
func (p *calendarRetryPlanner) StartPlannerForScheduledRun(_ context.Context, _ string, clear bool) error {
	p.calls++
	p.clear = clear
	if p.fail {
		return fmt.Errorf("planner unavailable")
	}
	return nil
}

func TestCalendarPlannerAdmissionRetriesAfterRestart(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := repository.NewScheduleRepo(db)
	tasks := repository.NewTaskRepo(db, nil)
	parent := &models.Task{ProjectID: "default", Title: "Planner recovery", Prompt: "plan", Category: models.CategoryScheduled, Status: models.StatusPending, SwarmRole: models.SwarmRoleParent}
	require.NoError(t, tasks.Create(ctx, parent))
	now := time.Now().UTC().Truncate(time.Second)
	schedule := &models.Schedule{TaskID: parent.ID, RunAt: now, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}
	require.NoError(t, repo.Create(ctx, schedule))
	planner := &calendarRetryPlanner{fail: true}
	svc := NewSchedulerService(repo, tasks, newTestWorkerService(t))
	svc.SetSwarmPlannerStarter(planner)
	svc.now = func() time.Time { return now }
	svc.checkDueTasks(ctx)
	admissions, err := repo.ListCalendarAdmissions(ctx)
	require.NoError(t, err)
	require.Len(t, admissions, 1)
	planner = &calendarRetryPlanner{}
	restarted := NewSchedulerService(repository.NewScheduleRepo(db), tasks, newTestWorkerService(t))
	restarted.SetSwarmPlannerStarter(planner)
	restarted.checkDueTasks(ctx)
	require.Equal(t, 1, planner.calls)
	require.True(t, planner.clear)
	admissions, err = repo.ListCalendarAdmissions(ctx)
	require.NoError(t, err)
	require.Empty(t, admissions)
}

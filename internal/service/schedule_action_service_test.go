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

func TestScheduleActionServiceCreateRejectsMalformedInputsWithoutPersistence(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	project := &models.Project{Name: "Schedule validation"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Target", Prompt: "prompt", Category: models.CategoryBacklog, Status: models.StatusCompleted, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	svc := NewScheduleActionService(taskRepo, scheduleRepo)

	tests := []struct {
		name string
		req  ScheduleTaskRequest
	}{
		{name: "trailing time text", req: ScheduleTaskRequest{TaskID: task.ID, Time: "09:30junk", Repeat: "daily"}},
		{name: "time with seconds", req: ScheduleTaskRequest{TaskID: task.ID, Time: "09:30:45", Repeat: "daily"}},
		{name: "unsupported repeat", req: ScheduleTaskRequest{TaskID: task.ID, Time: "09:30", Repeat: "yearly"}},
		{name: "unknown weekly day", req: ScheduleTaskRequest{TaskID: task.ID, Time: "09:30", Repeat: "weekly", Days: []string{"monday"}}},
		{name: "mixed valid and unknown weekly days", req: ScheduleTaskRequest{TaskID: task.ID, Time: "09:30", Repeat: "weekly", Days: []string{"mon", "funday"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Create(ctx, project.ID, tt.req)
			require.Error(t, err)
			schedules, listErr := scheduleRepo.ListByTask(ctx, task.ID)
			require.NoError(t, listErr)
			require.Empty(t, schedules)
			storedTask, getErr := taskRepo.GetByID(ctx, task.ID)
			require.NoError(t, getErr)
			require.Equal(t, models.CategoryBacklog, storedTask.Category)
			require.Equal(t, models.StatusCompleted, storedTask.Status)
		})
	}
}

func TestScheduleActionServiceCreateClearsCancellationRequestForScheduledRun(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerSvc := newTestWorkerService(t)
	project := &models.Project{Name: "Schedule clears cancellation marker"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Stopped target", Prompt: "prompt", Category: models.CategoryBacklog, Status: models.StatusCancelled, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	workerSvc.MarkCancellationRequested(task.ID)
	require.True(t, workerSvc.IsCancellationRequested(task.ID))
	svc := NewScheduleActionService(taskRepo, scheduleRepo, workerSvc)

	_, err := svc.Create(ctx, project.ID, ScheduleTaskRequest{TaskID: task.ID, Time: "09:30", Repeat: "daily"})
	require.NoError(t, err)
	require.False(t, workerSvc.IsCancellationRequested(task.ID), "schedule activation should clear stale cancellation marker")
	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryScheduled, updated.Category)
	require.Equal(t, models.StatusPending, updated.Status)
}

func TestScheduleActionServiceCreateOneTimeResetsCompletedAlreadyScheduledTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	project := &models.Project{Name: "Runtime one-time completed scheduled task"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Completed scheduled target", Prompt: "prompt", Category: models.CategoryScheduled, Status: models.StatusCompleted, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	svc := NewScheduleActionService(taskRepo, scheduleRepo)

	result, err := svc.Create(ctx, project.ID, ScheduleTaskRequest{TaskID: task.ID, Time: "09:30", Repeat: "once"})
	require.NoError(t, err)
	require.NotNil(t, result.Schedule)
	require.Equal(t, models.RepeatOnce, result.Schedule.RepeatType)
	require.NotNil(t, result.Schedule.NextRun)
	require.Equal(t, models.CategoryScheduled, result.Task.Category)
	require.Equal(t, models.StatusPending, result.Task.Status)

	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryScheduled, updated.Category)
	require.Equal(t, models.StatusPending, updated.Status)
	schedules, err := scheduleRepo.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, schedules, 1)
}

func TestScheduleActionServiceCreateDoesNotResetRunningAlreadyScheduledTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	project := &models.Project{Name: "Runtime running scheduled task"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Running scheduled target", Prompt: "prompt", Category: models.CategoryScheduled, Status: models.StatusRunning, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	svc := NewScheduleActionService(taskRepo, scheduleRepo)

	result, err := svc.Create(ctx, project.ID, ScheduleTaskRequest{TaskID: task.ID, Time: "09:30", Repeat: "once"})
	require.NoError(t, err)
	require.NotNil(t, result.Schedule)
	require.Equal(t, models.StatusRunning, result.Task.Status)

	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryScheduled, updated.Category)
	require.Equal(t, models.StatusRunning, updated.Status)
}

func TestScheduleActionServiceCreateRecurringPreservesTerminalAlreadyScheduledTask(t *testing.T) {
	for _, status := range []models.TaskStatus{models.StatusCompleted, models.StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := context.Background()
			projectRepo := repository.NewProjectRepo(db)
			taskRepo := repository.NewTaskRepo(db, nil)
			scheduleRepo := repository.NewScheduleRepo(db)
			project := &models.Project{Name: "Runtime recurring scheduled task"}
			require.NoError(t, projectRepo.Create(ctx, project))
			task := &models.Task{ProjectID: project.ID, Title: "Terminal scheduled target", Prompt: "prompt", Category: models.CategoryScheduled, Status: status, Priority: 2}
			require.NoError(t, taskRepo.Create(ctx, task))
			svc := NewScheduleActionService(taskRepo, scheduleRepo)

			result, err := svc.Create(ctx, project.ID, ScheduleTaskRequest{TaskID: task.ID, Time: "09:30", Repeat: "daily"})
			require.NoError(t, err)
			require.NotNil(t, result.Schedule)
			require.Equal(t, models.RepeatDaily, result.Schedule.RepeatType)
			require.Equal(t, status, result.Task.Status)

			updated, err := taskRepo.GetByID(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, models.CategoryScheduled, updated.Category)
			require.Equal(t, status, updated.Status)
		})
	}
}

func TestScheduleActionServiceModifyRejectsMalformedInputsWithoutMutation(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	project := &models.Project{Name: "Schedule validation"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Target", Prompt: "prompt", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	runAt := time.Date(2030, time.January, 7, 8, 15, 0, 0, time.Local).UTC()
	schedule := &models.Schedule{TaskID: task.ID, RunAt: runAt, RepeatType: models.RepeatWeekly, RepeatInterval: 2, Enabled: true}
	require.NoError(t, scheduleRepo.Create(ctx, schedule))
	svc := NewScheduleActionService(taskRepo, scheduleRepo)

	tests := []struct {
		name string
		req  ModifyScheduleRequest
	}{
		{name: "trailing time text", req: ModifyScheduleRequest{ScheduleID: schedule.ID, Time: "09:30junk"}},
		{name: "time with seconds", req: ModifyScheduleRequest{ScheduleID: schedule.ID, Time: "09:30:45"}},
		{name: "unsupported repeat", req: ModifyScheduleRequest{ScheduleID: schedule.ID, Repeat: "yearly"}},
		{name: "unknown weekly day", req: ModifyScheduleRequest{ScheduleID: schedule.ID, Days: []string{"monday"}}},
		{name: "mixed valid and unknown weekly days", req: ModifyScheduleRequest{ScheduleID: schedule.ID, Days: []string{"fri", "funday"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Modify(ctx, project.ID, tt.req)
			require.Error(t, err)
			stored, getErr := scheduleRepo.GetByID(ctx, schedule.ID)
			require.NoError(t, getErr)
			require.Equal(t, runAt, stored.RunAt)
			require.Equal(t, models.RepeatWeekly, stored.RepeatType)
			require.Equal(t, 2, stored.RepeatInterval)
		})
	}
}

func TestScheduleActionServiceCreateRejectsMultipleWeeklyDaysWithoutPersistence(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	project := &models.Project{Name: "Schedule multi-day validation"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Target", Prompt: "prompt", Category: models.CategoryBacklog, Status: models.StatusCompleted, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	svc := NewScheduleActionService(taskRepo, scheduleRepo)

	_, err := svc.Create(ctx, project.ID, ScheduleTaskRequest{TaskID: task.ID, Time: "09:30", Repeat: "weekly", Days: []string{"mon", "wed", "fri"}})
	require.Error(t, err)
	var actionErr *ScheduleActionError
	require.ErrorAs(t, err, &actionErr)
	require.Equal(t, ScheduleActionDaysError, actionErr.Kind)
	require.Contains(t, err.Error(), "one weekly day")

	schedules, listErr := scheduleRepo.ListByTask(ctx, task.ID)
	require.NoError(t, listErr)
	require.Empty(t, schedules)
	storedTask, getErr := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, getErr)
	require.Equal(t, models.CategoryBacklog, storedTask.Category)
	require.Equal(t, models.StatusCompleted, storedTask.Status)
}

func TestScheduleActionServiceModifyRejectsMultipleWeeklyDaysWithoutMutation(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	project := &models.Project{Name: "Schedule multi-day validation"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Target", Prompt: "prompt", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	runAt := time.Date(2030, time.January, 7, 8, 15, 0, 0, time.Local).UTC()
	schedule := &models.Schedule{TaskID: task.ID, RunAt: runAt, RepeatType: models.RepeatWeekly, RepeatInterval: 2, Enabled: true, ClearContextOnStart: false}
	require.NoError(t, scheduleRepo.Create(ctx, schedule))
	svc := NewScheduleActionService(taskRepo, scheduleRepo)

	_, err := svc.Modify(ctx, project.ID, ModifyScheduleRequest{ScheduleID: schedule.ID, Days: []string{"mon", "wed", "fri"}})
	require.Error(t, err)
	var actionErr *ScheduleActionError
	require.ErrorAs(t, err, &actionErr)
	require.Equal(t, ScheduleActionDaysError, actionErr.Kind)
	require.Contains(t, err.Error(), "one weekly day")

	stored, getErr := scheduleRepo.GetByID(ctx, schedule.ID)
	require.NoError(t, getErr)
	require.Equal(t, runAt, stored.RunAt)
	require.Equal(t, models.RepeatWeekly, stored.RepeatType)
	require.Equal(t, 2, stored.RepeatInterval)
	require.False(t, stored.ClearContextOnStart)
}

func TestScheduleActionServiceAcceptsTimeBoundariesAndSupportedWeekdays(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	project := &models.Project{Name: "Schedule validation"}
	require.NoError(t, projectRepo.Create(ctx, project))
	svc := NewScheduleActionService(taskRepo, scheduleRepo)

	for i, tt := range []struct {
		time    string
		day     string
		weekday time.Weekday
	}{
		{time: "00:00", day: "sun", weekday: time.Sunday},
		{time: "23:59", day: "mon", weekday: time.Monday},
		{time: "12:30", day: "tue", weekday: time.Tuesday},
		{time: "12:30", day: "wed", weekday: time.Wednesday},
		{time: "12:30", day: "thu", weekday: time.Thursday},
		{time: "12:30", day: "fri", weekday: time.Friday},
		{time: "12:30", day: "sat", weekday: time.Saturday},
	} {
		task := &models.Task{ProjectID: project.ID, Title: tt.day, Prompt: "prompt", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: i + 1}
		require.NoError(t, taskRepo.Create(ctx, task))
		result, err := svc.Create(ctx, project.ID, ScheduleTaskRequest{TaskID: task.ID, Time: tt.time, Repeat: "weekly", Days: []string{tt.day}})
		require.NoError(t, err)
		require.Equal(t, tt.weekday, result.Schedule.RunAt.Local().Weekday())
		require.Equal(t, tt.time, result.Schedule.RunAt.Local().Format("15:04"))
	}
}

func TestScheduleActionServiceCreateAbsoluteAppliesBrowserFormDefaultsAndLifecycle(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerSvc := newTestWorkerService(t)
	project := &models.Project{Name: "Absolute create lifecycle"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Completed scheduled target", Prompt: "prompt", Category: models.CategoryScheduled, Status: models.StatusCompleted, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	workerSvc.MarkCancellationRequested(task.ID)
	clearContext := false
	runAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	svc := NewScheduleActionService(taskRepo, scheduleRepo, workerSvc)

	result, err := svc.CreateAbsoluteForTask(ctx, CreateAbsoluteScheduleForTaskRequest{
		TaskID:              task.ID,
		RunAt:               runAt,
		RepeatType:          models.RepeatOnce,
		RepeatInterval:      0,
		ClearContextOnStart: &clearContext,
	})
	require.NoError(t, err)
	require.Empty(t, result.Warnings)
	require.NotNil(t, result.Schedule)
	require.Equal(t, models.RepeatOnce, result.Schedule.RepeatType)
	require.Equal(t, 1, result.Schedule.RepeatInterval)
	require.True(t, result.Schedule.Enabled)
	require.False(t, result.Schedule.ClearContextOnStart)
	require.NotNil(t, result.Schedule.NextRun)
	require.True(t, result.Schedule.NextRun.Equal(runAt), "next_run=%v run_at=%v", result.Schedule.NextRun, runAt)
	require.False(t, workerSvc.IsCancellationRequested(task.ID))

	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryScheduled, updated.Category)
	require.Equal(t, models.StatusPending, updated.Status)
}

func TestScheduleActionServiceModifyAbsoluteAppliesBrowserNextRunAndLifecycle(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerSvc := newTestWorkerService(t)
	project := &models.Project{Name: "Absolute update lifecycle"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Cancelled scheduled target", Prompt: "prompt", Category: models.CategoryScheduled, Status: models.StatusCancelled, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	oldRunAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	schedule := &models.Schedule{TaskID: task.ID, RunAt: oldRunAt, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true, ClearContextOnStart: false}
	require.NoError(t, scheduleRepo.Create(ctx, schedule))
	workerSvc.MarkCancellationRequested(task.ID)
	newRunAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	clearContext := true
	svc := NewScheduleActionService(taskRepo, scheduleRepo, workerSvc)

	result, err := svc.ModifyAbsolute(ctx, ModifyAbsoluteScheduleRequest{
		ScheduleID:          schedule.ID,
		RunAt:               newRunAt,
		RepeatType:          models.RepeatHours,
		RepeatInterval:      2,
		ClearContextOnStart: &clearContext,
	})
	require.NoError(t, err)
	require.Empty(t, result.Warnings)
	require.False(t, workerSvc.IsCancellationRequested(task.ID))

	storedSchedule, err := scheduleRepo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.Equal(t, models.RepeatHours, storedSchedule.RepeatType)
	require.Equal(t, 2, storedSchedule.RepeatInterval)
	require.True(t, storedSchedule.ClearContextOnStart)
	require.True(t, storedSchedule.RunAt.Equal(newRunAt), "run_at=%v want=%v", storedSchedule.RunAt, newRunAt)
	require.NotNil(t, storedSchedule.NextRun)
	require.True(t, storedSchedule.NextRun.Equal(newRunAt), "next_run=%v want=%v", storedSchedule.NextRun, newRunAt)
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusPending, updatedTask.Status)
}

// Regression tests for issue #634: Modify silently set NextRun=nil for RepeatOnce schedules.

func TestScheduleActionServiceModifyRepeatOnceTimeChangeSetsNextRun(t *testing.T) {
	// Regression case 1: Modify with RepeatOnce + time change must set NextRun to the
	// new RunAt, not nil (ComputeNextRun always returns nil for RepeatOnce).
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	project := &models.Project{Name: "RepeatOnce time change"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Once task", Prompt: "prompt", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	// Create an unfired one-time schedule (NextRun set by repo to RunAt).
	runAt := time.Date(2030, time.January, 10, 9, 0, 0, 0, time.Local).UTC()
	schedule := &models.Schedule{TaskID: task.ID, RunAt: runAt, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
	require.NoError(t, scheduleRepo.Create(ctx, schedule))
	require.NotNil(t, schedule.NextRun, "repo must set NextRun on creation")
	svc := NewScheduleActionService(taskRepo, scheduleRepo)

	result, err := svc.Modify(ctx, project.ID, ModifyScheduleRequest{ScheduleID: schedule.ID, Time: "10:00"})
	require.NoError(t, err)
	require.NotNil(t, result.Schedule)
	require.NotNil(t, result.Schedule.NextRun, "NextRun must not be nil after time change on a RepeatOnce schedule")

	stored, err := scheduleRepo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.NextRun, "persisted NextRun must not be nil")
	require.Equal(t, stored.RunAt, *stored.NextRun, "NextRun must equal the updated RunAt")
	require.Equal(t, 10, stored.RunAt.Local().Hour())
	require.Equal(t, 0, stored.RunAt.Local().Minute())
}

func TestScheduleActionServiceModifyRepeatOnceReenableWithoutTimeReturnsError(t *testing.T) {
	// Regression case 2: Modify with RepeatOnce + enabled=true + NextRun==nil must
	// return an error; it must not silently persist Enabled=true, NextRun=NULL.
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	project := &models.Project{Name: "RepeatOnce re-enable error"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Once task fired", Prompt: "prompt", Category: models.CategoryScheduled, Status: models.StatusCompleted, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	// Simulate a fired one-time schedule: Enabled=false, NextRun=nil (as the scheduler
	// leaves it after execution).
	runAt := time.Date(2024, time.January, 1, 9, 0, 0, 0, time.Local).UTC()
	schedule := &models.Schedule{TaskID: task.ID, RunAt: runAt, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: false}
	require.NoError(t, scheduleRepo.Create(ctx, schedule))
	// Manually clear NextRun to simulate post-fire state.
	schedule.NextRun = nil
	require.NoError(t, scheduleRepo.Update(ctx, schedule))
	stored, err := scheduleRepo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.Nil(t, stored.NextRun, "pre-condition: NextRun must be nil")
	svc := NewScheduleActionService(taskRepo, scheduleRepo)

	enabled := true
	_, err = svc.Modify(ctx, project.ID, ModifyScheduleRequest{ScheduleID: schedule.ID, Enabled: &enabled})
	require.Error(t, err, "re-enabling a fired one-time schedule without a time must return an error")
	var actionErr *ScheduleActionError
	require.ErrorAs(t, err, &actionErr)
	require.Equal(t, ScheduleActionTimeError, actionErr.Kind)

	// Verify nothing was persisted.
	reloaded, err := scheduleRepo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.Nil(t, reloaded.NextRun, "NextRun must remain nil — must not have been persisted as Enabled=true, NextRun=NULL")
	require.False(t, reloaded.Enabled, "Enabled must not have been flipped to true without a valid NextRun")
}

func TestScheduleActionServiceModifyRepeatOnceTimeAndEnableSetsNextRunAndResetsStatus(t *testing.T) {
	// Regression case 3: Modify with RepeatOnce + time + enabled=true on a fired
	// schedule must set NextRun to the new time and reset task status to pending.
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerSvc := newTestWorkerService(t)
	project := &models.Project{Name: "RepeatOnce reschedule"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Once task to reschedule", Prompt: "prompt", Category: models.CategoryScheduled, Status: models.StatusCompleted, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	runAt := time.Date(2024, time.January, 1, 9, 0, 0, 0, time.Local).UTC()
	schedule := &models.Schedule{TaskID: task.ID, RunAt: runAt, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: false}
	require.NoError(t, scheduleRepo.Create(ctx, schedule))
	schedule.NextRun = nil
	require.NoError(t, scheduleRepo.Update(ctx, schedule))
	svc := NewScheduleActionService(taskRepo, scheduleRepo, workerSvc)

	enabled := true
	result, err := svc.Modify(ctx, project.ID, ModifyScheduleRequest{ScheduleID: schedule.ID, Time: "09:30", Enabled: &enabled})
	require.NoError(t, err)
	require.NotNil(t, result.Schedule.NextRun, "NextRun must be set after time+enable on a RepeatOnce schedule")

	stored, err := scheduleRepo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.NextRun)
	require.Equal(t, stored.RunAt, *stored.NextRun, "NextRun must equal the new RunAt")
	require.Equal(t, 9, stored.RunAt.Local().Hour())
	require.Equal(t, 30, stored.RunAt.Local().Minute())
	require.True(t, stored.Enabled)

	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusPending, updatedTask.Status, "task status must be reset to pending")
}

func TestScheduleActionServiceModifyRecurringTimeChangePreservesComputeNextRun(t *testing.T) {
	// Regression case 4: Modify with a recurring schedule + time change must
	// continue to use ComputeNextRun (not the RepeatOnce direct-assign path).
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	project := &models.Project{Name: "Recurring time change"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Daily task", Prompt: "prompt", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	runAt := time.Date(2030, time.January, 7, 8, 15, 0, 0, time.Local).UTC()
	schedule := &models.Schedule{TaskID: task.ID, RunAt: runAt, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, scheduleRepo.Create(ctx, schedule))
	svc := NewScheduleActionService(taskRepo, scheduleRepo)

	result, err := svc.Modify(ctx, project.ID, ModifyScheduleRequest{ScheduleID: schedule.ID, Time: "11:00"})
	require.NoError(t, err)
	require.NotNil(t, result.Schedule.NextRun, "NextRun must be set for recurring schedule after time change")

	stored, err := scheduleRepo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.NextRun)
	// For a daily schedule, ComputeNextRun advances from RunAt; verify NextRun is after now.
	require.True(t, stored.NextRun.After(time.Now()), "NextRun must be in the future for a future-anchored daily schedule")
}

func TestScheduleActionServiceModifyRepeatOnceModifyAbsoluteIsUnaffected(t *testing.T) {
	// Regression case 5: ModifyAbsolute (browser path) must continue to set
	// NextRun = &RunAt and must be unaffected by the RepeatOnce runtime fix.
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerSvc := newTestWorkerService(t)
	project := &models.Project{Name: "ModifyAbsolute unaffected"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Once browser task", Prompt: "prompt", Category: models.CategoryScheduled, Status: models.StatusCompleted, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	oldRunAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	schedule := &models.Schedule{TaskID: task.ID, RunAt: oldRunAt, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
	require.NoError(t, scheduleRepo.Create(ctx, schedule))
	newRunAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	svc := NewScheduleActionService(taskRepo, scheduleRepo, workerSvc)

	result, err := svc.ModifyAbsolute(ctx, ModifyAbsoluteScheduleRequest{
		ScheduleID:     schedule.ID,
		RunAt:          newRunAt,
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
	})
	require.NoError(t, err)
	require.Empty(t, result.Warnings)
	require.NotNil(t, result.Schedule.NextRun)
	require.True(t, result.Schedule.NextRun.Equal(newRunAt), "ModifyAbsolute must set NextRun=RunAt")

	stored, err := scheduleRepo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.NextRun)
	require.True(t, stored.NextRun.Equal(newRunAt))
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusPending, updatedTask.Status)
}

func TestScheduleActionServiceModifyRuntimeTimingAppliesLifecycleButDisableDoesNot(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerSvc := newTestWorkerService(t)
	project := &models.Project{Name: "Runtime modify lifecycle"}
	require.NoError(t, projectRepo.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "Runtime cancelled scheduled target", Prompt: "prompt", Category: models.CategoryScheduled, Status: models.StatusCancelled, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, task))
	schedule := &models.Schedule{TaskID: task.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, scheduleRepo.Create(ctx, schedule))
	workerSvc.MarkCancellationRequested(task.ID)
	svc := NewScheduleActionService(taskRepo, scheduleRepo, workerSvc)
	disabled := false

	_, err := svc.Modify(ctx, project.ID, ModifyScheduleRequest{ScheduleID: schedule.ID, Enabled: &disabled})
	require.NoError(t, err)
	require.True(t, workerSvc.IsCancellationRequested(task.ID), "pure disable should not reactivate a terminal scheduled task")
	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusCancelled, updated.Status)

	_, err = svc.Modify(ctx, project.ID, ModifyScheduleRequest{ScheduleID: schedule.ID, Time: "10:15"})
	require.NoError(t, err)
	require.False(t, workerSvc.IsCancellationRequested(task.ID), "timing updates should clear stale cancellation markers")
	updated, err = taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusPending, updated.Status)
}

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

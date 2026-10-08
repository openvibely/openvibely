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

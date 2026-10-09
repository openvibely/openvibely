package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestScheduleCalendarBulkPauseIsAtomicAndUndoPreservesPriorPause(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewScheduleRepo(db)
	tasks := NewTaskRepo(db, nil)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	var ids []string
	task := createTestTask(t, tasks)
	for _, enabled := range []bool{true, true, false} {
		s := &models.Schedule{TaskID: task.ID, RunAt: now.Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: enabled}
		require.NoError(t, repo.Create(ctx, s))
		ids = append(ids, s.ID)
	}
	_, err := repo.ApplyCalendarAction(ctx, "default", models.ScheduleCalendarAction{Action: "pause", ScheduleIDs: []string{ids[0], "not-in-project"}}, now)
	require.True(t, errors.Is(err, ErrScheduleCalendarSelection))
	unchanged, err := repo.GetByID(ctx, ids[0])
	require.NoError(t, err)
	require.True(t, unchanged.Enabled)
	inverse, err := repo.ApplyCalendarAction(ctx, "default", models.ScheduleCalendarAction{Action: "pause", ScheduleIDs: append(ids, ids[0])}, now)
	require.NoError(t, err)
	require.Equal(t, ids[:2], inverse.ScheduleIDs)
	for _, id := range ids {
		s, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		require.False(t, s.Enabled)
	}
	_, err = repo.ApplyCalendarAction(ctx, "default", inverse, now)
	require.NoError(t, err)
	for i, id := range ids {
		s, err := repo.GetByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, i < 2, s.Enabled)
	}
}

func TestScheduleCalendarDateSkipsAndProjectPause(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewScheduleRepo(db)
	tasks := NewTaskRepo(db, nil)
	ctx := context.Background()
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	day := time.Date(2026, 3, 8, 0, 0, 0, 0, loc) // 23-hour DST day
	task := createTestTask(t, tasks)
	s := &models.Schedule{TaskID: task.ID, RunAt: day.Add(2 * time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, repo.Create(ctx, s))
	skip := models.ScheduleSkip{StartAt: day.Unix(), EndAt: day.AddDate(0, 0, 1).Unix()}
	inverse, err := repo.ApplyCalendarAction(ctx, "default", models.ScheduleCalendarAction{Action: "skip", Skips: []models.ScheduleSkip{skip}}, day)
	require.NoError(t, err)
	for _, tc := range []struct {
		at   time.Time
		want bool
	}{{day.Add(-time.Second), false}, {day, true}, {day.AddDate(0, 0, 1).Add(-time.Second), true}, {day.AddDate(0, 0, 1), false}, {day.AddDate(0, 0, 7), false}} {
		suppressed, _, err := repo.SuppressedOccurrence(ctx, s.ID, tc.at)
		require.NoError(t, err)
		require.Equal(t, tc.want, suppressed)
	}
	_, err = repo.ApplyCalendarAction(ctx, "default", inverse, day)
	require.NoError(t, err)
	suppressed, _, err := repo.SuppressedOccurrence(ctx, s.ID, s.RunAt)
	require.NoError(t, err)
	require.False(t, suppressed)
	_, err = repo.ApplyCalendarAction(ctx, "default", models.ScheduleCalendarAction{Action: "pause_all"}, day)
	require.NoError(t, err)
	// Project pause must cover newly created schedules too without modifying enabled.
	later := &models.Schedule{TaskID: task.ID, RunAt: day.Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, repo.Create(ctx, later))
	suppressed, _, err = repo.SuppressedOccurrence(ctx, later.ID, later.RunAt)
	require.NoError(t, err)
	require.True(t, suppressed)
	_, err = repo.ApplyCalendarAction(ctx, "default", models.ScheduleCalendarAction{Action: "pause", ScheduleIDs: []string{s.ID}}, day)
	require.NoError(t, err)
	_, err = repo.ApplyCalendarAction(ctx, "default", models.ScheduleCalendarAction{Action: "resume_all"}, day.Add(3*time.Hour))
	require.NoError(t, err)
	paused, err := repo.GetByID(ctx, s.ID)
	require.NoError(t, err)
	require.False(t, paused.Enabled)
	active, err := repo.GetByID(ctx, later.ID)
	require.NoError(t, err)
	require.True(t, active.Enabled)
	// A pause that ended while the scheduler was offline still suppresses missed runs.
	suppressed, _, err = repo.SuppressedOccurrence(ctx, later.ID, later.RunAt)
	require.NoError(t, err)
	require.True(t, suppressed)
	suppressed, _, err = repo.SuppressedOccurrence(ctx, later.ID, day.Add(3*time.Hour))
	require.NoError(t, err)
	require.False(t, suppressed)
}

func TestScheduleCalendarResumeDoesNotReplayMissedRuns(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewScheduleRepo(db)
	tasks := NewTaskRepo(db, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	task := createTestTask(t, tasks)
	for _, repeat := range []models.RepeatType{models.RepeatOnce, models.RepeatMinutes} {
		s := &models.Schedule{TaskID: task.ID, RunAt: now.Add(-time.Hour), RepeatType: repeat, RepeatInterval: 1, Enabled: false}
		require.NoError(t, repo.Create(ctx, s))
		_, err := repo.ApplyCalendarAction(ctx, "default", models.ScheduleCalendarAction{Action: "resume", ScheduleIDs: []string{s.ID}}, now)
		require.NoError(t, err)
		got, err := repo.GetByID(ctx, s.ID)
		require.NoError(t, err)
		if repeat == models.RepeatOnce {
			require.Nil(t, got.NextRun)
		} else {
			require.True(t, got.NextRun.After(now))
		}
	}
}

func TestCalendarDispatchAdmissionRechecksBlackout(t *testing.T) {
	for _, action := range []string{"pause", "pause_all", "skip"} {
		t.Run(action, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			repo := NewScheduleRepo(db)
			tasks := NewTaskRepo(db, nil)
			task := createTestTask(t, tasks)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Second)
			schedule := &models.Schedule{TaskID: task.ID, RunAt: now, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
			require.NoError(t, repo.Create(ctx, schedule))
			suppressed, _, err := repo.SuppressedOccurrence(ctx, schedule.ID, now)
			require.NoError(t, err)
			require.False(t, suppressed)
			request := models.ScheduleCalendarAction{Action: action}
			if action == "pause" {
				request.ScheduleIDs = []string{schedule.ID}
			}
			if action == "skip" {
				request.Skips = []models.ScheduleSkip{{ScheduleID: schedule.ID, StartAt: now.Unix(), EndAt: now.Add(time.Hour).Unix()}}
			}
			inverse, err := repo.ApplyCalendarAction(ctx, "default", request, now)
			require.NoError(t, err)
			admitted, err := repo.ClaimCalendarOccurrence(ctx, *schedule, now, nil)
			require.NoError(t, err)
			require.False(t, admitted, "a stale preflight must not authorize ordinary dispatch")
			_, _, err = NewAutomationRepo(db).ClaimScheduledOccurrence(ctx, *schedule, now, nil)
			require.ErrorIs(t, err, ErrAutomationScheduleChanged, "automation admission must reject before loading ownership")
			stored, err := repo.GetByID(ctx, schedule.ID)
			require.NoError(t, err)
			require.Nil(t, stored.LastRun)
			require.Equal(t, schedule.NextRun, stored.NextRun)
			// Restore without consuming this due occurrence, then admit exactly once.
			if action == "pause" {
				_, err = repo.ApplyCalendarAction(ctx, "default", inverse, now.Add(-time.Second))
			} else {
				_, err = repo.ApplyCalendarAction(ctx, "default", inverse, now)
			}
			require.NoError(t, err)
			if action == "pause_all" {
				return
			} // resume-all deliberately consumes overdue runs
			admitted, err = repo.ClaimCalendarOccurrence(ctx, *schedule, now, nil)
			require.NoError(t, err)
			require.True(t, admitted)
			admitted, err = repo.ClaimCalendarOccurrence(ctx, *schedule, now, nil)
			require.NoError(t, err)
			require.False(t, admitted, "same occurrence is admitted only once")
		})
	}
}

func TestAutomationCalendarAdmissionRechecksBlackout(t *testing.T) {
	for _, action := range []string{"pause_all", "skip"} {
		t.Run(action, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := context.Background()
			fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
			tasks := NewTaskRepo(db, nil)
			task := createRuntimeScheduledTask(t, ctx, tasks, fixture.ProjectID, "Calendar admission")
			now := time.Now().UTC().Truncate(time.Second)
			schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], now.Add(-time.Minute))
			schedules := NewScheduleRepo(db)
			suppressed, _, err := schedules.SuppressedOccurrence(ctx, schedule.ID, *schedule.NextRun)
			require.NoError(t, err)
			require.False(t, suppressed)
			request := models.ScheduleCalendarAction{Action: action}
			if action == "skip" {
				request.Skips = []models.ScheduleSkip{{ScheduleID: schedule.ID, StartAt: schedule.NextRun.Unix(), EndAt: now.Add(time.Hour).Unix()}}
			}
			_, err = schedules.ApplyCalendarAction(ctx, fixture.ProjectID, request, now)
			require.NoError(t, err)
			invocation, dispatch, err := NewAutomationRepo(db).ClaimScheduledOccurrence(ctx, schedule, now, schedule.ComputeNextRun(now))
			require.ErrorIs(t, err, ErrAutomationScheduleChanged)
			require.Nil(t, invocation)
			require.Nil(t, dispatch)
			var count int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_invocations WHERE automation_id = ?`, fixture.AutomationID).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestCalendarAdmissionReleasePreservesLaterEdit(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewScheduleRepo(db)
	ctx := context.Background()
	task := createTestTask(t, NewTaskRepo(db, nil))
	now := time.Now().UTC().Truncate(time.Second)
	schedule := &models.Schedule{TaskID: task.ID, RunAt: now, RepeatType: models.RepeatHours, RepeatInterval: 1, Enabled: true}
	require.NoError(t, repo.Create(ctx, schedule))
	next := schedule.ComputeNextRun(now)
	admitted, err := repo.ClaimCalendarOccurrence(ctx, *schedule, now, next)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NoError(t, repo.ReleaseCalendarOccurrence(ctx, *schedule, now, next))
	stored, err := repo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.Equal(t, schedule.NextRun, stored.NextRun)
	require.Nil(t, stored.LastRun)
	admitted, err = repo.ClaimCalendarOccurrence(ctx, *schedule, now, next)
	require.NoError(t, err)
	require.True(t, admitted)
	editedNext := now.Add(2 * time.Hour)
	changed, err := repo.UpdateNextRunIfCurrent(ctx, schedule.ID, task.ID, next, &editedNext)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, repo.ReleaseCalendarOccurrence(ctx, *schedule, now, next))
	stored, err = repo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.True(t, editedNext.Equal(*stored.NextRun))
}

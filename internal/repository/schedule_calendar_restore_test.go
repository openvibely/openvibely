package repository

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestScheduleCalendarRestoreRunWithinSkippedDayAndUndo(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewScheduleRepo(db)
	ctx := context.Background()
	task := createTestTask(t, NewTaskRepo(db, nil))
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	makeSchedule := func() *models.Schedule {
		s := &models.Schedule{TaskID: task.ID, RunAt: day.Add(time.Hour), RepeatType: models.RepeatHours, RepeatInterval: 1, Enabled: true}
		require.NoError(t, repo.Create(ctx, s))
		return s
	}
	a, b := makeSchedule(), makeSchedule()
	fullDay := models.ScheduleSkip{StartAt: day.Unix(), EndAt: day.AddDate(0, 0, 1).Unix()}
	_, err := repo.ApplyCalendarAction(ctx, "default", models.ScheduleCalendarAction{Action: "skip", Skips: []models.ScheduleSkip{fullDay}}, day)
	require.NoError(t, err)
	run := models.ScheduleSkip{ScheduleID: a.ID, StartAt: day.Add(time.Hour).Unix(), EndAt: day.Add(2 * time.Hour).Unix()}
	undo, err := repo.ApplyCalendarAction(ctx, "default", models.ScheduleCalendarAction{Action: "restore", Skips: []models.ScheduleSkip{run}}, day)
	require.NoError(t, err)
	assertSuppressed := func(s *models.Schedule, at time.Time, want bool) {
		t.Helper()
		got, _, err := repo.SuppressedOccurrence(ctx, s.ID, at)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	assertSuppressed(a, a.RunAt, false)
	assertSuppressed(b, b.RunAt, true)
	assertSuppressed(a, day.Add(2*time.Hour), true)
	_, err = repo.ApplyCalendarAction(ctx, "default", undo, day)
	require.NoError(t, err)
	assertSuppressed(a, a.RunAt, true)
	// Restoration and skipping the same run can both be undone without losing the
	// surrounding date exclusion or another schedule's state.
	_, err = repo.ApplyCalendarAction(ctx, "default", models.ScheduleCalendarAction{Action: "restore", Skips: []models.ScheduleSkip{run}}, day)
	require.NoError(t, err)
	undo, err = repo.ApplyCalendarAction(ctx, "default", models.ScheduleCalendarAction{Action: "skip", Skips: []models.ScheduleSkip{run}}, day)
	require.NoError(t, err)
	assertSuppressed(a, a.RunAt, true)
	_, err = repo.ApplyCalendarAction(ctx, "default", undo, day)
	require.NoError(t, err)
	assertSuppressed(a, a.RunAt, false)
	// Restoring a date clears the date skip and its schedule-specific exceptions.
	_, err = repo.ApplyCalendarAction(ctx, "default", models.ScheduleCalendarAction{Action: "restore", Skips: []models.ScheduleSkip{fullDay}}, day)
	require.NoError(t, err)
	state, err := repo.CalendarState(ctx, "default")
	require.NoError(t, err)
	require.Empty(t, state.Skips)
}

func TestScheduleCalendarResumeAllAdvancesOfflineMissedRuns(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewScheduleRepo(db)
	ctx := context.Background()
	task := createTestTask(t, NewTaskRepo(db, nil))
	now := time.Now().UTC().Truncate(time.Second)
	sched := &models.Schedule{TaskID: task.ID, RunAt: now.Add(-time.Hour), RepeatType: models.RepeatMinutes, RepeatInterval: 1, Enabled: true}
	require.NoError(t, repo.Create(ctx, sched))
	_, err := repo.ApplyCalendarAction(ctx, "default", models.ScheduleCalendarAction{Action: "pause_all"}, now)
	require.NoError(t, err)
	resumed := now.Add(2 * time.Hour)
	_, err = repo.ApplyCalendarAction(ctx, "default", models.ScheduleCalendarAction{Action: "resume_all"}, resumed)
	require.NoError(t, err)
	got, err := repo.GetByID(ctx, sched.ID)
	require.NoError(t, err)
	require.True(t, got.NextRun.After(resumed))
	require.Nil(t, got.LastRun)
}

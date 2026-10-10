package pages

import (
	"fmt"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestScheduleCardIntervalRepeatedHour(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	first := time.Date(2026, 11, 1, 5, 50, 0, 0, time.UTC).In(loc)
	second := first.Add(time.Hour)
	task := repository.TaskWithSchedule{Schedule: &models.Schedule{ID: "hourly", RepeatType: models.RepeatHours, RepeatInterval: 1}}
	a := scheduleCardInterval(TaskOccurrence{Task: task, OccurrenceTime: first})
	b := scheduleCardInterval(TaskOccurrence{Task: task, OccurrenceTime: second})
	require.Equal(t, a, b)
	require.Equal(t, int64(7200), a.EndAt-a.StartAt)
	require.Less(t, second.Unix(), a.EndAt)
	normal := scheduleCardInterval(TaskOccurrence{Task: task, OccurrenceTime: first.AddDate(0, 0, 1)})
	require.Equal(t, int64(3600), normal.EndAt-normal.StartAt)
}

func TestScheduleHourIntervalDST(t *testing.T) {
	for _, tc := range []struct {
		zone      string
		year      int
		month     time.Month
		day, hour int
		seconds   int64
	}{
		{"America/New_York", 2026, time.March, 8, 2, 0},
		{"America/New_York", 2026, time.November, 1, 1, 7200},
		{"America/New_York", 2026, time.November, 1, 23, 3600},
		{"Australia/Lord_Howe", 2026, time.April, 5, 1, 5400},
		{"Australia/Lord_Howe", 2026, time.October, 4, 2, 1800},
	} {
		t.Run(fmt.Sprintf("%s/%d/%d", tc.zone, tc.month, tc.hour), func(t *testing.T) {
			loc, err := time.LoadLocation(tc.zone)
			require.NoError(t, err)
			day := time.Date(tc.year, tc.month, tc.day, 0, 0, 0, 0, loc)
			run := scheduleHourInterval(day, tc.hour)
			require.Equal(t, tc.seconds, run.EndAt-run.StartAt)
			if tc.seconds == 0 {
				require.Zero(t, run.StartAt)
				return
			}
			require.Equal(t, tc.hour, time.Unix(run.StartAt, 0).In(loc).Hour())
			require.Equal(t, tc.hour, time.Unix(run.EndAt-1, 0).In(loc).Hour())
			require.GreaterOrEqual(t, run.StartAt, day.Unix())
			require.LessOrEqual(t, run.EndAt, day.AddDate(0, 0, 1).Unix())
		})
	}
}

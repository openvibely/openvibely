package pages

import (
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

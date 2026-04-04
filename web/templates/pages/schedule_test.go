package pages

import (
	"fmt"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// localKey returns the expected occurrence map key for a UTC time after local conversion.
func localKey(t time.Time) string {
	local := t.Local()
	return fmt.Sprintf("%s-%02d", local.Format("2006-01-02"), local.Hour())
}

// TestBuildTaskOccurrenceMap_UsesNextRunForRecurring verifies that for recurring tasks,
// the timeline uses NextRun as the starting point for generating occurrences.
// After rescheduling, both RunAt and NextRun are updated to the new time.
func TestBuildTaskOccurrenceMap_UsesNextRunForRecurring(t *testing.T) {
	// Setup: Create a daily recurring task that has been rescheduled
	startOfWeek := time.Date(2026, 2, 16, 0, 0, 0, 0, time.Local) // Sunday

	// After user rescheduled via drag-and-drop to Wednesday 6:31 PM local
	rescheduledTime := time.Date(2026, 2, 19, 18, 31, 0, 0, time.Local)
	rescheduledUTC := rescheduledTime.UTC()

	task := repository.TaskWithSchedule{
		Task: models.Task{
			ID:        "task1",
			ProjectID: "project1",
			Title:     "Daily Task",
		},
		Schedule: &models.Schedule{
			ID:             "schedule1",
			TaskID:         "task1",
			RunAt:          rescheduledUTC, // DB stores UTC
			NextRun:        &rescheduledUTC,
			RepeatType:     models.RepeatDaily,
			RepeatInterval: 1,
			Enabled:        true,
		},
	}

	occurrenceMap := buildTaskOccurrenceMap([]repository.TaskWithSchedule{task}, startOfWeek)

	// Should appear at the rescheduled local time
	key := localKey(rescheduledUTC)
	occurrences := occurrenceMap[key]

	if len(occurrences) == 0 {
		t.Errorf("Expected task occurrence at %s, but found none", key)
		for k, v := range occurrenceMap {
			t.Logf("Found occurrences at %s: %d items", k, len(v))
			for _, occ := range v {
				t.Logf("  - %s at %s", occ.Task.Task.Title, occ.OccurrenceTime.Format("2006-01-02 15:04"))
			}
		}
		return
	}

	if len(occurrences) != 1 {
		t.Errorf("Expected 1 occurrence at %s, got %d", key, len(occurrences))
	}
}

// TestBuildTaskOccurrenceMap_RecurringWithoutNextRun verifies that for new recurring schedules
// where NextRun is nil, the timeline falls back to using RunAt.
func TestBuildTaskOccurrenceMap_RecurringWithoutNextRun(t *testing.T) {
	startOfWeek := time.Date(2026, 2, 16, 0, 0, 0, 0, time.Local) // Sunday

	runAt := time.Date(2026, 2, 18, 9, 30, 0, 0, time.Local) // Tuesday, 9:30 AM local
	runAtUTC := runAt.UTC()

	task := repository.TaskWithSchedule{
		Task: models.Task{
			ID:        "task1",
			ProjectID: "project1",
			Title:     "New Daily Task",
		},
		Schedule: &models.Schedule{
			ID:             "schedule1",
			TaskID:         "task1",
			RunAt:          runAtUTC,
			NextRun:        nil, // New schedule, not yet executed
			RepeatType:     models.RepeatDaily,
			RepeatInterval: 1,
			Enabled:        true,
		},
	}

	occurrenceMap := buildTaskOccurrenceMap([]repository.TaskWithSchedule{task}, startOfWeek)

	key := localKey(runAtUTC)
	occurrences := occurrenceMap[key]

	if len(occurrences) == 0 {
		t.Errorf("Expected task occurrence at %s, but found none", key)
		return
	}

	if len(occurrences) != 1 {
		t.Errorf("Expected 1 occurrence, got %d", len(occurrences))
	}
}

// TestBuildTaskOccurrenceMap_OneTimeTask verifies that one-time tasks use NextRun
func TestBuildTaskOccurrenceMap_OneTimeTask(t *testing.T) {
	startOfWeek := time.Date(2026, 2, 16, 0, 0, 0, 0, time.Local)

	runAt := time.Date(2026, 2, 17, 14, 0, 0, 0, time.Local)
	runAtUTC := runAt.UTC()
	nextRun := time.Date(2026, 2, 20, 15, 0, 0, 0, time.Local)
	nextRunUTC := nextRun.UTC()

	task := repository.TaskWithSchedule{
		Task: models.Task{
			ID:        "task1",
			ProjectID: "project1",
			Title:     "One-time Task",
		},
		Schedule: &models.Schedule{
			ID:         "schedule1",
			TaskID:     "task1",
			RunAt:      runAtUTC,
			NextRun:    &nextRunUTC,
			RepeatType: models.RepeatOnce,
			Enabled:    true,
		},
	}

	occurrenceMap := buildTaskOccurrenceMap([]repository.TaskWithSchedule{task}, startOfWeek)

	key := localKey(nextRunUTC)
	occurrences := occurrenceMap[key]

	if len(occurrences) != 1 {
		t.Errorf("Expected 1 occurrence at NextRun time %s, got %d", key, len(occurrences))
	}
}

// TestBuildTaskOccurrenceMap_OneTimeTaskWithoutNextRun verifies that one-time tasks
// that haven't run yet (NextRun is nil) use RunAt for display
func TestBuildTaskOccurrenceMap_OneTimeTaskWithoutNextRun(t *testing.T) {
	startOfWeek := time.Date(2026, 2, 16, 0, 0, 0, 0, time.Local)

	runAt := time.Date(2026, 2, 22, 14, 0, 0, 0, time.Local)
	runAtUTC := runAt.UTC()

	task := repository.TaskWithSchedule{
		Task: models.Task{
			ID:        "task1",
			ProjectID: "project1",
			Title:     "New One-time Task",
		},
		Schedule: &models.Schedule{
			ID:         "schedule1",
			TaskID:     "task1",
			RunAt:      runAtUTC,
			NextRun:    nil, // Not yet executed
			RepeatType: models.RepeatOnce,
			Enabled:    true,
		},
	}

	occurrenceMap := buildTaskOccurrenceMap([]repository.TaskWithSchedule{task}, startOfWeek)

	key := localKey(runAtUTC)
	occurrences := occurrenceMap[key]

	if len(occurrences) == 0 {
		t.Errorf("Expected task occurrence at %s (RunAt time), but found none", key)
		for k, v := range occurrenceMap {
			t.Logf("Found occurrences at %s: %d items", k, len(v))
		}
		return
	}

	if len(occurrences) != 1 {
		t.Errorf("Expected 1 occurrence at RunAt time, got %d", len(occurrences))
	}
}

// TestBuildTaskOccurrenceMap_ShowsTodayAfterExecution verifies that tasks that executed
// today still show up in the timeline (user's reported issue)
func TestBuildTaskOccurrenceMap_ShowsTodayAfterExecution(t *testing.T) {
	// Week of Feb 22-28, 2026 (Sunday to Saturday)
	startOfWeek := time.Date(2026, 2, 22, 0, 0, 0, 0, time.Local)

	// Task was created to run daily at 2 PM local, starting today (Sunday)
	runAt := time.Date(2026, 2, 22, 14, 0, 0, 0, time.Local)
	runAtUTC := runAt.UTC()

	// Task executed at 2 PM today, NextRun is now set to tomorrow
	nextRun := time.Date(2026, 2, 23, 14, 0, 0, 0, time.Local)
	nextRunUTC := nextRun.UTC()

	task := repository.TaskWithSchedule{
		Task: models.Task{
			ID:        "task1",
			ProjectID: "project1",
			Title:     "Daily Task",
		},
		Schedule: &models.Schedule{
			ID:             "schedule1",
			TaskID:         "task1",
			RunAt:          runAtUTC,
			NextRun:        &nextRunUTC,
			RepeatType:     models.RepeatDaily,
			RepeatInterval: 1,
			Enabled:        true,
		},
	}

	occurrenceMap := buildTaskOccurrenceMap([]repository.TaskWithSchedule{task}, startOfWeek)

	// Verify all 7 days of the week are included
	for i := 0; i < 7; i++ {
		dayTime := time.Date(2026, 2, 22+i, 14, 0, 0, 0, time.Local)
		key := fmt.Sprintf("%s-%02d", dayTime.Format("2006-01-02"), dayTime.Hour())
		occurrences := occurrenceMap[key]
		if len(occurrences) != 1 {
			t.Errorf("Expected 1 occurrence on day %d (%s), got %d", i, key, len(occurrences))
		}
	}
}

// TestBuildTaskOccurrenceMap_PastWeek verifies that daily tasks show on all 7 days
// when viewing a past week, even when RunAt/NextRun is in the future.
// This was a bug where ComputeNextRun (anchored to RunAt) would jump directly
// to RunAt, skipping all intermediate days in the past week.
func TestBuildTaskOccurrenceMap_PastWeek(t *testing.T) {
	// Viewing previous week (Feb 15-21)
	startOfWeek := time.Date(2026, 2, 15, 0, 0, 0, 0, time.Local) // Sunday

	// Task was created for 11 PM local, RunAt and NextRun are in the FUTURE (current week)
	runAt := time.Date(2026, 2, 23, 23, 0, 0, 0, time.Local) // Feb 23, 11 PM local
	runAtUTC := runAt.UTC()
	nextRun := time.Date(2026, 2, 24, 23, 0, 0, 0, time.Local) // Feb 24, 11 PM local
	nextRunUTC := nextRun.UTC()

	task := repository.TaskWithSchedule{
		Task: models.Task{
			ID:        "task1",
			ProjectID: "project1",
			Title:     "Daily 11PM Task",
		},
		Schedule: &models.Schedule{
			ID:             "schedule1",
			TaskID:         "task1",
			RunAt:          runAtUTC,
			NextRun:        &nextRunUTC,
			RepeatType:     models.RepeatDaily,
			RepeatInterval: 1,
			Enabled:        true,
		},
	}

	occurrenceMap := buildTaskOccurrenceMap([]repository.TaskWithSchedule{task}, startOfWeek)

	// Verify all 7 days of the previous week have occurrences at 11 PM local
	for i := 0; i < 7; i++ {
		dayTime := time.Date(2026, 2, 15+i, 23, 0, 0, 0, time.Local)
		key := fmt.Sprintf("%s-%02d", dayTime.Format("2006-01-02"), dayTime.Hour())
		occurrences := occurrenceMap[key]
		if len(occurrences) != 1 {
			t.Errorf("Day %d (%s): expected 1 occurrence, got %d", i, key, len(occurrences))
			if len(occurrenceMap) < 10 {
				for k, v := range occurrenceMap {
					t.Logf("  Found: %s (%d items)", k, len(v))
				}
			}
		}
	}

	// Verify total count is exactly 7
	total := 0
	for _, v := range occurrenceMap {
		total += len(v)
	}
	if total != 7 {
		t.Errorf("Expected 7 total occurrences for the week, got %d", total)
	}
}

// TestBuildTaskOccurrenceMap_FutureWeek verifies that daily tasks show on all 7 days
// when viewing a future week, with consistent local time-of-day.
func TestBuildTaskOccurrenceMap_FutureWeek(t *testing.T) {
	// Viewing a future week (Mar 15-21, well past any DST change)
	startOfWeek := time.Date(2026, 3, 15, 0, 0, 0, 0, time.Local) // Sunday

	// Task scheduled for 11 PM local
	runAt := time.Date(2026, 2, 23, 23, 0, 0, 0, time.Local)
	runAtUTC := runAt.UTC()
	nextRun := time.Date(2026, 2, 24, 23, 0, 0, 0, time.Local)
	nextRunUTC := nextRun.UTC()

	task := repository.TaskWithSchedule{
		Task: models.Task{
			ID:        "task1",
			ProjectID: "project1",
			Title:     "Daily 11PM Task",
		},
		Schedule: &models.Schedule{
			ID:             "schedule1",
			TaskID:         "task1",
			RunAt:          runAtUTC,
			NextRun:        &nextRunUTC,
			RepeatType:     models.RepeatDaily,
			RepeatInterval: 1,
			Enabled:        true,
		},
	}

	occurrenceMap := buildTaskOccurrenceMap([]repository.TaskWithSchedule{task}, startOfWeek)

	// Verify all 7 days show at 11 PM local (consistent time regardless of DST)
	for i := 0; i < 7; i++ {
		dayTime := time.Date(2026, 3, 15+i, 23, 0, 0, 0, time.Local)
		key := fmt.Sprintf("%s-%02d", dayTime.Format("2006-01-02"), dayTime.Hour())
		occurrences := occurrenceMap[key]
		if len(occurrences) != 1 {
			t.Errorf("Day %d (%s): expected 1 occurrence at 11 PM local, got %d", i, key, len(occurrences))
		}
	}

	total := 0
	for _, v := range occurrenceMap {
		total += len(v)
	}
	if total != 7 {
		t.Errorf("Expected 7 total occurrences for the week, got %d", total)
	}
}

// TestBuildTaskOccurrenceMap_SortedByTimeWithinHour verifies that tasks within the same
// hour slot are sorted by their actual occurrence time. For example, a task at 11:00 PM
// should appear before a task at 11:30 PM in the same hour slot.
func TestBuildTaskOccurrenceMap_SortedByTimeWithinHour(t *testing.T) {
	startOfWeek := time.Date(2026, 2, 22, 0, 0, 0, 0, time.Local) // Sunday

	// Task "one" at 11:30 PM local
	runAt1 := time.Date(2026, 2, 23, 23, 30, 0, 0, time.Local)
	runAt1UTC := runAt1.UTC()
	nextRun1UTC := runAt1UTC

	// Task "dail" at 11:00 PM local (earlier, but listed second in the input slice)
	runAt2 := time.Date(2026, 2, 23, 23, 0, 0, 0, time.Local)
	runAt2UTC := runAt2.UTC()
	nextRun2UTC := runAt2UTC

	tasks := []repository.TaskWithSchedule{
		{
			Task: models.Task{
				ID:        "task1",
				ProjectID: "project1",
				Title:     "one",
			},
			Schedule: &models.Schedule{
				ID:         "schedule1",
				TaskID:     "task1",
				RunAt:      runAt1UTC,
				NextRun:    &nextRun1UTC,
				RepeatType: models.RepeatOnce,
				Enabled:    true,
			},
		},
		{
			Task: models.Task{
				ID:        "task2",
				ProjectID: "project1",
				Title:     "dail",
			},
			Schedule: &models.Schedule{
				ID:             "schedule2",
				TaskID:         "task2",
				RunAt:          runAt2UTC,
				NextRun:        &nextRun2UTC,
				RepeatType:     models.RepeatDaily,
				RepeatInterval: 1,
				Enabled:        true,
			},
		},
	}

	occurrenceMap := buildTaskOccurrenceMap(tasks, startOfWeek)

	// Both tasks should be in the 11 PM slot on Feb 23
	dayTime := time.Date(2026, 2, 23, 23, 0, 0, 0, time.Local)
	key := fmt.Sprintf("%s-%02d", dayTime.Format("2006-01-02"), dayTime.Hour())
	occurrences := occurrenceMap[key]

	if len(occurrences) < 2 {
		t.Fatalf("Expected at least 2 occurrences at %s, got %d", key, len(occurrences))
	}

	// "dail" (11:00 PM) should come before "one" (11:30 PM)
	if occurrences[0].Task.Task.Title != "dail" {
		t.Errorf("Expected first task to be 'dail' (11:00 PM), got '%s' at %s",
			occurrences[0].Task.Task.Title, occurrences[0].OccurrenceTime.Format("3:04 PM"))
	}
	if occurrences[1].Task.Task.Title != "one" {
		t.Errorf("Expected second task to be 'one' (11:30 PM), got '%s' at %s",
			occurrences[1].Task.Task.Title, occurrences[1].OccurrenceTime.Format("3:04 PM"))
	}

	// Verify times are actually ordered
	if !occurrences[0].OccurrenceTime.Before(occurrences[1].OccurrenceTime) {
		t.Errorf("Tasks not sorted by time: %s should be before %s",
			occurrences[0].OccurrenceTime.Format("3:04 PM"),
			occurrences[1].OccurrenceTime.Format("3:04 PM"))
	}
}

// TestBuildTaskOccurrenceMap_WeeklyPastWeek verifies that weekly tasks show correctly
// when viewing a past week.
func TestBuildTaskOccurrenceMap_WeeklyPastWeek(t *testing.T) {
	// Viewing the week of Feb 8-14
	startOfWeek := time.Date(2026, 2, 8, 0, 0, 0, 0, time.Local)

	// Weekly task on Wednesday at 3 PM local
	runAt := time.Date(2026, 2, 25, 15, 0, 0, 0, time.Local) // Future Wednesday
	runAtUTC := runAt.UTC()
	nextRunUTC := runAtUTC

	task := repository.TaskWithSchedule{
		Task: models.Task{
			ID:        "task1",
			ProjectID: "project1",
			Title:     "Weekly Wednesday Task",
		},
		Schedule: &models.Schedule{
			ID:             "schedule1",
			TaskID:         "task1",
			RunAt:          runAtUTC,
			NextRun:        &nextRunUTC,
			RepeatType:     models.RepeatWeekly,
			RepeatInterval: 1,
			Enabled:        true,
		},
	}

	occurrenceMap := buildTaskOccurrenceMap([]repository.TaskWithSchedule{task}, startOfWeek)

	// Should show on Wednesday Feb 11 at 3 PM local
	wedTime := time.Date(2026, 2, 11, 15, 0, 0, 0, time.Local)
	key := fmt.Sprintf("%s-%02d", wedTime.Format("2006-01-02"), wedTime.Hour())
	occurrences := occurrenceMap[key]

	if len(occurrences) != 1 {
		t.Errorf("Expected 1 occurrence on Wednesday at 3 PM (%s), got %d", key, len(occurrences))
		for k, v := range occurrenceMap {
			t.Logf("Found: %s (%d items)", k, len(v))
		}
	}

	// Should be exactly 1 occurrence for the whole week (weekly task)
	total := 0
	for _, v := range occurrenceMap {
		total += len(v)
	}
	if total != 1 {
		t.Errorf("Expected 1 total occurrence for weekly task, got %d", total)
	}
}

// TestBuildTaskOccurrenceMap_ProductionData simulates the exact production database data
// to verify that the occurrence map matches what we'd expect to see in the UI.
func TestBuildTaskOccurrenceMap_ProductionData(t *testing.T) {
	// Week of Feb 22-28, 2026 (system timezone is EST = UTC-5)
	startOfWeek := time.Date(2026, 2, 22, 0, 0, 0, 0, time.Local)

	// DB data converted to time.Time (stored as UTC in DB)
	// 1. "dail" - daily/1, run_at=2026-02-27 04:01:00 UTC, next_run=2026-02-27 04:01:00 UTC
	dailRunAt := time.Date(2026, 2, 27, 4, 1, 0, 0, time.UTC)
	dailNextRun := time.Date(2026, 2, 27, 4, 1, 0, 0, time.UTC)

	// 2. "week" - weekly/1, run_at=2026-02-23 17:00:00 UTC, next_run=2026-03-02 17:00:00 UTC
	weekRunAt := time.Date(2026, 2, 23, 17, 0, 0, 0, time.UTC)
	weekNextRun := time.Date(2026, 3, 2, 17, 0, 0, 0, time.UTC)

	// 3. "one" - once/1, run_at=2026-02-24 04:30:00 UTC, next_run=nil (already ran)
	oneRunAt := time.Date(2026, 2, 24, 4, 30, 0, 0, time.UTC)

	// 4. "Run Tests" - hours/1, run_at=2026-02-27 02:05:00 UTC, next_run=2026-02-27 02:05:00 UTC
	rtRunAt := time.Date(2026, 2, 27, 2, 5, 0, 0, time.UTC)
	rtNextRun := time.Date(2026, 2, 27, 2, 5, 0, 0, time.UTC)

	// 5. "Interval" - daily/2, run_at=2026-02-26 02:26:00 UTC, next_run=2026-02-28 02:26:00 UTC
	intRunAt := time.Date(2026, 2, 26, 2, 26, 0, 0, time.UTC)
	intNextRun := time.Date(2026, 2, 28, 2, 26, 0, 0, time.UTC)

	tasks := []repository.TaskWithSchedule{
		{
			Task:     models.Task{ID: "t1", ProjectID: "p1", Title: "dail"},
			Schedule: &models.Schedule{ID: "s1", TaskID: "t1", RunAt: dailRunAt, NextRun: &dailNextRun, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true},
		},
		{
			Task:     models.Task{ID: "t2", ProjectID: "p1", Title: "week"},
			Schedule: &models.Schedule{ID: "s2", TaskID: "t2", RunAt: weekRunAt, NextRun: &weekNextRun, RepeatType: models.RepeatWeekly, RepeatInterval: 1, Enabled: true},
		},
		{
			Task:     models.Task{ID: "t3", ProjectID: "p1", Title: "one"},
			Schedule: &models.Schedule{ID: "s3", TaskID: "t3", RunAt: oneRunAt, NextRun: nil, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true},
		},
		{
			Task:     models.Task{ID: "t4", ProjectID: "p1", Title: "Run Tests"},
			Schedule: &models.Schedule{ID: "s4", TaskID: "t4", RunAt: rtRunAt, NextRun: &rtNextRun, RepeatType: models.RepeatHours, RepeatInterval: 1, Enabled: true},
		},
		{
			Task:     models.Task{ID: "t5", ProjectID: "p1", Title: "Interval"},
			Schedule: &models.Schedule{ID: "s5", TaskID: "t5", RunAt: intRunAt, NextRun: &intNextRun, RepeatType: models.RepeatDaily, RepeatInterval: 2, Enabled: true},
		},
	}

	occurrenceMap := buildTaskOccurrenceMap(tasks, startOfWeek)

	// Log all generated keys for debugging
	t.Log("=== All occurrence map entries ===")
	for _, day := range []int{22, 23, 24, 25, 26, 27, 28} {
		for hour := 0; hour < 24; hour++ {
			key := fmt.Sprintf("2026-02-%02d-%02d", day, hour)
			if occs, ok := occurrenceMap[key]; ok {
				for _, occ := range occs {
					dayTime := time.Date(2026, 2, day, 0, 0, 0, 0, time.Local)
					t.Logf("  %s %s hour=%d: %s at %s",
						dayTime.Weekday().String()[:3],
						key, hour,
						occ.Task.Task.Title,
						occ.OccurrenceTime.Format("3:04 PM"))
				}
			}
		}
	}

	// Expected for local time (EST = UTC-5):
	// "dail" run_at=Feb 27 04:01 UTC = Feb 26 11:01 PM EST → daily at 11 PM, all 7 days
	// "week" next_run=Mar 2 17:00 UTC = Mar 2 12:00 PM EST → rewind to Feb 23 12 PM (Mon)
	// "one" run_at=Feb 24 04:30 UTC = Feb 23 11:30 PM EST → Mon at 11 PM
	// "Run Tests" next_run=Feb 27 02:05 UTC = Feb 26 9:05 PM EST → sub-daily from Thu 9 PM
	// "Interval" next_run=Feb 28 02:26 UTC = Feb 27 9:26 PM EST → Mon/Wed/Fri at 9 PM

	// Check "week" is on the correct day
	weekLocalTime := weekNextRun.Local() // Mar 2 local
	// Rewind by 7 days should give Feb 23 local
	weekExpectedLocal := weekLocalTime.AddDate(0, 0, -7)
	weekKey := fmt.Sprintf("%s-%02d", weekExpectedLocal.Format("2006-01-02"), weekExpectedLocal.Hour())
	if occs := occurrenceMap[weekKey]; len(occs) == 0 {
		t.Errorf("WEEK: expected occurrence at %s, but found none", weekKey)
	} else {
		found := false
		for _, occ := range occs {
			if occ.Task.Task.Title == "week" {
				found = true
				t.Logf("WEEK: found at %s (%s)", weekKey, occ.OccurrenceTime.Format("Mon Jan 2 3:04 PM"))
			}
		}
		if !found {
			t.Errorf("WEEK: key %s exists but doesn't contain 'week' task", weekKey)
		}
	}

	// Check "dail" has 7 occurrences (one per day at 11 PM)
	dailLocal := dailRunAt.Local()
	dailCount := 0
	for day := 22; day <= 28; day++ {
		dayTime := time.Date(2026, 2, day, dailLocal.Hour(), 0, 0, 0, time.Local)
		key := fmt.Sprintf("%s-%02d", dayTime.Format("2006-01-02"), dayTime.Hour())
		for _, occ := range occurrenceMap[key] {
			if occ.Task.Task.Title == "dail" {
				dailCount++
			}
		}
	}
	if dailCount != 7 {
		t.Errorf("DAIL: expected 7 daily occurrences, got %d", dailCount)
	}

	// Check "one" appears once at its RunAt time
	oneLocal := oneRunAt.Local()
	oneKey := fmt.Sprintf("%s-%02d", oneLocal.Format("2006-01-02"), oneLocal.Hour())
	oneFound := false
	for _, occ := range occurrenceMap[oneKey] {
		if occ.Task.Task.Title == "one" {
			oneFound = true
		}
	}
	if !oneFound {
		t.Errorf("ONE: expected occurrence at %s, but not found", oneKey)
	}

	// Check "Run Tests" has entries on Fri and Sat (sub-daily from Thu 9 PM)
	rtLocal := rtNextRun.Local()
	t.Logf("Run Tests refTime local: %s", rtLocal.Format("Mon Jan 2 3:04 PM"))
	rtFriCount := 0
	rtSatCount := 0
	rtThuCount := 0
	for hour := 0; hour < 24; hour++ {
		// Thu Feb 26
		thuKey := fmt.Sprintf("2026-02-26-%02d", hour)
		for _, occ := range occurrenceMap[thuKey] {
			if occ.Task.Task.Title == "Run Tests" {
				rtThuCount++
			}
		}
		// Fri Feb 27
		friKey := fmt.Sprintf("2026-02-27-%02d", hour)
		for _, occ := range occurrenceMap[friKey] {
			if occ.Task.Task.Title == "Run Tests" {
				rtFriCount++
			}
		}
		// Sat Feb 28
		satKey := fmt.Sprintf("2026-02-28-%02d", hour)
		for _, occ := range occurrenceMap[satKey] {
			if occ.Task.Task.Title == "Run Tests" {
				rtSatCount++
			}
		}
	}
	t.Logf("Run Tests: Thu=%d, Fri=%d, Sat=%d entries", rtThuCount, rtFriCount, rtSatCount)

	// Check "Interval" has 3 occurrences (every 2 days)
	intCount := 0
	for day := 22; day <= 28; day++ {
		for hour := 0; hour < 24; hour++ {
			key := fmt.Sprintf("2026-02-%02d-%02d", day, hour)
			for _, occ := range occurrenceMap[key] {
				if occ.Task.Task.Title == "Interval" {
					intCount++
					dayTime := time.Date(2026, 2, day, 0, 0, 0, 0, time.Local)
					t.Logf("INTERVAL: found on %s hour=%d", dayTime.Weekday().String()[:3], hour)
				}
			}
		}
	}
	if intCount != 3 {
		t.Errorf("INTERVAL: expected 3 occurrences (every 2 days), got %d", intCount)
	}
}

// TestBuildTaskOccurrenceMap_SubDailyWithLastRun verifies that a sub-daily schedule
// that has been active (LastRun is set) shows entries for the entire week, not just
// from NextRun onwards. This was the root cause of the display bug where hourly tasks
// only appeared on some days of the week.
func TestBuildTaskOccurrenceMap_SubDailyWithLastRun(t *testing.T) {
	// Week of Feb 22-28, 2026
	startOfWeek := time.Date(2026, 2, 22, 0, 0, 0, 0, time.Local)

	// Hourly schedule that has been running — RunAt is when it was originally created,
	// NextRun is somewhere mid-week, LastRun is set (schedule has been active)
	runAt := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)     // Originally created Tue
	nextRun := time.Date(2026, 2, 26, 15, 0, 0, 0, time.UTC)   // NextRun is Thu afternoon
	lastRun := time.Date(2026, 2, 26, 14, 0, 0, 0, time.UTC)   // Last ran Thu 2 PM

	task := repository.TaskWithSchedule{
		Task: models.Task{
			ID:        "hourly1",
			ProjectID: "project1",
			Title:     "Hourly Active Task",
		},
		Schedule: &models.Schedule{
			ID:             "s1",
			TaskID:         "hourly1",
			RunAt:          runAt,
			NextRun:        &nextRun,
			LastRun:        &lastRun,
			RepeatType:     models.RepeatHours,
			RepeatInterval: 1,
			Enabled:        true,
		},
	}

	occurrenceMap := buildTaskOccurrenceMap([]repository.TaskWithSchedule{task}, startOfWeek)

	// With LastRun set, sub-daily tasks should have entries starting from the
	// beginning of the week (Sun Feb 22), not from NextRun (Thu)
	sunCount := 0
	monCount := 0
	thuCount := 0
	satCount := 0

	for hour := 0; hour < 24; hour++ {
		sunKey := fmt.Sprintf("2026-02-22-%02d", hour)
		monKey := fmt.Sprintf("2026-02-23-%02d", hour)
		thuKey := fmt.Sprintf("2026-02-26-%02d", hour)
		satKey := fmt.Sprintf("2026-02-28-%02d", hour)

		for _, occ := range occurrenceMap[sunKey] {
			if occ.Task.Task.Title == "Hourly Active Task" {
				sunCount++
			}
		}
		for _, occ := range occurrenceMap[monKey] {
			if occ.Task.Task.Title == "Hourly Active Task" {
				monCount++
			}
		}
		for _, occ := range occurrenceMap[thuKey] {
			if occ.Task.Task.Title == "Hourly Active Task" {
				thuCount++
			}
		}
		for _, occ := range occurrenceMap[satKey] {
			if occ.Task.Task.Title == "Hourly Active Task" {
				satCount++
			}
		}
	}

	// All days should have 24 entries (one per hour)
	if sunCount != 24 {
		t.Errorf("Sun: expected 24 hourly entries, got %d (should show full week with LastRun set)", sunCount)
	}
	if monCount != 24 {
		t.Errorf("Mon: expected 24 hourly entries, got %d", monCount)
	}
	if thuCount != 24 {
		t.Errorf("Thu: expected 24 hourly entries, got %d", thuCount)
	}
	if satCount != 24 {
		t.Errorf("Sat: expected 24 hourly entries, got %d", satCount)
	}
}

// TestBuildTaskOccurrenceMap_SubDailyWithoutLastRun verifies that a NEW sub-daily schedule
// (no LastRun) shows entries starting from RunAt, not from the beginning of the week.
func TestBuildTaskOccurrenceMap_SubDailyWithoutLastRun(t *testing.T) {
	// Week of Feb 22-28, 2026
	startOfWeek := time.Date(2026, 2, 22, 0, 0, 0, 0, time.Local)

	// New hourly schedule created on Thu — no LastRun yet
	runAt := time.Date(2026, 2, 26, 21, 5, 0, 0, time.Local)   // Thu 9:05 PM local
	runAtUTC := runAt.UTC()
	nextRun := runAtUTC

	task := repository.TaskWithSchedule{
		Task: models.Task{
			ID:        "hourly2",
			ProjectID: "project1",
			Title:     "New Hourly Task",
		},
		Schedule: &models.Schedule{
			ID:             "s2",
			TaskID:         "hourly2",
			RunAt:          runAtUTC,
			NextRun:        &nextRun,
			LastRun:        nil, // Never ran yet
			RepeatType:     models.RepeatHours,
			RepeatInterval: 1,
			Enabled:        true,
		},
	}

	occurrenceMap := buildTaskOccurrenceMap([]repository.TaskWithSchedule{task}, startOfWeek)

	// Without LastRun, entries should start from RunAt (Thu 9 PM), not from start of week
	sunCount := 0
	wedCount := 0
	thuCount := 0
	friCount := 0

	for hour := 0; hour < 24; hour++ {
		sunKey := fmt.Sprintf("2026-02-22-%02d", hour)
		wedKey := fmt.Sprintf("2026-02-25-%02d", hour)
		thuKey := fmt.Sprintf("2026-02-26-%02d", hour)
		friKey := fmt.Sprintf("2026-02-27-%02d", hour)

		for _, occ := range occurrenceMap[sunKey] {
			if occ.Task.Task.Title == "New Hourly Task" {
				sunCount++
			}
		}
		for _, occ := range occurrenceMap[wedKey] {
			if occ.Task.Task.Title == "New Hourly Task" {
				wedCount++
			}
		}
		for _, occ := range occurrenceMap[thuKey] {
			if occ.Task.Task.Title == "New Hourly Task" {
				thuCount++
			}
		}
		for _, occ := range occurrenceMap[friKey] {
			if occ.Task.Task.Title == "New Hourly Task" {
				friCount++
			}
		}
	}

	// Sun-Wed should have NO entries (before RunAt)
	if sunCount != 0 {
		t.Errorf("Sun: expected 0 entries before RunAt, got %d", sunCount)
	}
	if wedCount != 0 {
		t.Errorf("Wed: expected 0 entries before RunAt, got %d", wedCount)
	}

	// Thu should have entries only from 9 PM onwards (hours 21, 22, 23 = 3 entries)
	if thuCount != 3 {
		t.Errorf("Thu: expected 3 entries (9-11 PM), got %d", thuCount)
	}

	// Fri should have all 24 hours
	if friCount != 24 {
		t.Errorf("Fri: expected 24 entries, got %d", friCount)
	}
}

// TestBuildTaskOccurrenceMap_SubDailyEveryTwoHours verifies that a task scheduled
// every 2 hours only shows cards at the correct 2-hour intervals, not every hour.
// This was a bug where "every 2 hours" tasks appeared in every hour slot.
func TestBuildTaskOccurrenceMap_SubDailyEveryTwoHours(t *testing.T) {
	// Week of Feb 22-28, 2026
	startOfWeek := time.Date(2026, 2, 22, 0, 0, 0, 0, time.Local)

	// Task runs every 2 hours, starting at 9 AM local. Schedule has been active.
	runAt := time.Date(2026, 2, 23, 9, 0, 0, 0, time.Local)
	runAtUTC := runAt.UTC()
	nextRun := time.Date(2026, 2, 26, 11, 0, 0, 0, time.Local)
	nextRunUTC := nextRun.UTC()
	lastRun := time.Date(2026, 2, 26, 9, 0, 0, 0, time.Local)
	lastRunUTC := lastRun.UTC()

	task := repository.TaskWithSchedule{
		Task: models.Task{
			ID:        "every2h",
			ProjectID: "project1",
			Title:     "Every 2 Hours Task",
		},
		Schedule: &models.Schedule{
			ID:             "s1",
			TaskID:         "every2h",
			RunAt:          runAtUTC,
			NextRun:        &nextRunUTC,
			LastRun:        &lastRunUTC,
			RepeatType:     models.RepeatHours,
			RepeatInterval: 2,
			Enabled:        true,
		},
	}

	occurrenceMap := buildTaskOccurrenceMap([]repository.TaskWithSchedule{task}, startOfWeek)

	// With anchor hour 9 and interval 2, valid hours are: 1, 3, 5, 7, 9, 11, 13, 15, 17, 19, 21, 23
	// (hours where (hour - 9 + 24) % 2 == 0)
	// That's 12 entries per day, NOT 24.
	validHours := map[int]bool{}
	for h := 0; h < 24; h++ {
		diff := h - 9
		if diff < 0 {
			diff += 24
		}
		if diff%2 == 0 {
			validHours[h] = true
		}
	}

	// Check a full day (Sunday Feb 22 — should have entries since LastRun is set)
	sunCount := 0
	for hour := 0; hour < 24; hour++ {
		sunKey := fmt.Sprintf("2026-02-22-%02d", hour)
		for _, occ := range occurrenceMap[sunKey] {
			if occ.Task.Task.Title == "Every 2 Hours Task" {
				sunCount++
				if !validHours[hour] {
					t.Errorf("Sun hour %d: should NOT have entry (not aligned with 2-hour interval from hour 9)", hour)
				}
			}
		}
	}

	if sunCount != 12 {
		t.Errorf("Sun: expected 12 entries (every 2 hours), got %d", sunCount)
	}

	// Also check another day
	wedCount := 0
	for hour := 0; hour < 24; hour++ {
		wedKey := fmt.Sprintf("2026-02-25-%02d", hour)
		for _, occ := range occurrenceMap[wedKey] {
			if occ.Task.Task.Title == "Every 2 Hours Task" {
				wedCount++
			}
		}
	}

	if wedCount != 12 {
		t.Errorf("Wed: expected 12 entries (every 2 hours), got %d", wedCount)
	}

	// Verify total across the week: 12 per day * 7 days = 84
	totalCount := 0
	for _, occs := range occurrenceMap {
		for _, occ := range occs {
			if occ.Task.Task.Title == "Every 2 Hours Task" {
				totalCount++
			}
		}
	}
	if totalCount != 84 {
		t.Errorf("Total: expected 84 entries (12/day * 7 days), got %d", totalCount)
	}
}

// TestBuildTaskOccurrenceMap_SubDailyEveryThreeHoursNewSchedule verifies that a NEW
// every-3-hours task (no LastRun) shows correctly from RunAt with proper interval spacing.
func TestBuildTaskOccurrenceMap_SubDailyEveryThreeHoursNewSchedule(t *testing.T) {
	startOfWeek := time.Date(2026, 2, 22, 0, 0, 0, 0, time.Local)

	// New schedule: every 3 hours starting at 6 PM local on Thursday
	runAt := time.Date(2026, 2, 26, 18, 0, 0, 0, time.Local)
	runAtUTC := runAt.UTC()
	nextRun := runAtUTC

	task := repository.TaskWithSchedule{
		Task: models.Task{
			ID:        "every3h",
			ProjectID: "project1",
			Title:     "Every 3 Hours Task",
		},
		Schedule: &models.Schedule{
			ID:             "s1",
			TaskID:         "every3h",
			RunAt:          runAtUTC,
			NextRun:        &nextRun,
			LastRun:        nil, // New schedule
			RepeatType:     models.RepeatHours,
			RepeatInterval: 3,
			Enabled:        true,
		},
	}

	occurrenceMap := buildTaskOccurrenceMap([]repository.TaskWithSchedule{task}, startOfWeek)

	// Valid hours with anchor 18 and interval 3: 0, 3, 6, 9, 12, 15, 18, 21
	// Thu (Feb 26): from 18 onwards → 18, 21 = 2 entries
	thuCount := 0
	for hour := 0; hour < 24; hour++ {
		thuKey := fmt.Sprintf("2026-02-26-%02d", hour)
		for _, occ := range occurrenceMap[thuKey] {
			if occ.Task.Task.Title == "Every 3 Hours Task" {
				thuCount++
			}
		}
	}
	if thuCount != 2 {
		t.Errorf("Thu: expected 2 entries (18, 21), got %d", thuCount)
	}

	// Fri (Feb 27): full day → 0, 3, 6, 9, 12, 15, 18, 21 = 8 entries
	friCount := 0
	for hour := 0; hour < 24; hour++ {
		friKey := fmt.Sprintf("2026-02-27-%02d", hour)
		for _, occ := range occurrenceMap[friKey] {
			if occ.Task.Task.Title == "Every 3 Hours Task" {
				friCount++
			}
		}
	}
	if friCount != 8 {
		t.Errorf("Fri: expected 8 entries (every 3 hours), got %d", friCount)
	}

	// Days before Thu should have 0 entries
	wedCount := 0
	for hour := 0; hour < 24; hour++ {
		wedKey := fmt.Sprintf("2026-02-25-%02d", hour)
		for _, occ := range occurrenceMap[wedKey] {
			if occ.Task.Task.Title == "Every 3 Hours Task" {
				wedCount++
			}
		}
	}
	if wedCount != 0 {
		t.Errorf("Wed: expected 0 entries (before RunAt), got %d", wedCount)
	}
}

// TestBuildTaskOccurrenceMap_ExcludesTasksWithoutSchedule verifies that tasks returned
// by ListWithSchedulesByProject with category='scheduled' but no schedule entry (Schedule == nil)
// are excluded from the occurrence map. This is critical for drag-and-drop: only tasks
// with schedule configuration should appear as draggable cards.
func TestBuildTaskOccurrenceMap_ExcludesTasksWithoutSchedule(t *testing.T) {
	startOfWeek := time.Date(2026, 2, 22, 0, 0, 0, 0, time.Local)

	// Task with schedule (should appear)
	runAt := time.Date(2026, 2, 25, 14, 0, 0, 0, time.Local)
	runAtUTC := runAt.UTC()

	// Task WITHOUT schedule (category='scheduled' but no schedule entry — should be excluded)
	tasks := []repository.TaskWithSchedule{
		{
			Task: models.Task{
				ID:        "task-with-schedule",
				ProjectID: "project1",
				Title:     "Scheduled Task",
				Category:  models.CategoryScheduled,
			},
			Schedule: &models.Schedule{
				ID:         "schedule1",
				TaskID:     "task-with-schedule",
				RunAt:      runAtUTC,
				NextRun:    &runAtUTC,
				RepeatType: models.RepeatOnce,
				Enabled:    true,
			},
		},
		{
			Task: models.Task{
				ID:        "task-no-schedule",
				ProjectID: "project1",
				Title:     "No Schedule Task",
				Category:  models.CategoryScheduled,
			},
			Schedule: nil, // No schedule entry
		},
	}

	occurrenceMap := buildTaskOccurrenceMap(tasks, startOfWeek)

	// Only the scheduled task should appear
	totalOccurrences := 0
	for _, occs := range occurrenceMap {
		for _, occ := range occs {
			totalOccurrences++
			if occ.Task.Task.ID == "task-no-schedule" {
				t.Errorf("Task without schedule should not appear in occurrence map")
			}
		}
	}

	if totalOccurrences != 1 {
		t.Errorf("Expected exactly 1 occurrence (scheduled task only), got %d", totalOccurrences)
	}
}

// TestGetScheduleID_EdgeCases verifies the getScheduleID helper returns correct values.
func TestGetScheduleID_EdgeCases(t *testing.T) {
	// Task with schedule
	withSchedule := repository.TaskWithSchedule{
		Task: models.Task{ID: "t1"},
		Schedule: &models.Schedule{
			ID:     "sched-123",
			TaskID: "t1",
		},
	}
	if got := getScheduleID(withSchedule); got != "sched-123" {
		t.Errorf("getScheduleID with schedule: expected 'sched-123', got '%s'", got)
	}

	// Task without schedule
	withoutSchedule := repository.TaskWithSchedule{
		Task:     models.Task{ID: "t2"},
		Schedule: nil,
	}
	if got := getScheduleID(withoutSchedule); got != "" {
		t.Errorf("getScheduleID without schedule: expected '', got '%s'", got)
	}
}

// TestBuildTaskOccurrenceMap_RepeatOnceExecuted verifies that a RepeatOnce task
// that has already executed (NextRun = nil) still appears on the calendar at its
// RunAt time and has valid schedule data for drag-and-drop.
func TestBuildTaskOccurrenceMap_RepeatOnceExecuted(t *testing.T) {
	startOfWeek := time.Date(2026, 2, 22, 0, 0, 0, 0, time.Local)

	// RepeatOnce task that has already executed — NextRun is nil
	runAt := time.Date(2026, 2, 24, 10, 0, 0, 0, time.Local)
	runAtUTC := runAt.UTC()
	lastRun := runAtUTC
	scheduleID := "sched-once-executed"

	task := repository.TaskWithSchedule{
		Task: models.Task{
			ID:        "once-executed",
			ProjectID: "project1",
			Title:     "Executed Once Task",
		},
		Schedule: &models.Schedule{
			ID:         scheduleID,
			TaskID:     "once-executed",
			RunAt:      runAtUTC,
			NextRun:    nil, // Already executed
			LastRun:    &lastRun,
			RepeatType: models.RepeatOnce,
			Enabled:    true,
		},
	}

	occurrenceMap := buildTaskOccurrenceMap([]repository.TaskWithSchedule{task}, startOfWeek)

	// Should appear at RunAt time (falls back to RunAt when NextRun is nil)
	key := localKey(runAtUTC)
	occurrences := occurrenceMap[key]

	if len(occurrences) != 1 {
		t.Errorf("Expected 1 occurrence for executed RepeatOnce task at %s, got %d", key, len(occurrences))
		for k, v := range occurrenceMap {
			t.Logf("Found: %s (%d items)", k, len(v))
		}
		return
	}

	// Verify the occurrence has a valid schedule ID for drag-and-drop
	occ := occurrences[0]
	if getScheduleID(occ.Task) != scheduleID {
		t.Errorf("Expected schedule ID '%s', got '%s'", scheduleID, getScheduleID(occ.Task))
	}
	if occ.Task.Schedule == nil {
		t.Error("Expected non-nil schedule for drag-and-drop data attributes")
	}
}

// TestBuildTaskOccurrenceMap_MixedScheduleTypes verifies that a mix of RepeatOnce,
// recurring, and sub-daily tasks all have valid schedule data for drag-and-drop,
// and that tasks without schedules are excluded.
func TestBuildTaskOccurrenceMap_MixedScheduleTypes(t *testing.T) {
	startOfWeek := time.Date(2026, 2, 22, 0, 0, 0, 0, time.Local)

	onceRunAt := time.Date(2026, 2, 23, 9, 0, 0, 0, time.Local).UTC()
	dailyRunAt := time.Date(2026, 2, 22, 14, 0, 0, 0, time.Local).UTC()
	hourlyRunAt := time.Date(2026, 2, 24, 10, 0, 0, 0, time.Local).UTC()
	lastRun := hourlyRunAt

	tasks := []repository.TaskWithSchedule{
		{
			Task:     models.Task{ID: "t-once", ProjectID: "p1", Title: "Once"},
			Schedule: &models.Schedule{ID: "s1", TaskID: "t-once", RunAt: onceRunAt, NextRun: &onceRunAt, RepeatType: models.RepeatOnce, Enabled: true},
		},
		{
			Task:     models.Task{ID: "t-daily", ProjectID: "p1", Title: "Daily"},
			Schedule: &models.Schedule{ID: "s2", TaskID: "t-daily", RunAt: dailyRunAt, NextRun: &dailyRunAt, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true},
		},
		{
			Task:     models.Task{ID: "t-hourly", ProjectID: "p1", Title: "Hourly"},
			Schedule: &models.Schedule{ID: "s3", TaskID: "t-hourly", RunAt: hourlyRunAt, NextRun: &hourlyRunAt, LastRun: &lastRun, RepeatType: models.RepeatHours, RepeatInterval: 1, Enabled: true},
		},
		{
			Task:     models.Task{ID: "t-none", ProjectID: "p1", Title: "No Schedule", Category: models.CategoryScheduled},
			Schedule: nil, // No schedule - should be excluded
		},
	}

	occurrenceMap := buildTaskOccurrenceMap(tasks, startOfWeek)

	// Verify all occurrences have valid schedule data
	scheduledTasks := map[string]bool{"t-once": false, "t-daily": false, "t-hourly": false}
	for _, occs := range occurrenceMap {
		for _, occ := range occs {
			taskID := occ.Task.Task.ID
			if taskID == "t-none" {
				t.Error("Task without schedule should not appear in occurrence map")
			}
			if occ.Task.Schedule == nil {
				t.Errorf("Task %s has nil schedule in occurrence map", taskID)
			}
			if getScheduleID(occ.Task) == "" {
				t.Errorf("Task %s has empty schedule ID in occurrence map", taskID)
			}
			scheduledTasks[taskID] = true
		}
	}

	// Verify all scheduled tasks have at least one occurrence
	for taskID, found := range scheduledTasks {
		if !found {
			t.Errorf("Scheduled task %s not found in occurrence map", taskID)
		}
	}
}

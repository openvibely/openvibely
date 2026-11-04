package sqlite

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSlackScheduleProvenanceSurvivesTaskRebind(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
	admitted := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{TeamID: "T123", ExternalUserID: "U123", ProjectID: "default", UserID: models.BootstrapUserID})
	task := models.Task{ProjectID: "default", Title: "Scheduled Slack work", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, NewTaskRepo(db, nil).Create(admitted, &task))
	schedules := NewScheduleRepo(db)
	schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().Add(-time.Minute), RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
	require.NoError(t, schedules.Create(admitted, &schedule))
	identities := NewRunIdentityRepo(db)
	require.NoError(t, identities.BindTask(owner, task.ID))
	user, err := identities.ScheduleUser(ctx, schedule.ID)
	require.NoError(t, err)
	require.Equal(t, models.BootstrapUserID, user)
	claimed, err := schedules.ClaimCalendarOccurrence(ctx, schedule, time.Now(), nil)
	require.Error(t, err)
	require.False(t, claimed, "missing principal cannot bypass provenance")
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	_, err = identities.ScheduleUser(ctx, schedule.ID)
	require.Error(t, err)
	claimed, err = schedules.ClaimCalendarOccurrence(owner, schedule, time.Now(), nil)
	require.Error(t, err)
	require.False(t, claimed)
	saved, err := schedules.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.Nil(t, saved.LastRun)
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
	_, err = db.Exec(`CREATE TRIGGER fail_schedule_dispatch BEFORE INSERT ON project_audit_events WHEN NEW.action='schedule.dispatch' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	claimed, err = schedules.ClaimCalendarOccurrence(owner, schedule, time.Now(), nil)
	require.Error(t, err)
	require.False(t, claimed)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM slack_execution_provenance`).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`DROP TRIGGER fail_schedule_dispatch`)
	require.NoError(t, err)
	claimed, err = schedules.ClaimCalendarOccurrence(owner, schedule, time.Now(), nil)
	require.NoError(t, err)
	require.True(t, claimed)
	var executionID string
	require.NoError(t, db.QueryRow(`SELECT execution_id FROM schedule_dispatch_admissions WHERE schedule_id=?`, schedule.ID).Scan(&executionID))
	require.NoError(t, identities.CheckExecutionGoal(ctx, executionID, task.ID))
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	require.Error(t, identities.CheckExecutionGoal(ctx, executionID, task.ID))
	require.Error(t, NewExecutionRepo(db).MarkRunning(ctx, executionID))
}

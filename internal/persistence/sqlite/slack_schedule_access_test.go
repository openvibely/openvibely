package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSlackScheduleActionsRecheckBinding(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
	admitted := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{TeamID: "T123", ExternalUserID: "U123", ProjectID: "default", UserID: models.BootstrapUserID})
	task := &models.Task{ProjectID: "default", Title: "Scheduled Slack action", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, NewTaskRepo(db, nil).Create(ctx, task))
	repo := NewScheduleRepo(db)
	schedule := &models.Schedule{TaskID: task.ID, RunAt: time.Now().UTC(), RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
	require.NoError(t, repo.Create(admitted, schedule))
	original, err := repo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	update := *original
	update.RunAt = update.RunAt.Add(time.Hour)
	actions := map[string]func() error{
		"create":  func() error { copy := *original; return repo.Create(admitted, &copy) },
		"update":  func() error { return repo.UpdateForTask(admitted, &update, task.ID) },
		"context": func() error { return repo.UpdateClearContextOnStart(admitted, schedule.ID, task.ID, true) },
		"disable": func() error { _, err := repo.SetEnabledForTask(admitted, schedule.ID, task.ID, false); return err },
		"delete":  func() error { return repo.Delete(admitted, schedule.ID) },
	}
	unchanged := func() {
		t.Helper()
		saved, err := repo.GetByID(ctx, schedule.ID)
		require.NoError(t, err)
		require.Equal(t, original, saved)
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM schedules`).Scan(&count))
		require.Equal(t, 1, count)
	}
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	for name, action := range actions {
		require.Error(t, action(), name)
		unchanged()
	}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
	require.Error(t, repo.UpdateForTask(admitted, &update, "foreign-task"))
	unchanged()
	_, err = db.Exec(`CREATE TRIGGER fail_schedule_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	for name, action := range actions {
		require.Error(t, action(), name)
		unchanged()
	}
	_, err = db.Exec(`DROP TRIGGER fail_schedule_audit`)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateForTask(admitted, &update, task.ID))
	require.NoError(t, repo.UpdateClearContextOnStart(admitted, schedule.ID, task.ID, true))
	enabled, err := repo.SetEnabledForTask(admitted, schedule.ID, task.ID, false)
	require.NoError(t, err)
	require.False(t, enabled.Enabled)
	require.NoError(t, repo.Delete(admitted, schedule.ID))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events WHERE action LIKE 'slack.schedule_%'`).Scan(&count))
	require.Equal(t, 4, count)
}

package sqlite

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestScheduleRunIdentityAuditRollbackAndViewerDenial(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	tasks := NewTaskRepo(db, nil)
	task := models.Task{ProjectID: "default", Title: "Atomic schedule", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, tasks.Create(owner, &task))
	schedules := NewScheduleRepo(db)
	schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().Add(-time.Minute), RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
	_, err := db.Exec(`CREATE TRIGGER reject_schedule_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='schedule.run_as' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	require.Error(t, schedules.Create(owner, &schedule))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM schedules WHERE task_id=?`, task.ID).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM schedule_run_identities`).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`DROP TRIGGER reject_schedule_audit; INSERT INTO users(id,username) VALUES('viewer','viewer'); INSERT INTO org_memberships VALUES('default','viewer','member'); INSERT INTO project_members VALUES('default','viewer','viewer')`)
	require.NoError(t, err)
	viewer := models.WithPrincipal(ctx, models.Principal{UserID: "viewer"})
	require.ErrorIs(t, schedules.Create(viewer, &schedule), persistence.ErrProjectDenied)
	require.NoError(t, schedules.Create(owner, &schedule))
	_, err = db.Exec(`CREATE TRIGGER reject_dispatch_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='schedule.dispatch' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	claimed, err := schedules.ClaimCalendarOccurrence(owner, schedule, time.Now(), nil)
	require.Error(t, err)
	require.False(t, claimed)
	stored, err := schedules.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.Nil(t, stored.LastRun)
	for _, table := range []string{"executions", "execution_run_identities", "schedule_dispatch_admissions"} {
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
		require.Zero(t, count, "admission must roll back with its audit event")
	}
}

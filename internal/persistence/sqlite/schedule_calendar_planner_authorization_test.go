package sqlite

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestCalendarPlannerRetainsDiscordScheduleIdentity(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	auth := NewDiscordAuthRepo(db)
	entry := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: "123456789"}
	bind := func() {
		require.NoError(t, auth.Create(owner, entry))
		require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
	}
	bind()
	admitted := models.WithDiscordIngressIdentity(owner, models.DiscordIngressIdentity{ProjectID: "default", ExternalUserID: entry.DiscordUserID, UserID: models.BootstrapUserID})
	tasks := NewTaskRepo(db, nil)
	parent := models.Task{ProjectID: "default", Title: "Scheduled swarm", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2, SwarmRole: models.SwarmRoleParent}
	require.NoError(t, tasks.Create(owner, &parent))
	schedules := NewScheduleRepo(db)
	schedule := models.Schedule{TaskID: parent.ID, RunAt: time.Now().Add(-time.Minute), RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
	require.NoError(t, schedules.Create(admitted, &schedule))
	claimed, err := schedules.ClaimCalendarOccurrence(owner, schedule, time.Now(), nil)
	require.NoError(t, err)
	require.True(t, claimed)
	calls := 0
	makePlanner := func(p *models.Task) (*models.Task, error) {
		calls++
		return &models.Task{ProjectID: p.ProjectID, ParentTaskID: &p.ID, Title: "Planner", Category: models.CategoryActive, Status: models.StatusPending, Priority: 2, SwarmRole: models.SwarmRolePlanner}, nil
	}
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	_, err = tasks.ReserveCalendarPlanner(ctx, parent.ID, makePlanner)
	require.Error(t, err)
	require.Zero(t, calls, "revocation rejected before planner preparation")
	bind()
	// A foreign principal cannot replace the durable admitted user.
	_, err = tasks.ReserveCalendarPlanner(models.WithPrincipal(ctx, models.Principal{UserID: "other", Role: "owner"}), parent.ID, makePlanner)
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	require.Zero(t, calls)
	_, err = tasks.ReserveCalendarPlanner(ctx, parent.ID, func(p *models.Task) (*models.Task, error) {
		planner, err := makePlanner(p)
		planner.ProjectID = "foreign-project"
		return planner, err
	})
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
	_, err = db.Exec(`CREATE TRIGGER fail_planner_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='schedule.planner_admitted' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	_, err = tasks.ReserveCalendarPlanner(ctx, parent.ID, makePlanner)
	require.ErrorContains(t, err, "audit unavailable")
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM executions`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM tasks WHERE parent_task_id=?`, parent.ID).Scan(&count))
	require.Zero(t, count)
	saved, err := tasks.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusPending, saved.Status)
	_, err = db.Exec(`DROP TRIGGER fail_planner_audit`)
	require.NoError(t, err)
	result, err := tasks.ReserveCalendarPlanner(ctx, parent.ID, makePlanner)
	require.NoError(t, err)
	require.NotNil(t, result)
	identities := NewRunIdentityRepo(db)
	user, found, err := identities.ExecutionUser(ctx, result.ExecutionID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, models.BootstrapUserID, user)
	require.NoError(t, identities.CheckExecutionGoal(ctx, result.ExecutionID, result.Task.ID))
	_, err = db.Exec(`DELETE FROM discord_schedule_provenance WHERE schedule_id=?`, schedule.ID)
	require.NoError(t, err)
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	require.Error(t, identities.CheckExecutionGoal(owner, result.ExecutionID, result.Task.ID))
	require.Error(t, NewExecutionRepo(db).MarkRunning(owner, result.ExecutionID))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE action='schedule.planner_admitted'`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestCalendarPlannerLegacyAnonymousCompatibility(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	tasks := NewTaskRepo(db, nil)
	parent := models.Task{ProjectID: "default", Title: "Legacy swarm", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2, SwarmRole: models.SwarmRoleParent}
	require.NoError(t, tasks.Create(ctx, &parent))
	schedules := NewScheduleRepo(db)
	schedule := models.Schedule{TaskID: parent.ID, RunAt: time.Now().Add(-time.Minute), RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
	require.NoError(t, schedules.Create(ctx, &schedule))
	claimed, err := schedules.ClaimCalendarOccurrence(ctx, schedule, time.Now(), nil)
	require.NoError(t, err)
	require.True(t, claimed)
	result, err := tasks.ReserveCalendarPlanner(ctx, parent.ID, func(p *models.Task) (*models.Task, error) {
		return &models.Task{ProjectID: p.ProjectID, ParentTaskID: &p.ID, Title: "Legacy planner", Category: models.CategoryActive, Status: models.StatusPending, Priority: 2, SwarmRole: models.SwarmRolePlanner}, nil
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	_, found, err := NewRunIdentityRepo(db).ExecutionUser(ctx, result.ExecutionID)
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, NewExecutionRepo(db).MarkRunning(ctx, result.ExecutionID))
}

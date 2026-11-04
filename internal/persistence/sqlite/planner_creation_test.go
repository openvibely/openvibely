package sqlite

import (
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestFirstPlannerCreationAtomicIdentityAndParent(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	tasks := NewTaskRepo(db, nil)
	auth := NewDiscordAuthRepo(db)
	entry := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: "123456789"}
	bind := func() {
		require.NoError(t, auth.Create(owner, entry))
		require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
	}
	bind()
	admitted := models.WithDiscordIngressIdentity(owner, models.DiscordIngressIdentity{ProjectID: "default", ExternalUserID: entry.DiscordUserID, UserID: models.BootstrapUserID})
	parent := models.Task{ProjectID: "default", Title: "Parent", Priority: 2, Category: models.CategoryBacklog, Status: models.StatusPending, SwarmRole: models.SwarmRoleParent}
	require.NoError(t, tasks.Create(admitted, &parent))
	before, err := tasks.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	guarded := persistence.WithPlannerCreationParent(persistence.WithSwarmWriteIdentities(owner, []persistence.SwarmWriteIdentity{{TaskID: parent.ID, ProjectID: "default", UserID: models.BootstrapUserID}}), *before)
	child := func() *models.Task {
		return &models.Task{ProjectID: "default", Title: "Planner", Priority: 2, Category: models.CategoryActive, Status: models.StatusPending, SwarmRole: models.SwarmRolePlanner, ParentTaskID: &parent.ID}
	}
	unchanged := func() {
		saved, err := tasks.GetByID(ctx, parent.ID)
		require.NoError(t, err)
		require.Equal(t, before, saved)
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM tasks WHERE parent_task_id=?`, parent.ID).Scan(&n))
		require.Zero(t, n)
	}
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	require.Error(t, tasks.Create(guarded, child()))
	unchanged()
	bind()
	_, err = db.Exec(`CREATE TRIGGER fail_planner_insert BEFORE INSERT ON tasks WHEN NEW.swarm_role='planner' BEGIN SELECT RAISE(ABORT,'child unavailable'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, tasks.Create(guarded, child()), "child unavailable")
	unchanged()
	_, err = db.Exec(`DROP TRIGGER fail_planner_insert`)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TRIGGER fail_planner_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='swarm.first_planner_prepared' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, tasks.Create(guarded, child()), "audit unavailable")
	unchanged()
	_, err = db.Exec(`DROP TRIGGER fail_planner_audit`)
	require.NoError(t, err)
	require.ErrorContains(t, tasks.CreateWithGoalAndCallback(guarded, child(), &models.TaskGoal{Objective: "Plan"}, func(persistence.Tx) error { return errors.New("callback failure") }), "callback failure")
	unchanged()
	planner := child()
	goal := &models.TaskGoal{Objective: "Plan"}
	require.NoError(t, tasks.CreateWithGoal(guarded, planner, goal))
	saved, err := tasks.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryActive, saved.Category)
	require.Equal(t, models.StatusBlocked, saved.Status)
	require.Equal(t, models.BootstrapUserID, goal.RunAsUserID)
	user, err := NewRunIdentityRepo(db).TaskUser(ctx, planner.ID)
	require.NoError(t, err)
	require.Equal(t, models.BootstrapUserID, user)
	require.Error(t, tasks.Create(guarded, child()), "stale retry cannot create another planner")
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	_, err = NewRunIdentityRepo(db).TaskUser(ctx, planner.ID)
	require.Error(t, err)
}

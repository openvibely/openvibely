package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSlackTaskProvenanceSurvivesWorkerRecovery(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
	admitted := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{TeamID: "T123", ExternalUserID: "U123", ProjectID: "default", UserID: models.BootstrapUserID})
	task := &models.Task{ProjectID: "default", Title: "Deferred Slack work", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, NewTaskRepo(db, nil).Create(admitted, task))
	identities := NewRunIdentityRepo(db)
	user, err := identities.TaskUser(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.BootstrapUserID, user)
	executions := NewExecutionRepo(db)
	require.Error(t, executions.Create(ctx, &models.Execution{TaskID: task.ID, Status: models.ExecQueued}), "channel-bound work requires a saved principal")
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecQueued}
	require.NoError(t, executions.Create(owner, execution))
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	_, err = identities.TaskUser(ctx, task.ID)
	require.Error(t, err)
	require.Error(t, executions.Create(owner, &models.Execution{TaskID: task.ID, Status: models.ExecQueued}))
	require.Error(t, identities.CheckExecutionGoal(ctx, execution.ID, task.ID))
	_, err = db.Exec(`CREATE TRIGGER fail_rebind_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='task.run_as' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	require.Error(t, identities.BindTask(owner, task.ID))
	_, err = identities.TaskUser(ctx, task.ID)
	require.Error(t, err, "failed explicit rebind must retain channel provenance")
	_, err = db.Exec(`DROP TRIGGER fail_rebind_audit`)
	require.NoError(t, err)
	require.NoError(t, identities.BindTask(owner, task.ID))
	_, err = identities.TaskUser(ctx, task.ID)
	require.NoError(t, err, "fresh authorized web submission replaces task provenance")
	require.NoError(t, executions.Create(owner, &models.Execution{TaskID: task.ID, Status: models.ExecQueued}))
	require.Error(t, identities.CheckExecutionGoal(ctx, execution.ID, task.ID), "new task submission cannot erase an old execution's provenance")
}

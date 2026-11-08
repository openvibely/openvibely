package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestDiscordExecutionProvenanceRecovery(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	auth := NewDiscordAuthRepo(db)
	entry := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: "123456789"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
	identity := models.DiscordIngressIdentity{ProjectID: "default", ExternalUserID: entry.DiscordUserID, UserID: models.BootstrapUserID}
	admitted := models.WithDiscordIngressIdentity(owner, identity)
	task := &models.Task{ProjectID: "default", Title: "Discord execution", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2, CreatedVia: models.TaskOriginDiscord}
	require.NoError(t, NewTaskRepo(db, nil).Create(admitted, task))
	executions := NewExecutionRepo(db)
	fresh := func() *models.Execution {
		return &models.Execution{TaskID: task.ID, Status: models.ExecQueued, PromptSent: "run"}
	}
	require.Error(t, executions.Create(ctx, fresh()), "anonymous recovery cannot lose task provenance")
	require.Error(t, executions.Create(models.WithDiscordIngressIdentity(ctx, identity), fresh()))
	_, err := db.Exec(`CREATE TRIGGER fail_discord_snapshot BEFORE INSERT ON discord_execution_provenance BEGIN SELECT RAISE(ABORT,'snapshot failed'); END`)
	require.NoError(t, err)
	require.Error(t, executions.Create(owner, fresh()))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM executions WHERE task_id=?`, task.ID).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`DROP TRIGGER fail_discord_snapshot`)
	require.NoError(t, err)
	run := fresh()
	require.NoError(t, executions.Create(owner, run), "recovery inherits task evidence without request metadata")
	require.NoError(t, NewRunIdentityRepo(db).CheckExecutionGoal(ctx, run.ID, task.ID))
	// Removing task provenance cannot erase an independently admitted execution.
	_, err = db.Exec(`DELETE FROM discord_task_provenance WHERE task_id=?`, task.ID)
	require.NoError(t, err)
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	require.Error(t, NewRunIdentityRepo(db).CheckExecutionGoal(owner, run.ID, task.ID))
	require.Error(t, executions.MarkRunning(owner, run.ID))
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM executions WHERE id=?`, run.ID).Scan(&status))
	require.Equal(t, string(models.ExecQueued), status)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_execution_provenance WHERE execution_id=?`, run.ID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestDiscordExecutionLegacyWithoutProvenance(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	task := &models.Task{ProjectID: "default", Title: "Legacy auth-off Discord", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2, CreatedVia: models.TaskOriginDiscord}
	require.NoError(t, NewTaskRepo(db, nil).Create(ctx, task))
	run := &models.Execution{TaskID: task.ID, Status: models.ExecQueued, PromptSent: "legacy"}
	executions := NewExecutionRepo(db)
	require.NoError(t, executions.Create(ctx, run))
	require.NoError(t, executions.MarkRunning(ctx, run.ID))
	require.NoError(t, NewRunIdentityRepo(db).CheckExecutionGoal(ctx, run.ID, task.ID))
}

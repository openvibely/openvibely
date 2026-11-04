package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSlackRunAdmissionRejectsRemovedBinding(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('sender','sender'); INSERT INTO org_memberships VALUES('default','sender','member')`)
	require.NoError(t, err)
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", "sender"))
	admitted := models.WithSlackIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: "sender"}), models.SlackIngressIdentity{TeamID: "T123", ExternalUserID: "U123", ProjectID: "default", UserID: "sender"})
	tasks := NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: "default", Title: "Run admission", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, tasks.Create(owner, task))
	identities := NewRunIdentityRepo(db)
	executions := NewExecutionRepo(db)
	newExecution := func() *models.Execution {
		return &models.Execution{TaskID: task.ID, Status: models.ExecQueued, PromptSent: "Run"}
	}
	denied := func() {
		t.Helper()
		require.Error(t, identities.BindTask(admitted, task.ID))
		require.Error(t, executions.Create(admitted, newExecution()))
		user, err := identities.TaskUser(ctx, task.ID)
		require.NoError(t, err)
		require.Equal(t, models.BootstrapUserID, user)
		var count int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM executions WHERE task_id=?`, task.ID).Scan(&count))
		require.Zero(t, count)
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM project_audit_events WHERE actor_user_id='sender' AND action IN ('task.run_as','execution.run_as')`).Scan(&count))
		require.Zero(t, count)
	}
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
	denied()
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", "sender"))
	original := admitted
	admitted = models.WithSlackIngressIdentity(admitted, models.SlackIngressIdentity{TeamID: "T123", ExternalUserID: "U123", ProjectID: "foreign", UserID: "sender"})
	denied()
	admitted = original
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	denied()
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", "sender"))
	require.NoError(t, identities.BindTask(admitted, task.ID))
	metadata, _ := models.SlackIngressIdentityFromContext(admitted)
	require.Error(t, executions.Create(models.WithSlackIngressIdentity(ctx, metadata), newExecution()), "channel metadata cannot bypass identity admission")
	_, err = db.Exec(`CREATE TRIGGER fail_execution_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='execution.run_as' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	require.Error(t, executions.Create(admitted, newExecution()))
	var provenanceCount int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM slack_execution_provenance`).Scan(&provenanceCount))
	require.Zero(t, provenanceCount)
	_, err = db.Exec(`DROP TRIGGER fail_execution_audit`)
	require.NoError(t, err)
	execution := newExecution()
	require.NoError(t, executions.Create(admitted, execution))
	user, found, err := identities.ExecutionUser(ctx, execution.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "sender", user)
	require.NoError(t, identities.CheckExecutionGoal(ctx, execution.ID, task.ID))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
	require.Error(t, identities.CheckExecutionGoal(ctx, execution.ID, task.ID), "recovery cannot substitute a rebound owner")
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", "sender"))
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	require.Error(t, identities.CheckExecutionGoal(ctx, execution.ID, task.ID), "recovery without request metadata must check saved binding")
	require.Error(t, executions.MarkRunning(ctx, execution.ID), "startup without request metadata must check saved binding")
	require.Error(t, executions.MarkRunning(admitted, execution.ID))
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM executions WHERE id=?`, execution.ID).Scan(&status))
	require.Equal(t, string(models.ExecQueued), status)
}

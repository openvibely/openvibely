package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSlackQueuedInputProvenanceSurvivesRecovery(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
	admitted := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{TeamID: "T123", ExternalUserID: "U123", ProjectID: "default", UserID: models.BootstrapUserID})
	task := createThreadInputTask(t, owner, db, "default")
	agent := createThreadInputLLMConfig(t, owner, db)
	require.NoError(t, NewTaskRepo(db, nil).UpdateStatus(owner, task.ID, models.StatusCompleted))
	inputs := NewThreadInputRepo(db)
	input := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, AgentConfigID: agent.ID, Content: "Continue"}
	_, err := db.Exec(`CREATE TRIGGER fail_input_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='slack.input_admitted' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	require.Error(t, inputs.CreateQueued(admitted, &input))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM slack_input_provenance`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM thread_inputs`).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`DROP TRIGGER fail_input_audit`)
	require.NoError(t, err)
	require.NoError(t, inputs.CreateQueued(admitted, &input))
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	execution := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, IsFollowup: true}
	require.Error(t, inputs.ClaimQueuedForTaskExecution(ctx, input.ID, &execution))
	pending, err := inputs.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputPending, pending.InputStatus)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM executions WHERE task_id=?`, task.ID).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, auth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
	_, err = db.Exec(`CREATE TRIGGER fail_promote_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='execution.run_as' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	require.Error(t, inputs.ClaimQueuedForTaskExecution(ctx, input.ID, &execution))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM slack_execution_provenance`).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`DROP TRIGGER fail_promote_audit`)
	require.NoError(t, err)
	require.NoError(t, inputs.ClaimQueuedForTaskExecution(ctx, input.ID, &execution))
	identities := NewRunIdentityRepo(db)
	require.NoError(t, identities.CheckExecutionGoal(ctx, execution.ID, task.ID))
	require.NoError(t, auth.DeleteForProject(owner, "default", entry.ID))
	require.Error(t, identities.CheckExecutionGoal(ctx, execution.ID, task.ID))
	require.Error(t, NewExecutionRepo(db).MarkRunning(ctx, execution.ID))
}

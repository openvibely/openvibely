package sqlite

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestAutomationContinuationRechecksPublisherBinding(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	auth := NewSlackAuthRepo(db)
	entry := &models.SlackAuthorizedUser{ProjectID: fixture.ProjectID, SlackUserID: "publisher"}
	restore := func() {
		require.NoError(t, auth.Create(owner, entry))
		require.NoError(t, auth.BindApplicationUser(owner, fixture.ProjectID, entry.ID, "workspace", models.BootstrapUserID))
	}
	restore()
	publisher := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{ProjectID: fixture.ProjectID, TeamID: "workspace", ExternalUserID: entry.SlackUserID, UserID: models.BootstrapUserID})
	require.NoError(t, withImmediateTx(ctx, db, func(tx sqlExecutor) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO automation_version_publishers(version_id,user_id) VALUES(?,?)`, fixture.VersionID, models.BootstrapUserID); err != nil {
			return err
		}
		return snapshotSlackPublisher(publisher, tx, fixture.VersionID)
	}))
	tasks := NewTaskRepo(db, nil)
	source := createRuntimeScheduledTask(t, ctx, tasks, fixture.ProjectID, "Source")
	schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, source.ID, fixture.Nodes["trigger"], time.Now().UTC().Add(-time.Minute))
	invocation, _, err := NewAutomationRepo(db).ClaimScheduledOccurrence(owner, schedule, time.Now().UTC(), nil)
	require.NoError(t, err)
	target := createThreadInputTask(t, owner, db, fixture.ProjectID)
	agent := createThreadInputLLMConfig(t, owner, db)
	require.NoError(t, tasks.UpdateStatus(owner, target.ID, models.StatusCompleted))
	inputs := NewThreadInputRepo(db)
	input := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: fixture.ProjectID, TaskID: target.ID, AgentConfigID: agent.ID, Content: "Continue"}
	binding := models.AutomationContext{ProjectID: fixture.ProjectID, Bindings: []models.AutomationBinding{{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID, NodeID: fixture.Nodes["trigger"], InvocationID: invocation.ID}}}
	wrong := binding
	wrong.Bindings = append([]models.AutomationBinding(nil), binding.Bindings...)
	wrong.Bindings[0].AutomationID = "foreign-automation"
	require.ErrorIs(t, inputs.CreateQueuedWithAutomationContext(owner, &input, wrong, "continue"), persistence.ErrProjectDenied)
	require.NoError(t, auth.DeleteForProject(owner, fixture.ProjectID, entry.ID))
	require.ErrorIs(t, inputs.CreateQueuedWithAutomationContext(owner, &input, binding, "continue"), persistence.ErrProjectDenied)
	restore()
	require.NoError(t, inputs.CreateQueuedWithAutomationContext(owner, &input, binding, "continue"))
	require.NoError(t, auth.DeleteForProject(owner, fixture.ProjectID, entry.ID))
	execution := models.Execution{TaskID: target.ID, AgentConfigID: agent.ID, IsFollowup: true}
	require.ErrorIs(t, inputs.ClaimQueuedForTaskExecution(ctx, input.ID, &execution), persistence.ErrProjectDenied)
	pending, err := inputs.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputPending, pending.InputStatus)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM executions WHERE task_id=?`, target.ID).Scan(&count))
	require.Zero(t, count)
	restore()
	_, err = db.Exec(`INSERT INTO users(id,username) VALUES('downgraded-initiator','downgraded-initiator'); INSERT INTO org_memberships VALUES('default','downgraded-initiator','member'); UPDATE automation_invocation_users SET user_id='downgraded-initiator' WHERE invocation_id=?`, invocation.ID)
	require.NoError(t, err)
	require.ErrorIs(t, inputs.ClaimQueuedForTaskExecution(ctx, input.ID, &execution), persistence.ErrProjectDenied, "editor initiator cannot authorize continuation")
	_, err = db.Exec(`UPDATE automation_invocation_users SET user_id=? WHERE invocation_id=?`, models.BootstrapUserID, invocation.ID)
	require.NoError(t, err)
	require.NoError(t, inputs.ClaimQueuedForTaskExecution(ctx, input.ID, &execution))
	require.NoError(t, NewRunIdentityRepo(db).CheckExecutionGoal(ctx, execution.ID, target.ID))
	require.NoError(t, auth.DeleteForProject(owner, fixture.ProjectID, entry.ID))
	require.ErrorIs(t, NewRunIdentityRepo(db).CheckExecutionGoal(ctx, execution.ID, target.ID), persistence.ErrProjectDenied)
	require.ErrorIs(t, NewExecutionRepo(db).MarkRunning(owner, execution.ID), persistence.ErrProjectDenied)
}

func TestAutomationContinuationAuthorRoleAtAdmissionPromotionAndRecovery(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('continuation-author','continuation-author'); INSERT INTO org_memberships VALUES('default','continuation-author','member')`)
	require.NoError(t, err)
	author := models.WithPrincipal(ctx, models.Principal{UserID: "continuation-author", Role: "owner"})
	access := NewProjectAccessRepo(db)
	task := createThreadInputTask(t, owner, db, fixture.ProjectID)
	agent := createThreadInputLLMConfig(t, owner, db)
	require.NoError(t, NewTaskRepo(db, nil).UpdateStatus(owner, task.ID, models.StatusCompleted))
	inputs := NewThreadInputRepo(db)
	input := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: fixture.ProjectID, TaskID: task.ID, AgentConfigID: agent.ID, Content: "Continue"}
	_, err = db.Exec(`INSERT INTO automation_invocations(id,project_id,automation_id,version_id,trigger_node_id,trigger_resource_type,trigger_resource_id,occurrence_key,status) VALUES('author-invocation',?,?,?,?,'manual','test','author-test','running')`, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["trigger"])
	require.NoError(t, err)
	binding := models.AutomationContext{ProjectID: fixture.ProjectID, Bindings: []models.AutomationBinding{{AutomationID: fixture.AutomationID, VersionID: fixture.VersionID, NodeID: fixture.Nodes["trigger"], InvocationID: "author-invocation"}}}
	require.ErrorIs(t, inputs.CreateQueuedWithAutomationContext(author, &input, binding, "continue"), persistence.ErrProjectDenied, "forged owner claim cannot lend editor automation permissions")
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM thread_inputs WHERE task_id=?`, task.ID).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, fixture.ProjectID, "continuation-author", "maintainer"))
	require.NoError(t, inputs.CreateQueuedWithAutomationContext(author, &input, binding, "continue"))
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, fixture.ProjectID, "continuation-author", "editor"))
	execution := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, IsFollowup: true}
	require.ErrorIs(t, inputs.ClaimQueuedForTaskExecution(owner, input.ID, &execution), persistence.ErrProjectDenied, "owner caller cannot substitute for downgraded author")
	pending, err := inputs.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputPending, pending.InputStatus)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM executions WHERE task_id=?`, task.ID).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, fixture.ProjectID, "continuation-author", "maintainer"))
	require.NoError(t, inputs.ClaimQueuedForTaskExecution(ctx, input.ID, &execution))
	require.NoError(t, NewRunIdentityRepo(db).CheckExecutionGoal(ctx, execution.ID, task.ID))
	require.NoError(t, access.SetMember(ctx, models.BootstrapUserID, fixture.ProjectID, "continuation-author", "editor"))
	require.ErrorIs(t, NewRunIdentityRepo(db).CheckExecutionGoal(owner, execution.ID, task.ID), persistence.ErrProjectDenied)
	require.ErrorIs(t, NewExecutionRepo(db).MarkRunning(owner, execution.ID), persistence.ErrProjectDenied)
}

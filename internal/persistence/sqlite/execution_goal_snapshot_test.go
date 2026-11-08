package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestPromotedGoalExecutionSnapshotAndAuditRollback(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	project := models.Project{ID: "default"}
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := createThreadInputLLMConfig(t, ctx, db)
	require.NoError(t, NewTaskRepo(db, nil).UpdateStatus(ctx, task.ID, models.StatusCompleted))
	goal := models.TaskGoal{TaskID: task.ID, GoalID: "original-goal", Objective: "Finish"}
	goals := NewTaskGoalRepo(db)
	require.NoError(t, goals.CreateOrReplace(ctx, &goal))
	input := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, Content: "Continue", Source: models.TaskOriginSystemAgent, OriginAgent: models.AgentSystemKindGoal, GoalID: goal.GoalID}
	inputs := NewThreadInputRepo(db)
	require.NoError(t, inputs.CreateQueued(ctx, &input))
	execution := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, PromptSent: input.Content, IsFollowup: true}
	_, err := db.Exec(`CREATE TRIGGER reject_execution_audit BEFORE INSERT ON project_audit_events WHEN NEW.action='execution.run_as' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	require.NoError(t, err)
	require.Error(t, inputs.ClaimQueuedForTaskExecution(ctx, input.ID, &execution))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM executions WHERE task_id=?`, task.ID).Scan(&count))
	require.Zero(t, count)
	pending, err := inputs.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Empty(t, pending.ExpectedTurnID)
	_, err = db.Exec(`DROP TRIGGER reject_execution_audit`)
	require.NoError(t, err)
	require.NoError(t, inputs.ClaimQueuedForTaskExecution(ctx, input.ID, &execution))
	// A fresh repository models recovery without the original queueing context.
	identities := NewRunIdentityRepo(db)
	userID, found, err := identities.ExecutionUser(t.Context(), execution.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, models.BootstrapUserID, userID)
	require.NoError(t, identities.CheckExecutionGoal(ctx, execution.ID, task.ID))
	require.ErrorIs(t, identities.CheckExecutionGoal(ctx, execution.ID, "other-task"), persistence.ErrProjectDenied)
	replacement := models.TaskGoal{TaskID: task.ID, GoalID: "replacement-goal", Objective: "Different work"}
	require.NoError(t, goals.CreateOrReplace(ctx, &replacement))
	require.ErrorIs(t, identities.CheckExecutionGoal(ctx, execution.ID, task.ID), persistence.ErrProjectDenied)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events WHERE action='execution.run_as' AND target_id=?`, execution.ID).Scan(&count))
	require.Equal(t, 1, count)
}

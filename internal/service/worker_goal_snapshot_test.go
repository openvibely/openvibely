package service

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestRecoveredGoalExecutionKeepsCreatorAndDeniesRestartAfterReplacement(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	org := NewOrganizationService(env.Organizations, "secret")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "goal-recovery-user", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, "editor"))
	userCtx := models.WithPrincipal(ctx, user.Principal)
	task := models.Task{ProjectID: "default", Title: "Goal recovery", Category: models.CategoryBacklog, Status: models.StatusCompleted, Priority: 2}
	require.NoError(t, env.Tasks.Create(owner, &task))
	model := models.LLMConfig{Name: "Recovery model", Provider: "test", Model: "test"}
	require.NoError(t, env.LLMConfigs.Create(ctx, &model))
	goal := models.TaskGoal{TaskID: task.ID, GoalID: "first-goal", Objective: "Finish"}
	require.NoError(t, env.TaskGoals.CreateOrReplace(userCtx, &goal))
	input := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: task.ProjectID, TaskID: task.ID, AgentConfigID: model.ID, Content: "Continue", Source: models.TaskOriginSystemAgent, OriginAgent: models.AgentSystemKindGoal, GoalID: goal.GoalID}
	require.NoError(t, env.ThreadInputs.CreateQueued(userCtx, &input))
	execution := models.Execution{TaskID: task.ID, AgentConfigID: model.ID, PromptSent: input.Content, IsFollowup: true}
	require.NoError(t, env.ThreadInputs.ClaimQueuedForTaskExecutionWithModel(ctx, input.ID, &execution, nil, &model))
	require.NoError(t, env.RunIdentities.BindTask(owner, task.ID))
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	recovered, err := worker.executionPrincipalContext(ctx, task, execution.ID)
	require.NoError(t, err)
	principal, ok := models.PrincipalFromContext(recovered)
	require.True(t, ok)
	require.Equal(t, user.UserID, principal.UserID)
	running := recovered
	replacement := models.TaskGoal{TaskID: task.ID, GoalID: "second-goal", Objective: "Different work"}
	require.NoError(t, env.TaskGoals.CreateOrReplace(userCtx, &replacement))
	_, err = worker.executionPrincipalContext(owner, task, execution.ID)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.NoError(t, running.Err(), "revocation does not stop a run already started")
}

func TestLegacyExecutionRecoveryRejectsMissingAndForeignExecutions(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	task := models.Task{ProjectID: "default", Title: "Legacy webhook", Category: models.CategoryBacklog, Status: models.StatusCompleted, Priority: 2, CreatedVia: models.TaskOriginWebhook}
	require.NoError(t, env.Tasks.Create(owner, &task))
	other := models.Task{ProjectID: "default", Title: "Other task", Category: models.CategoryBacklog, Status: models.StatusCompleted, Priority: 2}
	require.NoError(t, env.Tasks.Create(owner, &other))
	execution := models.Execution{TaskID: task.ID, Status: models.ExecQueued}
	require.NoError(t, env.Executions.Create(ctx, &execution))
	_, found, err := env.RunIdentities.ExecutionUser(ctx, execution.ID)
	require.NoError(t, err)
	require.False(t, found, "fixture must exercise legacy recovery")
	for _, authOff := range []bool{false, true} {
		worker := NewWorkerService(nil, 1, env.Projects)
		worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, authOff)
		_, err = worker.executionPrincipalContext(ctx, task, execution.ID)
		require.NoError(t, err)
		_, err = worker.executionPrincipalContext(owner, other, execution.ID)
		require.ErrorIs(t, err, authz.ErrDenied)
		_, err = worker.executionPrincipalContext(owner, task, "missing-execution")
		require.ErrorIs(t, err, authz.ErrDenied)
	}
}

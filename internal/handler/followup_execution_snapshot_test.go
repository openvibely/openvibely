package handler

import (
	"testing"

	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestFollowupExecutionSnapshotRejectsReassignedCallerAndGoal(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	h.SetRunIdentityStore(tc.stores.RunIdentities)
	h.SetAuthMode(auth.AuthModeLocal)
	owner := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	token, err := org.Invite(owner, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(owner, token, "snapshot-caller", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(owner, models.BootstrapUserID, "default", user.UserID, "editor"))
	caller := models.WithPrincipal(t.Context(), user.Principal)
	task := createTask(t, h, "default", "Execution identity", func(task *models.Task) { task.Category = models.CategoryBacklog; task.Status = models.StatusCompleted })
	model := models.LLMConfig{Name: "Snapshot", Provider: "test", Model: "test"}
	require.NoError(t, tc.stores.LLMConfigs.Create(owner, &model))
	goal := models.TaskGoal{TaskID: task.ID, GoalID: "original", Objective: "Finish"}
	require.NoError(t, tc.stores.TaskGoals.CreateOrReplace(caller, &goal))
	input := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: task.ProjectID, TaskID: task.ID, Content: "Continue", Source: models.TaskOriginSystemAgent, OriginAgent: models.AgentSystemKindGoal, GoalID: goal.GoalID}
	require.NoError(t, tc.stores.ThreadInputs.CreateQueued(caller, &input))
	execution := models.Execution{TaskID: task.ID, AgentConfigID: model.ID, PromptSent: input.Content, IsFollowup: true}
	require.NoError(t, tc.stores.ThreadInputs.ClaimQueuedForTaskExecutionWithModel(owner, input.ID, &execution, nil, &model))
	// Persisted lineage is enforced even if an in-memory caller omits goal metadata.
	params := streamingResponseParams{ExecID: execution.ID, TaskID: task.ID, ProjectID: task.ProjectID}
	require.NoError(t, h.requireFollowupAccess(caller, params))
	require.ErrorIs(t, h.requireFollowupAccess(owner, params), authz.ErrDenied)
	replacement := models.TaskGoal{TaskID: task.ID, GoalID: "replacement", Objective: "Other work"}
	require.NoError(t, tc.stores.TaskGoals.CreateOrReplace(caller, &replacement))
	require.ErrorIs(t, h.requireFollowupAccess(caller, params), authz.ErrDenied)
}

package handler

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestGoalContinuationRestoresCreatorAndRechecks(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := t.Context()
	access := service.NewProjectAccessService(tc.stores.ProjectAccess)
	h.SetProjectAccessService(access)
	h.SetAuthMode(auth.AuthModeLocal)
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	goals := service.NewTaskGoalService(tc.stores.TaskGoals, tc.stores.Tasks, nil)
	goals.SetProjectAuthorization(access, false)
	h.SetTaskGoalService(goals)
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "continuation-user", "long-password-value", "")
	require.NoError(t, err)
	caller := models.WithPrincipal(ctx, user.Principal)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	task := createTask(t, h, "default", "Goal continuation", func(task *models.Task) { task.Category = models.CategoryBacklog })
	_, err = goals.SetGoal(caller, task.ID, "Finish", service.GoalOptions{})
	require.NoError(t, err)
	restored, err := h.resolveGoalContinuation(owner, task.ID, "")
	require.NoError(t, err)
	principal, ok := models.PrincipalFromContext(restored)
	require.True(t, ok)
	require.Equal(t, user.UserID, principal.UserID)
	goalID, _ := restored.Value(continuationGoalKey{}).(string)
	input := models.ThreadInput{GoalID: goalID, TaskID: task.ID, ProjectID: "default", Scope: models.ThreadInputScopeTask, Source: models.TaskOriginSystemAgent, OriginAgent: models.AgentSystemKindGoal, Content: "Continue"}
	require.NoError(t, tc.stores.ThreadInputs.CreateQueued(restored, &input))
	require.Equal(t, user.UserID, input.AuthorUserID)
	reloaded, err := tc.stores.ThreadInputs.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, goalID, reloaded.GoalID)
	input = *reloaded
	_, err = h.queuedGoalContinuationContext(ctx, input)
	require.NoError(t, err)
	require.NoError(t, goals.PauseGoal(owner, task.ID, "owner"))
	_, err = h.queuedGoalContinuationContext(ctx, input)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.NoError(t, goals.ResumeGoal(owner, task.ID, "owner"))
	policy, err := tc.stores.ProjectAccess.Get(ctx, "default")
	require.NoError(t, err)
	policy.OrgRole = "viewer"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	_, err = h.queuedGoalContinuationContext(owner, input)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.ErrorIs(t, h.startQueuedTaskThreadInput(owner, input), authz.ErrDenied)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, "editor"))
	active, cancel := context.WithCancel(caller)
	defer cancel()
	params := streamingResponseParams{TaskID: task.ID, ProjectID: "default", GoalID: input.GoalID, InputOrigin: input.Source, InputOriginAgent: input.OriginAgent}
	require.NoError(t, h.requireFollowupAccess(active, params))
	_, err = goals.SetGoal(caller, task.ID, "Same user replacement", service.GoalOptions{})
	require.NoError(t, err)
	_, err = h.queuedGoalContinuationContext(caller, input)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.NoError(t, active.Err(), "revocation does not stop a run already started")
	// Even a replacement owner's goal cannot retag an already queued message.
	_, err = goals.SetGoal(owner, task.ID, "Replacement", service.GoalOptions{})
	require.NoError(t, err)
	_, err = h.queuedGoalContinuationContext(owner, input)
	require.ErrorIs(t, err, authz.ErrDenied)
	input.AuthorUserID = ""
	_, err = h.queuedGoalContinuationContext(ctx, input)
	require.ErrorIs(t, err, authz.ErrDenied)
	h.SetAuthMode(auth.AuthModeDisabled)
	goals.SetProjectAuthorization(access, true)
	current, err := goals.GetEvaluableGoal(owner, task.ID)
	require.NoError(t, err)
	input.GoalID = current.GoalID
	_, err = h.queuedGoalContinuationContext(ctx, input)
	require.NoError(t, err)
}

package service

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestGoalServiceCurrentPermissions(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	task := models.Task{ProjectID: "default", Title: "Protected goal", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, env.Tasks.Create(owner, &task))
	svc := NewTaskGoalService(env.TaskGoals, env.Tasks, nil)
	svc.SetProjectAuthorization(NewProjectAccessService(env.ProjectAccess), false)
	goal, err := svc.SetGoal(owner, task.ID, "Finish work", GoalOptions{})
	require.NoError(t, err)
	org := NewOrganizationService(env.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "goal-user", "long-password-value", "")
	require.NoError(t, err)
	caller := models.WithPrincipal(ctx, user.Principal)
	policy, err := env.ProjectAccess.Get(ctx, "default")
	require.NoError(t, err)
	policy.OrgRole = "viewer"
	require.NoError(t, env.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	_, err = svc.GetGoal(caller, task.ID)
	require.NoError(t, err)
	callback := false
	svc.SetGoalAchievedHandler(func(_ctx context.Context, _, _ string) { callback = true })
	for name, attempt := range map[string]func() error{
		"set":      func() error { _, e := svc.SetGoal(caller, task.ID, "Replace", GoalOptions{}); return e },
		"clear":    func() error { return svc.ClearGoal(caller, task.ID, "assistant") },
		"pause":    func() error { return svc.PauseGoal(caller, task.ID, "assistant") },
		"resume":   func() error { return svc.ResumeGoal(caller, task.ID, "assistant") },
		"evaluate": func() error { _, e := svc.GetEvaluableGoal(caller, task.ID); return e },
		"achieve":  func() error { _, e := svc.MarkAchieved(caller, task.ID, goal.GoalID, "done"); return e },
		"block": func() error {
			_, e := svc.RecordBlockedReport(caller, task.ID, goal.GoalID, "test", "blocked")
			return e
		},
		"unblock":        func() error { _, e := svc.ClearBlockedReport(caller, task.ID, "test", "cleared"); return e },
		"reactivate":     func() error { _, e := svc.ReactivateAchievedGoal(caller, task.ID, "assistant"); return e },
		"resume stopped": func() error { _, e := svc.ResumeGoalStoppedByUser(caller, task.ID, "assistant"); return e },
	} {
		t.Run(name, func(t *testing.T) { require.ErrorIs(t, attempt(), authz.ErrDenied) })
	}
	require.False(t, callback)
	unchanged, err := env.TaskGoals.GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, goal.GoalID, unchanged.GoalID)
	require.Equal(t, models.TaskGoalStatusActive, unchanged.Status)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, "editor"))
	require.NoError(t, svc.PauseGoal(caller, task.ID, "assistant"))
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, ""))
	require.ErrorIs(t, svc.ResumeGoal(caller, task.ID, "assistant"), authz.ErrDenied)
	policy.Visibility = "private"
	require.NoError(t, env.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	_, err = svc.GetGoal(caller, task.ID)
	require.ErrorIs(t, err, authz.ErrDenied, "knowing a task ID does not grant access")
	_, err = svc.GetGoal(ctx, task.ID)
	require.ErrorIs(t, err, authz.ErrDenied)
	svc.SetProjectAuthorization(NewProjectAccessService(env.ProjectAccess), true)
	require.NoError(t, svc.ResumeGoal(ctx, task.ID, "local"))
}

func TestGoalSavedCreatorSurvivesTaskReassignment(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	org := NewOrganizationService(env.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "goal-creator", "long-password-value", "")
	require.NoError(t, err)
	caller := models.WithPrincipal(ctx, user.Principal)
	task := models.Task{ProjectID: "default", Title: "Durable goal", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, env.Tasks.Create(caller, &task))
	svc := NewTaskGoalService(env.TaskGoals, env.Tasks, nil)
	svc.SetProjectAuthorization(NewProjectAccessService(env.ProjectAccess), false)
	goal, err := svc.SetGoal(caller, task.ID, "Finish", GoalOptions{})
	require.NoError(t, err)
	require.Equal(t, user.UserID, goal.RunAsUserID)
	require.NoError(t, env.RunIdentities.BindTask(owner, task.ID))
	// Restarted services read the saved goal user, not the task's current user.
	svc = NewTaskGoalService(env.TaskGoals, env.Tasks, nil)
	svc.SetProjectAuthorization(NewProjectAccessService(env.ProjectAccess), false)
	loaded, err := svc.GetEvaluableGoal(owner, task.ID)
	require.NoError(t, err)
	require.Equal(t, user.UserID, loaded.RunAsUserID)
	policy, err := env.ProjectAccess.Get(ctx, "default")
	require.NoError(t, err)
	policy.OrgRole = "viewer"
	require.NoError(t, env.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	_, err = svc.GetEvaluableGoal(owner, task.ID)
	require.ErrorIs(t, err, authz.ErrDenied)
	called := false
	svc.SetGoalAchievedHandler(func(context.Context, string, string) { called = true })
	_, err = svc.MarkAchieved(owner, task.ID, goal.GoalID, "done")
	require.ErrorIs(t, err, authz.ErrDenied)
	require.False(t, called)
	stored, err := env.TaskGoals.GetByTaskID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.TaskGoalStatusActive, stored.Status)
	// A deliberate replacement is a new goal, not a silent identity transfer.
	replacement, err := svc.SetGoal(owner, task.ID, "New objective", GoalOptions{})
	require.NoError(t, err)
	require.NotEqual(t, goal.GoalID, replacement.GoalID)
	require.Equal(t, models.BootstrapUserID, replacement.RunAsUserID)
	_, err = svc.GetEvaluableGoal(owner, task.ID)
	require.NoError(t, err)
}

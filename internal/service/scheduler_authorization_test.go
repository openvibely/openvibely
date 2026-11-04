package service

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestScheduleRunIdentityAdmissionRecoveryAndRevocation(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	org := NewOrganizationService(env.Organizations, "secret")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	bob, err := org.Accept(ctx, token, "scheduler-bob", "bob-long-password", "")
	require.NoError(t, err)
	bobCtx := models.WithPrincipal(ctx, bob.Principal)
	task := models.Task{ProjectID: "default", Title: "Owner's task", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, env.Tasks.Create(owner, &task))
	now := time.Now().UTC()
	schedule := models.Schedule{TaskID: task.ID, RunAt: now.Add(-time.Minute), RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
	require.NoError(t, env.Schedules.Create(bobCtx, &schedule))
	userID, err := env.RunIdentities.ScheduleUser(ctx, schedule.ID)
	require.NoError(t, err)
	require.Equal(t, bob.UserID, userID)
	// Editing timing does not silently transfer the schedule's execution identity.
	require.NoError(t, env.Schedules.Update(owner, &schedule))
	userID, err = env.RunIdentities.ScheduleUser(ctx, schedule.ID)
	require.NoError(t, err)
	require.Equal(t, bob.UserID, userID)
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	scheduler := NewSchedulerService(env.Schedules, env.Tasks, worker)
	scheduler.now = func() time.Time { return now }
	policy, err := env.ProjectAccess.Get(ctx, "default")
	require.NoError(t, err)
	policy.OrgRole = "viewer"
	require.NoError(t, env.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	scheduler.checkDueTasks(ctx)
	stored, err := env.Schedules.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.Nil(t, stored.LastRun, "denied occurrence must not be consumed")
	admissions, err := env.Schedules.ListCalendarAdmissions(ctx)
	require.NoError(t, err)
	require.Empty(t, admissions)
	// The transaction also rechecks a principal resolved before revocation.
	claimed, err := env.Tasks.AdmitScheduledCalendarOccurrence(bobCtx, schedule, now, nil)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.False(t, claimed)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", bob.UserID, "editor"))
	scheduler.checkDueTasks(ctx)
	admissions, err = env.Schedules.ListCalendarAdmissions(ctx)
	require.NoError(t, err)
	require.Len(t, admissions, 1)
	require.Equal(t, bob.UserID, admissions[0].RunAsUserID)
	require.NotEmpty(t, admissions[0].ExecutionID)
	// Another user's subsequent task run cannot change this reserved occurrence.
	require.NoError(t, env.RunIdentities.BindTask(owner, task.ID))
	recovered := NewWorkerService(nil, 1, env.Projects)
	recovered.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	runCtx, err := recovered.executionPrincipalContext(ctx, task, admissions[0].ExecutionID)
	require.NoError(t, err)
	principal, ok := models.PrincipalFromContext(runCtx)
	require.True(t, ok)
	require.Equal(t, bob.UserID, principal.UserID)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", bob.UserID, ""))
	_, err = recovered.executionPrincipalContext(ctx, task, admissions[0].ExecutionID)
	require.ErrorIs(t, err, authz.ErrDenied, "task owner's access cannot rescue a revoked scheduled run")
}

func TestScheduleMissingIdentityFailsClosedExceptAuthOff(t *testing.T) {
	for _, authOff := range []bool{false, true} {
		name := "authenticated"
		if authOff {
			name = "auth-off"
		}
		t.Run(name, func(t *testing.T) {
			env := teststores.New(t)
			ctx := t.Context()
			task := models.Task{ProjectID: "default", Title: "Legacy local schedule", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
			require.NoError(t, env.Tasks.Create(ctx, &task))
			schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().Add(-time.Minute), RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
			require.NoError(t, env.Schedules.Create(ctx, &schedule))
			worker := NewWorkerService(nil, 1, env.Projects)
			worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, authOff)
			scheduler := NewSchedulerService(env.Schedules, env.Tasks, worker)
			scheduler.checkDueTasks(ctx)
			admissions, err := env.Schedules.ListCalendarAdmissions(ctx)
			require.NoError(t, err)
			if authOff {
				require.Len(t, admissions, 1)
				require.Equal(t, models.BootstrapUserID, admissions[0].RunAsUserID)
			} else {
				require.Empty(t, admissions)
			}
		})
	}
}

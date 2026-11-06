package service

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestAutomationDispatchAuthorizationBeforeAdmission(t *testing.T) {
	for _, mode := range []string{"missing", "editor", "revoked", "maintainer", "auth-off"} {
		t.Run(mode, func(t *testing.T) {
			f := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC)
			ctx := t.Context()
			worker := NewWorkerService(nil, 1, f.stores.Projects)
			worker.SetAdmissionGate(func() bool { return false })
			worker.SetRunAuthorization(f.stores.RunIdentities, f.stores.Organizations, f.stores.ProjectAccess, mode == "auth-off")
			if mode != "missing" && mode != "auth-off" {
				org := NewOrganizationService(f.stores.Organizations, "secret")
				invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
				require.NoError(t, err)
				user, err := org.Accept(ctx, invite, "automation-user", "long-password-value", "")
				require.NoError(t, err)
				require.NoError(t, f.stores.RunIdentities.BindTask(models.WithPrincipal(ctx, user.Principal), f.task.ID))
				if mode == "maintainer" || mode == "revoked" {
					require.NoError(t, f.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, "maintainer"))
				}
				if mode == "revoked" {
					require.NoError(t, f.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, ""))
				}
			}
			_, dispatch, err := f.repo.ClaimScheduledOccurrence(ctx, f.schedule, time.Now().UTC(), f.schedule.ComputeNextRun(time.Now().UTC()))
			require.NoError(t, err)
			require.NotNil(t, dispatch)
			dispatcher := NewAutomationDispatcher(f.repo, f.taskRepo, worker)
			found, err := dispatcher.DispatchOne(ctx)
			require.True(t, found)
			if mode == "maintainer" || mode == "auth-off" {
				require.NoError(t, err)
				require.Equal(t, 1, worker.QueueSize())
			} else {
				require.ErrorIs(t, err, authz.ErrDenied)
				require.Zero(t, worker.QueueSize())
				state := f.dispatchState(t, dispatch.ID)
				require.Zero(t, state.Executions)
			}
		})
	}
}

func TestAutomationMaintainerDowngradeDeniesNextStart(t *testing.T) {
	f := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC)
	ctx := t.Context()
	org := NewOrganizationService(f.stores.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "running-user", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, f.stores.RunIdentities.BindTask(models.WithPrincipal(ctx, user.Principal), f.task.ID))
	require.NoError(t, f.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, "maintainer"))
	worker := NewWorkerService(nil, 1, f.stores.Projects)
	worker.SetRunAuthorization(f.stores.RunIdentities, f.stores.Organizations, f.stores.ProjectAccess, false)
	automationCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: f.project.ID, OriginTask: true})
	authorized, err := worker.runPrincipalContext(automationCtx, f.task)
	require.NoError(t, err)
	running := authorized
	require.NoError(t, f.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, "editor"))
	_, err = worker.runPrincipalContext(automationCtx, f.task)
	require.ErrorIs(t, err, authz.ErrDenied, "waiting automation must recheck at worker admission")
	require.NoError(t, running.Err(), "revocation does not stop a run already started")
	// The same editor can still run ordinary tasks.
	_, err = worker.runPrincipalContext(ctx, f.task)
	require.NoError(t, err)
}

func TestAutomationEnvelopeUsesInvocationIdentityBeforeExecution(t *testing.T) {
	f := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC)
	ctx := t.Context()
	org := NewOrganizationService(f.stores.Organizations, "secret")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "invocation-runner", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, f.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, "maintainer"))
	actor := models.WithPrincipal(ctx, user.Principal)
	_, dispatches, err := f.repo.ClaimManualAutomationRun(actor, f.project.ID, f.definition.Automation.ID, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, dispatches, 1)
	envelope, err := f.repo.GetDispatchEnvelope(ctx, dispatches[0].ID)
	require.NoError(t, err)
	require.Equal(t, user.UserID, envelope.RunAsUserID)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	require.NoError(t, f.stores.RunIdentities.BindTask(owner, f.task.ID))
	worker := NewWorkerService(nil, 1, f.stores.Projects)
	worker.SetRunAuthorization(f.stores.RunIdentities, f.stores.Organizations, f.stores.ProjectAccess, false)
	restored, err := worker.automationPrincipalContext(ctx, *envelope, "")
	require.NoError(t, err)
	principal, ok := models.PrincipalFromContext(restored)
	require.True(t, ok)
	require.Equal(t, user.UserID, principal.UserID)
	require.NoError(t, f.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, f.project.ID, user.UserID, ""))
	_, err = worker.automationPrincipalContext(owner, *envelope, "")
	require.ErrorIs(t, err, authz.ErrDenied)
}

func TestSlackAutomationBindingRevocationAtWorkerBoundaries(t *testing.T) {
	f := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	entry := &models.SlackAuthorizedUser{ProjectID: f.project.ID, SlackUserID: "worker-sender"}
	require.NoError(t, f.stores.SlackAuth.Create(owner, entry))
	require.NoError(t, f.stores.SlackAuth.BindApplicationUser(owner, f.project.ID, entry.ID, "worker-workspace", models.BootstrapUserID))
	admitted := models.WithSlackIngressIdentity(owner, models.SlackIngressIdentity{TeamID: "worker-workspace", ExternalUserID: entry.SlackUserID, ProjectID: f.project.ID, UserID: models.BootstrapUserID})
	_, dispatches, err := f.repo.ClaimManualAutomationRun(admitted, f.project.ID, f.definition.Automation.ID, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, dispatches, 1)
	envelope, err := f.repo.GetDispatchEnvelope(ctx, dispatches[0].ID)
	require.NoError(t, err)
	worker := NewWorkerService(nil, 1, f.stores.Projects)
	worker.SetRunAuthorization(f.stores.RunIdentities, f.stores.Organizations, f.stores.ProjectAccess, false)
	worker.SetAutomationRepo(f.repo)
	_, err = worker.automationPrincipalContext(ctx, *envelope, "")
	require.NoError(t, err)
	foreign := *envelope
	foreign.Task.ProjectID = "foreign-project"
	_, err = worker.automationPrincipalContext(ctx, foreign, "")
	require.Error(t, err, "cached envelope cannot substitute a project")
	foreign = *envelope
	foreign.RunAsUserID = "substituted-user"
	_, err = worker.automationPrincipalContext(ctx, foreign, "")
	require.Error(t, err, "cached envelope cannot substitute the invocation user")
	require.NoError(t, f.stores.SlackAuth.DeleteForProject(owner, f.project.ID, entry.ID))
	_, err = worker.automationPrincipalContext(owner, *envelope, "")
	require.Error(t, err, "cached queued envelope must not bypass removed binding")
	_, err = f.repo.GetDispatchEnvelope(ctx, dispatches[0].ID)
	require.Error(t, err, "reconciler/dispatcher recovery must reject removed binding")
	require.NoError(t, f.stores.SlackAuth.Create(owner, entry))
	require.NoError(t, f.stores.SlackAuth.BindApplicationUser(owner, f.project.ID, entry.ID, "worker-workspace", models.BootstrapUserID))
	leased, err := f.repo.LeaseNextDispatch(ctx, "worker-test", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, leased)
	execution, err := f.taskRepo.ClaimAutomationDispatch(ctx, leased.ID, "worker-test")
	require.NoError(t, err)
	authorized, err := worker.automationPrincipalContext(ctx, *envelope, execution.ID)
	require.NoError(t, err)
	running := authorized
	require.NoError(t, f.stores.SlackAuth.DeleteForProject(owner, f.project.ID, entry.ID))
	require.NoError(t, running.Err(), "revocation does not stop a run already started")
	_, err = worker.automationPrincipalContext(owner, *envelope, execution.ID)
	require.Error(t, err, "recovered execution must not borrow owner identity")
}

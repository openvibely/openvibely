package service

import (
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestDiscordAutomationBindingRevocationAtWorkerBoundaries(t *testing.T) {
	f := newAutomationRuntimeStoreFixture(t, AutomationAdapterNativeSDLC)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	entry := &models.DiscordAuthorizedUser{ProjectID: f.project.ID, DiscordUserID: "123456789"}
	require.NoError(t, f.stores.DiscordAuth.Create(owner, entry))
	require.NoError(t, f.stores.DiscordAuth.BindApplicationUser(owner, f.project.ID, entry.ID, models.BootstrapUserID))
	admitted := models.WithDiscordIngressIdentity(owner, models.DiscordIngressIdentity{ExternalUserID: entry.DiscordUserID, ProjectID: f.project.ID, UserID: models.BootstrapUserID})
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
	require.NoError(t, f.stores.DiscordAuth.DeleteForProject(owner, f.project.ID, entry.ID))
	_, err = worker.automationPrincipalContext(owner, *envelope, "")
	require.Error(t, err, "cached queued envelope must not bypass removed binding")
	_, err = f.repo.GetDispatchEnvelope(ctx, dispatches[0].ID)
	require.Error(t, err, "reconciler/dispatcher recovery must reject removed binding")
	require.NoError(t, f.stores.DiscordAuth.Create(owner, entry))
	require.NoError(t, f.stores.DiscordAuth.BindApplicationUser(owner, f.project.ID, entry.ID, models.BootstrapUserID))
	leased, err := f.repo.LeaseNextDispatch(ctx, "worker-test", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, leased)
	execution, err := f.taskRepo.ClaimAutomationDispatch(ctx, leased.ID, "worker-test")
	require.NoError(t, err)
	authorized, err := worker.automationPrincipalContext(ctx, *envelope, execution.ID)
	require.NoError(t, err)
	require.NoError(t, f.stores.Executions.MarkRunning(authorized, execution.ID))
	running := authorized
	require.NoError(t, f.stores.DiscordAuth.DeleteForProject(owner, f.project.ID, entry.ID))
	require.NoError(t, running.Err(), "revocation does not stop a run already started")
	_, err = worker.automationPrincipalContext(owner, *envelope, execution.ID)
	require.Error(t, err, "recovered execution must not borrow owner identity")
}

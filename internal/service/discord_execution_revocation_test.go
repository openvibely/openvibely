package service

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestDiscordExecutionBindingRemovalCancelsWorker(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	entry := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: "123456789"}
	require.NoError(t, env.DiscordAuth.Create(owner, entry))
	require.NoError(t, env.DiscordAuth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
	admitted := models.WithDiscordIngressIdentity(owner, models.DiscordIngressIdentity{ProjectID: "default", ExternalUserID: entry.DiscordUserID, UserID: models.BootstrapUserID})
	// No channel origin: verify durable evidence, not the temporary origin block.
	task := models.Task{ProjectID: "default", Title: "Discord provenance worker", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, env.Tasks.Create(admitted, &task))
	execution := models.Execution{TaskID: task.ID, Status: models.ExecQueued, PromptSent: "run"}
	require.NoError(t, env.Executions.Create(owner, &execution))
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	restored, err := worker.executionPrincipalContext(ctx, task, execution.ID)
	require.NoError(t, err)
	require.NoError(t, env.Executions.MarkRunning(restored, execution.ID))
	running, cancel := context.WithCancel(restored)
	defer cancel()
	worker.watchRunAuthorization(running, cancel, task)
	require.NoError(t, env.DiscordAuth.DeleteForProject(owner, "default", entry.ID))
	select {
	case <-running.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("revoked Discord execution remained active")
	}
	_, err = worker.executionPrincipalContext(owner, task, execution.ID)
	require.Error(t, err, "owner session cannot replace saved binding")
}

package service

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestWorkerRejectsMissingIdentityBeforeClaimOrHooks(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	task := models.Task{ProjectID: "default", Title: "No initiating user", Category: models.CategoryActive, Status: models.StatusPending, Priority: 2}
	require.NoError(t, env.Tasks.Create(ctx, &task))
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetTaskRepo(env.Tasks)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	var reachedClaim atomic.Bool
	worker.beforeOrdinaryTaskClaim = func(models.Task) { reachedClaim.Store(true) }
	worker.Start(ctx)
	defer worker.Stop()
	worker.Submit(task)
	require.Eventually(t, func() bool {
		worker.mu.Lock()
		defer worker.mu.Unlock()
		return !worker.pending[task.ID]
	}, 3*time.Second, 10*time.Millisecond)
	require.False(t, reachedClaim.Load(), "unidentified task reached the execution claim")
	saved, err := env.Tasks.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusPending, saved.Status)
}

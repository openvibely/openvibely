package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

type runRoleCounter struct {
	persistence.ProjectAccessStore
	calls atomic.Int64
}

func (s *runRoleCounter) Role(ctx context.Context, user, project string) (string, error) {
	s.calls.Add(1)
	return s.ProjectAccessStore.Role(ctx, user, project)
}

func TestWorkerAlreadyRunningFinishesAfterRevocationWithoutPolling(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	org := NewOrganizationService(env.Organizations, "test")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "running-member", "long-test-password", "")
	require.NoError(t, err)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, "editor"))
	actor := models.WithPrincipal(ctx, user.Principal)
	model := &models.LLMConfig{Name: "Test", Provider: models.ProviderTest, Model: "test-model", AuthMethod: models.AuthMethodAPIKey, IsDefault: true}
	require.NoError(t, env.LLMConfigs.Create(ctx, model))
	task := &models.Task{ProjectID: "default", Title: "Already running", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "Finish work", AgentID: &model.ID, Priority: 2}
	require.NoError(t, env.Tasks.Create(actor, task))
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	caller := testutil.NewMockLLMCaller()
	caller.Response = "Finished"
	caller.OnCall = func(run context.Context, _ testutil.MockLLMCall) {
		started <- run
		select {
		case <-release:
		case <-run.Done():
		}
	}
	llmSvc := NewLLMService(env.LLMConfigs, env.Executions, env.Tasks, env.Projects, nil, env.Attachments)
	llmSvc.SetLLMCaller(caller)
	worker := NewWorkerService(llmSvc, 1, env.Projects)
	worker.SetLLMConfigRepo(env.LLMConfigs)
	worker.SetTaskRepo(env.Tasks)
	worker.SetExecutionRepo(env.Executions)
	counter := &runRoleCounter{ProjectAccessStore: env.ProjectAccess}
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, counter, false)
	worker.Start(ctx)
	defer worker.Stop()
	defer unblock()
	require.NoError(t, worker.SubmitAs(actor, *task))
	var running context.Context
	select {
	case running = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	policy, err := env.ProjectAccess.Get(ctx, "default")
	require.NoError(t, err)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, policy.ProjectID, user.UserID, "viewer"))
	before := counter.calls.Load()
	timer := time.NewTimer(1200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-running.Done():
		t.Fatal("access change cancelled the running task")
	}
	require.Equal(t, before, counter.calls.Load(), "running task polled project access")
	unblock()
	require.Eventually(t, func() bool {
		stored, err := env.Tasks.GetByID(ctx, task.ID)
		return err == nil && stored.Status == models.StatusCompleted
	}, 5*time.Second, 10*time.Millisecond)
	require.ErrorIs(t, worker.SubmitAs(actor, *task), authz.ErrDenied, "next start must be denied")
}

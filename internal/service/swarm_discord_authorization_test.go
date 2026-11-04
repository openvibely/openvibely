package service

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestManualSwarmStartRejectsRevokedDiscordBeforeStateChanges(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	entry := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: "123456789"}
	require.NoError(t, env.DiscordAuth.Create(owner, entry))
	require.NoError(t, env.DiscordAuth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
	admitted := models.WithDiscordIngressIdentity(owner, models.DiscordIngressIdentity{ProjectID: "default", ExternalUserID: entry.DiscordUserID, UserID: models.BootstrapUserID})
	parent := models.Task{ProjectID: "default", Title: "Manual swarm", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2, SwarmRole: models.SwarmRoleParent}
	require.NoError(t, env.Tasks.Create(admitted, &parent))
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	svc := NewSwarmService(NewTaskService(env.Tasks, nil, nil), env.Tasks, env.Executions, worker)
	require.NoError(t, env.DiscordAuth.DeleteForProject(owner, "default", entry.ID))
	require.Error(t, svc.StartPlanner(owner, parent.ID), "owner caller cannot replace saved sender")
	saved, err := env.Tasks.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryBacklog, saved.Category)
	require.Equal(t, models.StatusPending, saved.Status)
	planner, err := env.Tasks.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.Nil(t, planner)
}

func TestSwarmReuseRejectsIndependentlyRevokedChild(t *testing.T) {
	for _, role := range []models.SwarmRole{models.SwarmRolePlanner, models.SwarmRoleWorker, models.SwarmRoleReviewer, models.SwarmRoleMerger} {
		t.Run(string(role), func(t *testing.T) {
			env := teststores.New(t)
			ctx := t.Context()
			owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
			entry := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: "123456789"}
			require.NoError(t, env.DiscordAuth.Create(owner, entry))
			require.NoError(t, env.DiscordAuth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
			admitted := models.WithDiscordIngressIdentity(owner, models.DiscordIngressIdentity{ProjectID: "default", ExternalUserID: entry.DiscordUserID, UserID: models.BootstrapUserID})
			parent := models.Task{ProjectID: "default", Title: "Reusable parent", Category: models.CategoryBacklog, Status: models.StatusCompleted, Priority: 2, SwarmRole: models.SwarmRoleParent}
			require.NoError(t, env.Tasks.Create(owner, &parent))
			child := models.Task{ProjectID: "default", ParentTaskID: &parent.ID, Title: "Reusable child", Category: models.CategoryBacklog, Status: models.StatusCompleted, Priority: 2, SwarmRole: role}
			if role == models.SwarmRoleWorker {
				child.Status = models.StatusPending
			}
			require.NoError(t, env.Tasks.Create(admitted, &child))
			beforeParent, err := env.Tasks.GetByID(ctx, parent.ID)
			require.NoError(t, err)
			beforeChild, err := env.Tasks.GetByID(ctx, child.ID)
			require.NoError(t, err)
			worker := NewWorkerService(nil, 1, env.Projects)
			worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
			svc := NewSwarmService(NewTaskService(env.Tasks, nil, nil), env.Tasks, env.Executions, worker)
			guarded, err := svc.swarmChildRunContext(owner, &parent, &child)
			require.NoError(t, err)
			require.NoError(t, env.DiscordAuth.DeleteForProject(owner, "default", entry.ID))
			require.Error(t, env.Tasks.UpdateStatus(guarded, parent.ID, models.StatusRunning), "revocation after service preflight must block the parent write")
			_, err = worker.runPrincipalContext(owner, parent)
			require.NoError(t, err, "parent remains authorized independently")
			switch role {
			case models.SwarmRolePlanner:
				require.Error(t, svc.StartPlanner(owner, parent.ID))
			case models.SwarmRoleWorker:
				require.Error(t, svc.StartReadyWorkers(owner, parent.ID))
			default:
				_, err = svc.RerunRole(owner, parent.ID, role)
				require.Error(t, err)
			}
			saved, err := env.Tasks.GetByID(ctx, parent.ID)
			require.NoError(t, err)
			require.Equal(t, parent.Category, saved.Category)
			require.Equal(t, parent.Status, saved.Status)
			require.Equal(t, beforeParent.SwarmConfig, saved.SwarmConfig)
			savedChild, err := env.Tasks.GetByID(ctx, child.ID)
			require.NoError(t, err)
			require.Equal(t, child.Category, savedChild.Category)
			require.Equal(t, child.Status, savedChild.Status)
			require.Equal(t, beforeChild.SwarmConfig, savedChild.SwarmConfig)
			foreign := child
			foreign.ProjectID = "foreign"
			_, err = svc.swarmChildRunContext(owner, &parent, &foreign)
			require.Error(t, err)
			if role == models.SwarmRoleReviewer || role == models.SwarmRoleMerger || role == models.SwarmRolePlanner {
				require.NoError(t, env.DiscordAuth.Create(owner, entry))
				require.NoError(t, env.DiscordAuth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
				reached := false
				svc.taskRepo = &swarmBeforeRerunStore{TaskStore: env.Tasks, before: func() {
					reached = true
					require.NoError(t, env.DiscordAuth.DeleteForProject(owner, "default", entry.ID))
				}}
				if role == models.SwarmRolePlanner {
					err = svc.HandleParentFollowup(owner, parent.ID, "new instructions")
				} else {
					_, err = svc.RerunRole(owner, parent.ID, role)
				}
				require.Error(t, err)
				require.True(t, reached, "revocation must occur after service preflight")
				afterParent, err := env.Tasks.GetByID(ctx, parent.ID)
				require.NoError(t, err)
				afterChild, err := env.Tasks.GetByID(ctx, child.ID)
				require.NoError(t, err)
				require.Equal(t, beforeParent, afterParent)
				require.Equal(t, beforeChild, afterChild)
				select {
				case <-worker.Submitted():
					t.Fatal("revoked rerun submitted")
				default:
				}
			}
		})
	}
}

func TestSwarmCompletionRejectsRevokedSourceOrSuccessor(t *testing.T) {
	for _, role := range []models.SwarmRole{models.SwarmRoleReviewer, models.SwarmRoleMerger} {
		for _, revoked := range []string{"source", "successor", "late_source", "late_successor"} {
			t.Run(string(role)+"/"+revoked, func(t *testing.T) {
				env := teststores.New(t)
				ctx := t.Context()
				owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
				bind := func(id string) (*models.DiscordAuthorizedUser, context.Context) {
					entry := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: id}
					require.NoError(t, env.DiscordAuth.Create(owner, entry))
					require.NoError(t, env.DiscordAuth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
					return entry, models.WithDiscordIngressIdentity(owner, models.DiscordIngressIdentity{ProjectID: "default", ExternalUserID: id, UserID: models.BootstrapUserID})
				}
				sourceEntry, sourceCtx := bind("123456789")
				targetEntry, targetCtx := bind("987654321")
				cfg := models.SwarmConfig{Generation: 1, ReviewerEnabled: role == models.SwarmRoleReviewer, MergerEnabled: role == models.SwarmRoleMerger}
				config, err := cfg.JSON()
				require.NoError(t, err)
				parent := models.Task{ProjectID: "default", Title: "Parent", Category: models.CategoryActive, Status: models.StatusRunning, Priority: 2, SwarmRole: models.SwarmRoleParent, SwarmConfig: config}
				require.NoError(t, env.Tasks.Create(owner, &parent))
				sourceCfg := models.SwarmConfig{CompletedGeneration: 1}
				sourceJSON, err := sourceCfg.JSON()
				require.NoError(t, err)
				source := models.Task{ProjectID: "default", Title: "Completed worker", Category: models.CategoryCompleted, Status: models.StatusCompleted, Priority: 2, ParentTaskID: &parent.ID, SwarmRole: models.SwarmRoleWorker, SwarmStatus: "completed", SwarmConfig: sourceJSON}
				require.NoError(t, env.Tasks.Create(sourceCtx, &source))
				target := models.Task{ProjectID: "default", Title: "Successor", Category: models.CategoryBacklog, Status: models.StatusCompleted, Priority: 2, ParentTaskID: &parent.ID, SwarmRole: role}
				require.NoError(t, env.Tasks.Create(targetCtx, &target))
				before, err := env.Tasks.GetByID(ctx, target.ID)
				require.NoError(t, err)
				worker := NewWorkerService(nil, 1, env.Projects)
				worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
				svc := NewSwarmService(NewTaskService(env.Tasks, nil, nil), env.Tasks, env.Executions, worker)
				removed := sourceEntry
				if strings.HasSuffix(revoked, "successor") {
					removed = targetEntry
				}
				lateChecked := false
				if strings.HasPrefix(revoked, "late_") {
					svc.taskRepo = &swarmBeforeReadyWriteStore{TaskStore: env.Tasks, before: func() {
						lateChecked = true
						require.NoError(t, env.DiscordAuth.DeleteForProject(owner, "default", removed.ID))
					}}
				} else {
					require.NoError(t, env.DiscordAuth.DeleteForProject(owner, "default", removed.ID))
				}
				require.Error(t, svc.OnChildCompleted(owner, source.ID))
				if strings.HasPrefix(revoked, "late_") {
					require.True(t, lateChecked, "test must reach persistence after service admission")
				}
				select {
				case <-worker.Submitted():
					t.Fatal("revoked successor was submitted")
				default:
				}
				after, err := env.Tasks.GetByID(ctx, target.ID)
				require.NoError(t, err)
				require.Equal(t, before.Status, after.Status)
				require.Equal(t, before.Category, after.Category)
				require.Equal(t, before.SwarmStatus, after.SwarmStatus)
				require.Equal(t, before.SwarmConfig, after.SwarmConfig)
			})
		}
	}
}

// Inject revocation after the callback's admission check but before its first write.
type swarmBeforeReadyWriteStore struct {
	persistence.TaskStore
	before func()
}

func (s *swarmBeforeReadyWriteStore) PrepareSwarmSuccessor(ctx context.Context, parentID, sourceID string, target *models.Task) error {
	s.before()
	return s.TaskStore.PrepareSwarmSuccessor(ctx, parentID, sourceID, target)
}

type swarmBeforeRerunStore struct {
	persistence.TaskStore
	before func()
}

func (s *swarmBeforeRerunStore) PrepareSwarmRerun(ctx context.Context, parent, child *models.Task, parentConfig, childConfig string) error {
	s.before()
	return s.TaskStore.PrepareSwarmRerun(ctx, parent, child, parentConfig, childConfig)
}

func (s *swarmBeforeRerunStore) PreparePlannerFollowup(ctx context.Context, parent, planner, next *models.Task, parentConfig string) error {
	s.before()
	return s.TaskStore.PreparePlannerFollowup(ctx, parent, planner, next, parentConfig)
}

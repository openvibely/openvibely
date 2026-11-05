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

func TestActiveLaneSwarmStartIsAtomicAndRechecksSavedSender(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		env := teststores.New(t)
		ctx := t.Context()
		owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
		entry := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: "123456789"}
		require.NoError(t, env.DiscordAuth.Create(owner, entry))
		require.NoError(t, env.DiscordAuth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
		admitted := models.WithDiscordIngressIdentity(owner, models.DiscordIngressIdentity{ProjectID: "default", ExternalUserID: entry.DiscordUserID, UserID: models.BootstrapUserID})
		parent := models.Task{ProjectID: "default", Title: "Lane swarm", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2, SwarmRole: models.SwarmRoleParent}
		require.NoError(t, env.Tasks.Create(admitted, &parent))
		worker := NewWorkerService(nil, 1, env.Projects)
		worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
		taskSvc := NewTaskService(env.Tasks, nil, worker)
		svc := NewSwarmService(taskSvc, env.Tasks, env.Executions, worker)
		taskSvc.SetSwarmService(svc)
		taskSvc.beforeActiveLaneLifecycleRoute = func() {
			if revoked {
				require.NoError(t, env.DiscordAuth.DeleteForProject(owner, "default", entry.ID))
			}
		}
		err := taskSvc.MoveTasksToActiveLane(owner, "default", []persistence.ActiveLaneTaskMove{{ID: parent.ID, ExpectedCategory: parent.Category, ExpectedStatus: parent.Status}}, models.StatusRunning)
		saved, loadErr := env.Tasks.GetByID(ctx, parent.ID)
		require.NoError(t, loadErr)
		planner, plannerErr := env.Tasks.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
		require.NoError(t, plannerErr)
		audits, auditErr := env.ProjectAccess.Audit(ctx, "default")
		require.NoError(t, auditErr)
		prepared := 0
		for _, event := range audits {
			if event.Action == "swarm.first_planner_prepared" && event.TargetID == parent.ID {
				prepared++
			}
		}
		if revoked {
			require.Error(t, err)
			require.NotErrorIs(t, err, ErrActiveLaneLifecycleRouted)
			require.Equal(t, models.CategoryBacklog, saved.Category, "revoked sender must leave the parent unchanged")
			require.Equal(t, models.StatusPending, saved.Status)
			require.Nil(t, planner)
			require.Zero(t, prepared)
			continue
		}
		require.ErrorIs(t, err, ErrActiveLaneLifecycleRouted)
		require.Equal(t, models.CategoryActive, saved.Category)
		require.Equal(t, models.StatusBlocked, saved.Status)
		require.NotNil(t, planner)
		require.Equal(t, 1, prepared, "lane startup commits its audit with the planner")
	}
}

func TestParentFollowupFirstPlannerRejectsLateRevocationWithoutPartialState(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	entry := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: "123456789"}
	require.NoError(t, env.DiscordAuth.Create(owner, entry))
	require.NoError(t, env.DiscordAuth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
	admitted := models.WithDiscordIngressIdentity(owner, models.DiscordIngressIdentity{ProjectID: "default", ExternalUserID: entry.DiscordUserID, UserID: models.BootstrapUserID})
	parent := models.Task{ProjectID: "default", Title: "Followup swarm", Category: models.CategoryCompleted, Status: models.StatusCompleted, Priority: 2, SwarmRole: models.SwarmRoleParent, SwarmConfig: `{"generation":1}`}
	require.NoError(t, env.Tasks.Create(admitted, &parent))
	before, err := env.Tasks.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	svc := NewSwarmService(NewTaskService(env.Tasks, nil, nil), env.Tasks, env.Executions, worker)
	reached := false
	svc.taskRepo = &swarmBeforePlannerLookupStore{TaskStore: env.Tasks, before: func() {
		reached = true
		require.NoError(t, env.DiscordAuth.DeleteForProject(owner, "default", entry.ID))
	}}
	require.Error(t, svc.HandleParentFollowup(owner, parent.ID, "more work"))
	require.True(t, reached, "revocation must occur after service preflight")
	after, err := env.Tasks.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	planner, err := env.Tasks.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.Nil(t, planner)

	require.NoError(t, env.DiscordAuth.Create(owner, entry))
	require.NoError(t, env.DiscordAuth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
	svc.taskRepo = env.Tasks
	require.NoError(t, svc.HandleParentFollowup(owner, parent.ID, "more work"))
	after, err = env.Tasks.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	cfg, err := models.ParseSwarmConfig(after.SwarmConfig)
	require.NoError(t, err)
	require.Equal(t, 2, cfg.Generation)
	require.Equal(t, models.StatusBlocked, after.Status)
	planner, err = env.Tasks.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	planner, err = env.Tasks.GetByID(ctx, planner.ID)
	require.NoError(t, err)
	require.Contains(t, planner.Prompt, "more work")
}

// Revocation lands after HandleParentFollowup authorizes the parent.
type swarmBeforePlannerLookupStore struct {
	persistence.TaskStore
	before func()
	done   bool
}

func (s *swarmBeforePlannerLookupStore) FindSwarmChildByRole(ctx context.Context, parentID string, role models.SwarmRole) (*models.Task, error) {
	if !s.done {
		s.done = true
		s.before()
	}
	return s.TaskStore.FindSwarmChildByRole(ctx, parentID, role)
}

type swarmBeforePlanCommitStore struct {
	persistence.TaskStore
	before func()
}

func (s *swarmBeforePlanCommitStore) ApplySwarmPlan(ctx context.Context, commit persistence.SwarmPlanCommit) error {
	s.before()
	return s.TaskStore.ApplySwarmPlan(ctx, commit)
}

func TestPlanApplicationIsAtomicAndRunsAsSavedPlannerIdentity(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	entry := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: "123456789"}
	require.NoError(t, env.DiscordAuth.Create(owner, entry))
	require.NoError(t, env.DiscordAuth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
	admitted := models.WithDiscordIngressIdentity(owner, models.DiscordIngressIdentity{ProjectID: "default", ExternalUserID: entry.DiscordUserID, UserID: models.BootstrapUserID})
	parentCfg, err := (models.SwarmConfig{Generation: 1, MaxWorkers: 3, ReviewerEnabled: true}).JSON()
	require.NoError(t, err)
	parent := models.Task{ProjectID: "default", Title: "Plan swarm", Prompt: "Build", Category: models.CategoryActive, Status: models.StatusBlocked, Priority: 2, SwarmRole: models.SwarmRoleParent, SwarmStatus: "planning", SwarmConfig: parentCfg}
	require.NoError(t, env.Tasks.Create(admitted, &parent))
	planner := models.Task{ProjectID: "default", Title: "Plan swarm · Planner", Prompt: "Plan", Category: models.CategoryActive, Status: models.StatusCompleted, Priority: 2, ParentTaskID: &parent.ID, SwarmRole: models.SwarmRolePlanner, SwarmStatus: "planning", SwarmConfig: `{"rerun_generation":1}`}
	require.NoError(t, env.Tasks.Create(admitted, &planner))
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	svc := NewSwarmService(NewTaskService(env.Tasks, nil, nil), env.Tasks, env.Executions, worker)
	output := PlannerOutput{ReviewerPrompt: "Review", Workers: []PlannerWorker{
		{Title: "Backend", Prompt: "Build backend", WorkerKind: "backend", Ownership: []string{"internal/a"}, Isolation: "worktree", Required: true},
		{Title: "Frontend", Prompt: "Build frontend", WorkerKind: "frontend", Ownership: []string{"web"}, Isolation: "worktree", Required: true},
	}}
	beforeParent, err := env.Tasks.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	beforePlanner, err := env.Tasks.GetByID(ctx, planner.ID)
	require.NoError(t, err)

	reached := false
	svc.taskRepo = &swarmBeforePlanCommitStore{TaskStore: env.Tasks, before: func() {
		reached = true
		require.NoError(t, env.DiscordAuth.DeleteForProject(owner, "default", entry.ID))
	}}
	require.Error(t, svc.ApplyPlannerOutput(owner, planner.ID, output))
	require.True(t, reached, "revocation must land after service preflight")
	children, err := env.Tasks.ListSwarmChildren(ctx, parent.ID)
	require.NoError(t, err)
	require.Len(t, children, 1, "no worker or reviewer survives a rejected plan")
	afterParent, err := env.Tasks.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	require.Equal(t, beforeParent, afterParent)
	afterPlanner, err := env.Tasks.GetByID(ctx, planner.ID)
	require.NoError(t, err)
	require.Equal(t, beforePlanner, afterPlanner)
	select {
	case <-worker.Submitted():
		t.Fatal("rejected plan submitted a worker")
	default:
	}

	require.NoError(t, env.DiscordAuth.Create(owner, entry))
	require.NoError(t, env.DiscordAuth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
	svc.taskRepo = env.Tasks
	// A caller-supplied principal must not replace the planner's saved identity.
	require.NoError(t, svc.ApplyPlannerOutput(ctx, planner.ID, output))
	children, err = env.Tasks.ListSwarmChildren(ctx, parent.ID)
	require.NoError(t, err)
	require.Len(t, children, 4)
	for _, child := range children {
		user, err := env.RunIdentities.TaskUser(ctx, child.ID)
		require.NoError(t, err)
		require.Equal(t, models.BootstrapUserID, user, child.Title)
		if child.SwarmRole == models.SwarmRoleWorker {
			require.Contains(t, child.WorktreeBranch, shortID(child.ID), "worktree is named from the committed ID")
		}
	}
	planned, err := env.Tasks.GetByID(ctx, planner.ID)
	require.NoError(t, err)
	require.Equal(t, "planned", planned.SwarmStatus)
	require.Equal(t, models.StatusCompleted, planned.Status)
	audits, err := env.ProjectAccess.Audit(ctx, "default")
	require.NoError(t, err)
	applied := 0
	for _, event := range audits {
		if event.Action == "swarm.plan_applied" && event.TargetID == parent.ID {
			applied++
		}
	}
	require.Equal(t, 1, applied)
	submitted := map[string]bool{}
	for len(submitted) < 2 {
		select {
		case task := <-worker.Submitted():
			submitted[task.ID] = true
		default:
			t.Fatalf("workers were not submitted after commit: %v", submitted)
		}
	}
}

func TestCommittedPlanWorkersAreRecoverableBeforeSubmission(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	svc := NewSwarmService(NewTaskService(env.Tasks, nil, nil), env.Tasks, env.Executions, nil)
	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Recover plan", Prompt: "Build", MaxWorkers: 2})
	require.NoError(t, err)
	planner, err := env.Tasks.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NoError(t, svc.ApplyPlannerOutput(ctx, planner.ID, PlannerOutput{Workers: []PlannerWorker{
		{Title: "One", Prompt: "Do one", WorkerKind: "backend", Ownership: []string{"a"}, Isolation: "worktree", Required: true},
		{Title: "Two", Prompt: "Do two", WorkerKind: "backend", Ownership: []string{"b"}, Isolation: "worktree", Required: true},
	}}))
	admissions, err := env.Tasks.ListActivePendingAdmissions(ctx)
	require.NoError(t, err)
	recovered := 0
	for _, admission := range admissions {
		if admission.ParentTaskID != nil && *admission.ParentTaskID == parent.ID && admission.SwarmRole == models.SwarmRoleWorker {
			recovered++
		}
	}
	require.Equal(t, 2, recovered, "the scheduler's pending scan resubmits workers after a crash before submission")
}

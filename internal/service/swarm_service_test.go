package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func requireFullSwarmTestTask(t testing.TB, repo *repository.TaskRepo, id string) *models.Task {
	t.Helper()
	task, err := repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, task)
	return task
}

func TestAttachSwarmChildrenPreservesPreviouslyAttachedChildren(t *testing.T) {
	parent := models.Task{ID: "parent", SwarmRole: models.SwarmRoleParent}
	child := models.Task{ID: "child", SwarmRole: models.SwarmRoleWorker, Status: models.StatusRunning, SwarmSequence: 1}
	parent.SwarmChildren = []models.Task{child}

	attached := AttachSwarmChildren([]models.Task{parent})
	if len(attached) != 1 {
		t.Fatalf("expected one top-level task, got %d", len(attached))
	}
	if len(attached[0].SwarmChildren) != 1 || attached[0].SwarmChildren[0].ID != child.ID {
		t.Fatalf("expected attached child to be preserved, got %#v", attached[0].SwarmChildren)
	}
}

func TestSwarmServiceCreateAndApplyPlannerOutput(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, nil, nil)

	parent, err := svc.CreateSwarmTask(context.Background(), CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export", Prompt: "Build export", MaxWorkers: 3, WorkerIsolation: "worktree", ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatalf("CreateSwarmTask: %v", err)
	}
	if parent.SwarmRole != models.SwarmRoleParent {
		t.Fatalf("parent role=%q", parent.SwarmRole)
	}
	children, err := repo.ListSwarmChildren(context.Background(), parent.ID)
	if err != nil {
		t.Fatalf("ListSwarmChildren: %v", err)
	}
	if len(children) != 1 || children[0].SwarmRole != models.SwarmRolePlanner {
		t.Fatalf("planner child not created: %#v", children)
	}

	output := PlannerOutput{Workers: []PlannerWorker{{Title: "Backend worker", Prompt: "Do backend", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", WriteScope: []string{"internal/service"}, Required: true}}, ReviewerPrompt: "Review workers", MergerPrompt: "Integrate workers"}
	if err := svc.ApplyPlannerOutput(context.Background(), children[0].ID, output); err != nil {
		t.Fatalf("ApplyPlannerOutput: %v", err)
	}
	children, err = repo.ListSwarmChildren(context.Background(), parent.ID)
	if err != nil {
		t.Fatalf("ListSwarmChildren after planner: %v", err)
	}
	counts := map[models.SwarmRole]int{}
	for _, child := range children {
		counts[child.SwarmRole]++
	}
	if counts[models.SwarmRoleWorker] != 1 || counts[models.SwarmRoleReviewer] != 1 || counts[models.SwarmRoleMerger] != 1 {
		t.Fatalf("unexpected children: %#v", counts)
	}
	var workerChild *models.Task
	for i := range children {
		if children[i].SwarmRole == models.SwarmRoleWorker {
			workerChild = &children[i]
		}
	}
	if workerChild == nil || workerChild.ParentTaskID == nil || *workerChild.ParentTaskID != parent.ID {
		t.Fatalf("worker parent not set: %#v", workerChild)
	}
	if workerChild.WorktreePath == "" || workerChild.WorktreeBranch == "" {
		t.Fatalf("worker worktree metadata missing: %#v", workerChild)
	}
}

func TestSwarmServiceApplyPlannerOutputDisambiguatesExistingWorkerTitle(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, nil, nil)

	existing := &models.Task{ProjectID: "default", Title: "Backend worker", Prompt: "Previous unrelated task", Category: models.CategoryCompleted, Status: models.StatusCompleted}
	if err := taskSvc.Create(ctx, existing); err != nil {
		t.Fatalf("create existing task: %v", err)
	}
	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export again", Prompt: "Build export", MaxWorkers: 1, WorkerIsolation: "worktree", ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatalf("CreateSwarmTask: %v", err)
	}
	planner, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: planner=%#v err=%v", planner, err)
	}

	output := PlannerOutput{Workers: []PlannerWorker{{Title: existing.Title, Prompt: "Do backend", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", Required: true}}, ReviewerPrompt: "Review", MergerPrompt: "Merge"}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, output); err != nil {
		t.Fatalf("ApplyPlannerOutput with an existing worker title: %v", err)
	}
	children, err := repo.ListSwarmChildren(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSwarmChildren: %v", err)
	}
	counts := map[models.SwarmRole]int{}
	for _, child := range children {
		counts[child.SwarmRole]++
		if child.SwarmRole == models.SwarmRoleWorker && child.Title == existing.Title {
			t.Fatalf("worker title was not disambiguated: %q", child.Title)
		}
	}
	if counts[models.SwarmRoleWorker] != 1 || counts[models.SwarmRoleReviewer] != 1 || counts[models.SwarmRoleMerger] != 1 {
		t.Fatalf("unexpected children after title collision: %#v", counts)
	}
}

func TestSwarmServiceApplyPlannerOutputRecoversPartialCreationAndRepeatedTitleCollisions(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, nil, nil)

	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Recover export swarm", Prompt: "Build export", MaxWorkers: 2, WorkerIsolation: "worktree", ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatalf("CreateSwarmTask: %v", err)
	}
	planner, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: planner=%#v err=%v", planner, err)
	}
	parentPrefix := parent.ID[:8]
	reservedTitles := []string{
		"Second worker",
		fmt.Sprintf("Second worker · %s-11", parentPrefix),
		parent.Title + " · Reviewer",
		fmt.Sprintf("%s · Reviewer · %s-900", parent.Title, parentPrefix),
		parent.Title + " · Merger",
		fmt.Sprintf("%s · Merger · %s-1000", parent.Title, parentPrefix),
	}
	for _, title := range reservedTitles {
		unrelated := &models.Task{ProjectID: parent.ProjectID, Title: title, Prompt: "Unrelated task", Category: models.CategoryCompleted, Status: models.StatusCompleted}
		if err := taskSvc.Create(ctx, unrelated); err != nil {
			t.Fatalf("reserve title %q: %v", title, err)
		}
	}

	partialWorker := &models.Task{ProjectID: parent.ProjectID, Title: "First worker", Prompt: "Do first part", Category: models.CategoryActive, Status: models.StatusPending, ParentTaskID: &parent.ID, SwarmRole: models.SwarmRoleWorker, SwarmStatus: "running", SwarmConfig: `{"required":true,"rerun_generation":1}`, SwarmSequence: 10}
	if err := taskSvc.Create(ctx, partialWorker); err != nil {
		t.Fatalf("create partial worker: %v", err)
	}
	output := PlannerOutput{Workers: []PlannerWorker{
		{Title: "First worker", Prompt: "Do first part", WorkerKind: "backend", Ownership: []string{"internal/first"}, Isolation: "worktree", Required: true},
		{Title: "Second worker", Prompt: "Do second part", WorkerKind: "backend", Ownership: []string{"internal/second"}, Isolation: "worktree", Required: true},
	}, ReviewerPrompt: "Review", MergerPrompt: "Merge"}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, output); err != nil {
		t.Fatalf("resume partial planner application: %v", err)
	}

	children, err := repo.ListSwarmChildren(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSwarmChildren: %v", err)
	}
	counts := map[models.SwarmRole]int{}
	for _, child := range children {
		counts[child.SwarmRole]++
		for _, reserved := range reservedTitles {
			if child.Title == reserved {
				t.Fatalf("swarm child reused reserved title %q", child.Title)
			}
		}
	}
	if counts[models.SwarmRoleWorker] != 2 || counts[models.SwarmRoleReviewer] != 1 || counts[models.SwarmRoleMerger] != 1 {
		t.Fatalf("partial application was not completed exactly once: %#v", counts)
	}
	storedPlanner, err := repo.GetByID(ctx, planner.ID)
	if err != nil || storedPlanner == nil || storedPlanner.SwarmStatus != "planned" {
		t.Fatalf("planner not terminalized after recovery: planner=%#v err=%v", storedPlanner, err)
	}
}

func TestSwarmServiceStartPlannerDisambiguatesRepeatedTitleCollisions(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, nil, nil)

	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Deferred collision swarm", Prompt: "Build export", Category: models.CategoryBacklog, MaxWorkers: 1})
	if err != nil {
		t.Fatalf("CreateSwarmTask: %v", err)
	}
	baseTitle := parent.Title + " · Planner"
	for _, title := range []string{baseTitle, fmt.Sprintf("%s · %s-0", baseTitle, parent.ID[:8])} {
		unrelated := &models.Task{ProjectID: parent.ProjectID, Title: title, Prompt: "Unrelated task", Category: models.CategoryCompleted, Status: models.StatusCompleted}
		if err := taskSvc.Create(ctx, unrelated); err != nil {
			t.Fatalf("reserve title %q: %v", title, err)
		}
	}
	if err := svc.StartPlanner(ctx, parent.ID); err != nil {
		t.Fatalf("StartPlanner: %v", err)
	}
	planner, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: planner=%#v err=%v", planner, err)
	}
	if planner.Title == baseTitle || planner.Title == fmt.Sprintf("%s · %s-0", baseTitle, parent.ID[:8]) {
		t.Fatalf("planner title was not disambiguated beyond occupied fallbacks: %q", planner.Title)
	}
}

func TestSwarmServiceScheduledStartMarksExistingPlannerBoundary(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	workerSvc := newTestWorkerService(t)
	svc := NewSwarmService(taskSvc, repo, nil, nil)
	parent, err := svc.CreateSwarmTask(context.Background(), CreateSwarmTaskRequest{
		ProjectID: "default", Title: "Recurring scheduled swarm", Prompt: "Plan again", MaxWorkers: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	planner, err := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: %v", err)
	}
	if err := repo.UpdateCategory(context.Background(), planner.ID, models.CategoryBacklog); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(context.Background(), planner.ID, models.StatusFailed); err != nil {
		t.Fatal(err)
	}
	svc.workerSvc = workerSvc

	if err := svc.StartPlannerForScheduledRun(context.Background(), parent.ID, true); err != nil {
		t.Fatalf("StartPlannerForScheduledRun: %v", err)
	}
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != planner.ID {
			t.Fatalf("submitted task ID=%s, want planner %s", submitted.ID, planner.ID)
		}
		if !submitted.StartsNewContext {
			t.Fatal("scheduled planner restart must carry the new-context boundary")
		}
	default:
		t.Fatal("expected planner to be submitted")
	}
}

func TestSwarmServicePlannerCallbackPersistsApplicationError(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, repository.NewExecutionRepo(db), nil)

	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Planner callback error", Prompt: "Build export", MaxWorkers: 1})
	if err != nil {
		t.Fatalf("CreateSwarmTask: %v", err)
	}
	planner, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: planner=%#v err=%v", planner, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := svc.applyCompletedPlannerExecution(cancelled, planner); !errors.Is(err, context.Canceled) {
		t.Fatalf("applyCompletedPlannerExecution error = %v, want context canceled", err)
	}

	storedParent, err := repo.GetByID(ctx, parent.ID)
	if err != nil || storedParent == nil {
		t.Fatalf("load parent: parent=%#v err=%v", storedParent, err)
	}
	parentCfg, _ := models.ParseSwarmConfig(storedParent.SwarmConfig)
	if !strings.Contains(parentCfg.LastError, "context canceled") || storedParent.SwarmStatus != "blocked" {
		t.Fatalf("parent callback failure not persisted: status=%q error=%q", storedParent.SwarmStatus, parentCfg.LastError)
	}
	storedPlanner, err := repo.GetByID(ctx, planner.ID)
	if err != nil || storedPlanner == nil {
		t.Fatalf("load planner: planner=%#v err=%v", storedPlanner, err)
	}
	plannerCfg, _ := models.ParseSwarmConfig(storedPlanner.SwarmConfig)
	if !strings.Contains(plannerCfg.LastError, "context canceled") || storedPlanner.SwarmStatus != "plan_apply_failed" {
		t.Fatalf("planner callback failure not persisted: status=%q error=%q", storedPlanner.SwarmStatus, plannerCfg.LastError)
	}
}

func TestSwarmServiceFollowupPlannerApplicationErrorPreservesRetryRouting(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, execRepo, nil)

	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Retry follow-up plan", Prompt: "Build export", MaxWorkers: 1})
	if err != nil {
		t.Fatalf("CreateSwarmTask: %v", err)
	}
	planner, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: planner=%#v err=%v", planner, err)
	}
	initialOutput := PlannerOutput{Workers: []PlannerWorker{{Title: "Backend worker", Prompt: "Build backend", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", Required: true}}}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, initialOutput); err != nil {
		t.Fatalf("apply initial plan: %v", err)
	}
	worker, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	if err != nil || worker == nil {
		t.Fatalf("worker missing: worker=%#v err=%v", worker, err)
	}
	if err := repo.UpdateStatus(ctx, worker.ID, models.StatusCompleted); err != nil {
		t.Fatalf("complete worker: %v", err)
	}
	if err := svc.HandleParentFollowup(ctx, parent.ID, "Update the backend"); err != nil {
		t.Fatalf("HandleParentFollowup: %v", err)
	}
	planner, err = repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("coordinating planner missing: planner=%#v err=%v", planner, err)
	}
	followupJSON := fmt.Sprintf(`{"workers":[{"task_id":%q,"title":"Backend worker","prompt":"Update backend","worker_kind":"backend","ownership":["internal/service"],"isolation":"worktree","required":true}]}`, worker.ID)
	exec := &models.Execution{TaskID: planner.ID, Status: models.ExecRunning, PromptSent: requireFullSwarmTestTask(t, repo, planner.ID).Prompt}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create follow-up planner execution: %v", err)
	}
	if err := execRepo.Complete(ctx, exec.ID, models.ExecCompleted, followupJSON, "", 0, 1); err != nil {
		t.Fatalf("complete follow-up planner execution: %v", err)
	}
	if err := repo.UpdateStatus(ctx, planner.ID, models.StatusCompleted); err != nil {
		t.Fatalf("complete planner task: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := svc.applyCompletedPlannerExecution(cancelled, planner); !errors.Is(err, context.Canceled) {
		t.Fatalf("first application error = %v, want context canceled", err)
	}
	failedPlanner, err := repo.GetByID(ctx, planner.ID)
	if err != nil || failedPlanner == nil {
		t.Fatalf("load failed planner: planner=%#v err=%v", failedPlanner, err)
	}
	failedParent, err := repo.GetByID(ctx, parent.ID)
	if err != nil || failedParent == nil {
		t.Fatalf("load failed parent: parent=%#v err=%v", failedParent, err)
	}
	if failedPlanner.SwarmStatus != "coordinating" || failedParent.SwarmStatus != "needs_coordination" {
		t.Fatalf("follow-up phase was lost after failure: planner=%q parent=%q", failedPlanner.SwarmStatus, failedParent.SwarmStatus)
	}

	if err := svc.applyCompletedPlannerExecution(ctx, failedPlanner); err != nil {
		t.Fatalf("retry completed follow-up output: %v", err)
	}
	updatedWorker, err := repo.GetByID(ctx, worker.ID)
	if err != nil || updatedWorker == nil {
		t.Fatalf("load updated worker: worker=%#v err=%v", updatedWorker, err)
	}
	workerCfg, _ := models.ParseSwarmConfig(updatedWorker.SwarmConfig)
	if updatedWorker.Status != models.StatusPending || updatedWorker.SwarmStatus != "rerun_pending" || workerCfg.RerunGeneration != 2 {
		t.Fatalf("retry did not apply as follow-up: worker=%#v cfg=%#v", updatedWorker, workerCfg)
	}
	children, err := repo.ListSwarmChildren(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSwarmChildren: %v", err)
	}
	workerCount := 0
	for _, child := range children {
		if child.SwarmRole == models.SwarmRoleWorker {
			workerCount++
		}
	}
	if workerCount != 1 {
		t.Fatalf("retry created duplicate workers: %d", workerCount)
	}
	recoveredPlanner, err := repo.GetByID(ctx, planner.ID)
	if err != nil || recoveredPlanner == nil {
		t.Fatalf("load recovered planner: planner=%#v err=%v", recoveredPlanner, err)
	}
	recoveredParent, err := repo.GetByID(ctx, parent.ID)
	if err != nil || recoveredParent == nil {
		t.Fatalf("load recovered parent: parent=%#v err=%v", recoveredParent, err)
	}
	recoveredPlannerCfg, _ := models.ParseSwarmConfig(recoveredPlanner.SwarmConfig)
	recoveredParentCfg, _ := models.ParseSwarmConfig(recoveredParent.SwarmConfig)
	if recoveredPlanner.SwarmStatus != "planned" || recoveredPlannerCfg.LastError != "" || recoveredParentCfg.LastError != "" {
		t.Fatalf("successful retry did not clear failure state: planner=%#v planner_cfg=%#v parent_cfg=%#v", recoveredPlanner, recoveredPlannerCfg, recoveredParentCfg)
	}
}

func TestSwarmServiceHandleParentFollowupPreservesPlannerChainConfig(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, nil, nil)

	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Planner chain config", Prompt: "Build export", MaxWorkers: 1})
	if err != nil {
		t.Fatalf("CreateSwarmTask: %v", err)
	}
	planner, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: planner=%#v err=%v", planner, err)
	}
	fullPlanner := requireFullSwarmTestTask(t, repo, planner.ID)
	fullPlanner.ChainConfig = `{"enabled":true,"trigger":"on_completion"}`
	if err := repo.Update(ctx, fullPlanner); err != nil {
		t.Fatalf("seed planner chain config: %v", err)
	}

	if err := svc.HandleParentFollowup(ctx, parent.ID, "Update the plan"); err != nil {
		t.Fatalf("HandleParentFollowup: %v", err)
	}

	updatedPlanner := requireFullSwarmTestTask(t, repo, planner.ID)
	if updatedPlanner.ChainConfig != fullPlanner.ChainConfig {
		t.Fatalf("planner chain config = %q, want %q", updatedPlanner.ChainConfig, fullPlanner.ChainConfig)
	}
}

func TestSwarmServiceFollowupPlannerRerunDisambiguatesOccupiedWorkerTitle(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, nil, nil)

	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Rerun title collision", Prompt: "Build export", MaxWorkers: 1})
	if err != nil {
		t.Fatalf("CreateSwarmTask: %v", err)
	}
	planner, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: planner=%#v err=%v", planner, err)
	}
	initialOutput := PlannerOutput{Workers: []PlannerWorker{{Title: "Original worker", Prompt: "Build backend", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", Required: true}}}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, initialOutput); err != nil {
		t.Fatalf("apply initial plan: %v", err)
	}
	worker, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	if err != nil || worker == nil {
		t.Fatalf("worker missing: worker=%#v err=%v", worker, err)
	}
	fullWorker := requireFullSwarmTestTask(t, repo, worker.ID)
	fullWorker.ChainConfig = `{"enabled":true,"child":"worker"}`
	if err := repo.Update(ctx, fullWorker); err != nil {
		t.Fatalf("seed worker chain config: %v", err)
	}
	if err := repo.UpdateStatus(ctx, worker.ID, models.StatusCompleted); err != nil {
		t.Fatalf("complete worker: %v", err)
	}
	occupied := &models.Task{ProjectID: parent.ProjectID, Title: "Occupied rerun title", Prompt: "Unrelated task", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := taskSvc.Create(ctx, occupied); err != nil {
		t.Fatalf("create occupied title: %v", err)
	}
	occupiedFallback := &models.Task{ProjectID: parent.ProjectID, Title: swarmChildTitle(occupied.Title, parent.ID, worker.SwarmSequence, 1), Prompt: "Another unrelated task", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := taskSvc.Create(ctx, occupiedFallback); err != nil {
		t.Fatalf("create occupied fallback title: %v", err)
	}
	if err := svc.HandleParentFollowup(ctx, parent.ID, "Update the backend"); err != nil {
		t.Fatalf("HandleParentFollowup: %v", err)
	}
	planner, err = repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("coordinating planner missing: planner=%#v err=%v", planner, err)
	}

	followup := PlannerOutput{Workers: []PlannerWorker{{TaskID: worker.ID, Title: occupied.Title, Prompt: "Update backend safely", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", Required: true}}}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, followup); err != nil {
		t.Fatalf("apply follow-up with occupied title: %v", err)
	}
	updated, err := repo.GetByID(ctx, worker.ID)
	if err != nil || updated == nil {
		t.Fatalf("load rerun worker: worker=%#v err=%v", updated, err)
	}
	cfg, _ := models.ParseSwarmConfig(updated.SwarmConfig)
	if updated.Title == occupied.Title || updated.Title == occupiedFallback.Title || !strings.HasPrefix(updated.Title, occupied.Title+" · ") {
		t.Fatalf("rerun title was not disambiguated beyond occupied fallbacks: %q", updated.Title)
	}
	if updated.Status != models.StatusPending || updated.SwarmStatus != "rerun_pending" || cfg.RerunGeneration != 2 {
		t.Fatalf("rerun state not persisted: worker=%#v cfg=%#v", updated, cfg)
	}
	if !strings.Contains(updated.Prompt, "Update backend safely") {
		t.Fatalf("rerun prompt not updated: %q", updated.Prompt)
	}
	if updated.ChainConfig != fullWorker.ChainConfig {
		t.Fatalf("worker chain config = %q, want %q", updated.ChainConfig, fullWorker.ChainConfig)
	}
	storedOccupied, err := repo.GetByID(ctx, occupied.ID)
	if err != nil || storedOccupied == nil || storedOccupied.Title != occupied.Title {
		t.Fatalf("occupied task was modified: task=%#v err=%v", storedOccupied, err)
	}
	storedFallback, err := repo.GetByID(ctx, occupiedFallback.ID)
	if err != nil || storedFallback == nil || storedFallback.Title != occupiedFallback.Title {
		t.Fatalf("occupied fallback task was modified: task=%#v err=%v", storedFallback, err)
	}
}

func TestSwarmServiceFollowupPlannerRetryReconcilesPartiallyCreatedWorkers(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, execRepo, nil)

	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Resume partial follow-up workers", Prompt: "Build export", MaxWorkers: 3})
	if err != nil {
		t.Fatalf("CreateSwarmTask: %v", err)
	}
	planner, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: planner=%#v err=%v", planner, err)
	}
	initialOutput := PlannerOutput{Workers: []PlannerWorker{{Title: "Existing worker", Prompt: "Build existing part", WorkerKind: "backend", Ownership: []string{"internal/existing"}, Isolation: "worktree", Required: true}}}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, initialOutput); err != nil {
		t.Fatalf("apply initial plan: %v", err)
	}
	if err := svc.HandleParentFollowup(ctx, parent.ID, "Add two follow-up components"); err != nil {
		t.Fatalf("HandleParentFollowup: %v", err)
	}
	planner, err = repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("coordinating planner missing: planner=%#v err=%v", planner, err)
	}

	followupJSON := `{"workers":[{"title":"New worker one","prompt":"Build first new part","worker_kind":"backend","ownership":["internal/newone"],"isolation":"worktree","required":true},{"title":"New worker two","prompt":"Build second new part","worker_kind":"backend","ownership":["internal/newtwo"],"isolation":"worktree","required":true}]}`
	exec := &models.Execution{TaskID: planner.ID, Status: models.ExecRunning, PromptSent: requireFullSwarmTestTask(t, repo, planner.ID).Prompt}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create follow-up planner execution: %v", err)
	}
	if err := execRepo.Complete(ctx, exec.ID, models.ExecCompleted, followupJSON, "", 0, 1); err != nil {
		t.Fatalf("complete follow-up planner execution: %v", err)
	}
	if err := repo.UpdateStatus(ctx, planner.ID, models.StatusCompleted); err != nil {
		t.Fatalf("complete planner task: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER fail_second_followup_worker BEFORE INSERT ON tasks WHEN NEW.title = 'New worker two' BEGIN SELECT RAISE(ABORT, 'forced second worker failure'); END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	if err := svc.applyCompletedPlannerExecution(ctx, planner); err == nil || !strings.Contains(err.Error(), "forced second worker failure") {
		t.Fatalf("first application error = %v, want forced second worker failure", err)
	}
	children, err := repo.ListSwarmChildren(ctx, parent.ID)
	if err != nil {
		t.Fatalf("list partially created children: %v", err)
	}
	if got := countSwarmWorkers(children); got != 2 {
		t.Fatalf("workers after partial failure = %d, want existing plus first new worker", got)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER fail_second_followup_worker`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}

	failedPlanner, err := repo.GetByID(ctx, planner.ID)
	if err != nil || failedPlanner == nil {
		t.Fatalf("load failed planner: planner=%#v err=%v", failedPlanner, err)
	}
	if err := svc.applyCompletedPlannerExecution(ctx, failedPlanner); err != nil {
		t.Fatalf("retry exact completed follow-up output: %v", err)
	}
	children, err = repo.ListSwarmChildren(ctx, parent.ID)
	if err != nil {
		t.Fatalf("list recovered children: %v", err)
	}
	if got := countSwarmWorkers(children); got != 3 {
		t.Fatalf("workers after retry = %d, want exactly one existing and two planned new workers", got)
	}
	titleCounts := map[string]int{}
	for _, child := range children {
		if child.SwarmRole == models.SwarmRoleWorker {
			titleCounts[child.Title]++
		}
	}
	if titleCounts["New worker one"] != 1 || titleCounts["New worker two"] != 1 {
		t.Fatalf("follow-up workers were not reconciled exactly once: %#v", titleCounts)
	}
}

func countSwarmWorkers(children []models.Task) int {
	count := 0
	for _, child := range children {
		if child.SwarmRole == models.SwarmRoleWorker {
			count++
		}
	}
	return count
}

func TestSwarmServiceCreateAssignsProjectDefaultModelToChildren(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskSvc := NewTaskService(taskRepo, nil, nil)
	svc := NewSwarmService(taskSvc, taskRepo, nil, nil)
	svc.SetModelSelectionRepos(llmConfigRepo, projectRepo)

	globalAgent := &models.LLMConfig{Name: "Global Default", Provider: models.ProviderTest, Model: "global-default", MaxTokens: 4096, IsDefault: true}
	if err := llmConfigRepo.Create(ctx, globalAgent); err != nil {
		t.Fatalf("create global model: %v", err)
	}
	projectAgent := &models.LLMConfig{Name: "Project Swarm Model", Provider: models.ProviderTest, Model: "project-swarm", MaxTokens: 4096, IsDefault: false}
	if err := llmConfigRepo.Create(ctx, projectAgent); err != nil {
		t.Fatalf("create project model: %v", err)
	}
	project := &models.Project{Name: "Swarm Model Project", DefaultAgentConfigID: &projectAgent.ID}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: project.ID, Title: "Build export", Prompt: "Build export", MaxWorkers: 1, WorkerIsolation: "worktree", ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatalf("CreateSwarmTask: %v", err)
	}
	if parent.AgentID == nil || *parent.AgentID != projectAgent.ID {
		t.Fatalf("parent agent id = %v, want project default %s", parent.AgentID, projectAgent.ID)
	}
	planner, err := taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: planner=%#v err=%v", planner, err)
	}
	if planner.AgentID == nil || *planner.AgentID != projectAgent.ID {
		t.Fatalf("planner agent id = %v, want project default %s", planner.AgentID, projectAgent.ID)
	}

	output := PlannerOutput{Workers: []PlannerWorker{{Title: "Worker", Prompt: "Do work", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", Required: true}}, ReviewerPrompt: "Review", MergerPrompt: "Merge"}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, output); err != nil {
		t.Fatalf("ApplyPlannerOutput: %v", err)
	}
	children, err := taskRepo.ListSwarmChildren(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSwarmChildren: %v", err)
	}
	for _, child := range children {
		if child.AgentID == nil || *child.AgentID != projectAgent.ID {
			t.Fatalf("child %s role=%s agent id = %v, want project default %s", child.ID, child.SwarmRole, child.AgentID, projectAgent.ID)
		}
	}
	if parent.AgentID != nil && *parent.AgentID == globalAgent.ID {
		t.Fatalf("parent used global default %s instead of project default %s", globalAgent.ID, projectAgent.ID)
	}
}

func TestSwarmServiceApplyPlannerOutputAllowsOverlappingWorktreeScopes(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, nil, nil)

	parent, err := svc.CreateSwarmTask(context.Background(), CreateSwarmTaskRequest{ProjectID: "default", Title: "Fix email switching", Prompt: "Fix email switching", MaxWorkers: 4, WorkerIsolation: "worktree", ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatalf("CreateSwarmTask: %v", err)
	}
	planner, err := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: planner=%#v err=%v", planner, err)
	}

	output := PlannerOutput{
		Workers: []PlannerWorker{
			{Title: "Email runtime fixer", Prompt: "Fix email runtime switch_project", WorkerKind: "backend", Ownership: []string{"internal/service/email_service.go"}, Isolation: "worktree", WriteScope: []string{"internal/service"}, ReadScope: []string{"."}, Required: true},
			{Title: "Cross-channel comparison", Prompt: "Compare channel switch_project behavior", WorkerKind: "backend", Ownership: []string{"internal/service/chat_action_runtime.go"}, Isolation: "worktree", WriteScope: []string{"internal/service"}, ReadScope: []string{"."}, Required: true},
		},
		ReviewerPrompt: "Review overlapping service changes and conflicts",
		MergerPrompt:   "Integrate accepted service changes",
	}
	if err := svc.ApplyPlannerOutput(context.Background(), planner.ID, output); err != nil {
		t.Fatalf("ApplyPlannerOutput should allow overlapping isolated worktree scopes: %v", err)
	}
	children, err := repo.ListSwarmChildren(context.Background(), parent.ID)
	if err != nil {
		t.Fatalf("ListSwarmChildren: %v", err)
	}
	workers := 0
	for _, child := range children {
		if child.SwarmRole == models.SwarmRoleWorker {
			workers++
		}
	}
	if workers != 2 {
		t.Fatalf("expected 2 workers from overlapping scopes, got %d children=%#v", workers, children)
	}
}

func TestSwarmServiceCreateSwarmTaskStartsPlannerForActiveCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, nil, nil)
	startImmediately := false

	parent, err := svc.CreateSwarmTask(context.Background(), CreateSwarmTaskRequest{ProjectID: "default", Title: "Active swarm", Prompt: "Build export", Category: models.CategoryActive, MaxWorkers: 3, WorkerIsolation: "worktree", ReviewerEnabled: true, MergerEnabled: true, StartImmediately: &startImmediately})
	if err != nil {
		t.Fatalf("CreateSwarmTask: %v", err)
	}
	planner, err := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("active swarm should create planner regardless of start_immediately flag: planner=%#v err=%v", planner, err)
	}
	if planner.Status != models.StatusPending || planner.Category != models.CategoryActive {
		t.Fatalf("planner not runnable after active swarm creation: category=%s status=%s", planner.Category, planner.Status)
	}
}

func TestSwarmServiceCreateSwarmTaskDefersPlannerForBacklogCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, nil, nil)
	startImmediately := true

	parent, err := svc.CreateSwarmTask(context.Background(), CreateSwarmTaskRequest{ProjectID: "default", Title: "Backlog swarm", Prompt: "Build export", Category: models.CategoryBacklog, MaxWorkers: 3, WorkerIsolation: "worktree", ReviewerEnabled: true, MergerEnabled: true, StartImmediately: &startImmediately})
	if err != nil {
		t.Fatalf("CreateSwarmTask: %v", err)
	}
	if parent.Category != models.CategoryBacklog {
		t.Fatalf("parent category=%s, want backlog", parent.Category)
	}
	if planner, err := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRolePlanner); err != nil {
		t.Fatalf("FindSwarmChildByRole: %v", err)
	} else if planner != nil {
		t.Fatalf("backlog swarm must not start planner even when start_immediately is true, got %#v", planner)
	}

	if err := svc.StartPlanner(context.Background(), parent.ID); err != nil {
		t.Fatalf("StartPlanner: %v", err)
	}
	planner, err := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner not created on explicit start: planner=%#v err=%v", planner, err)
	}
	storedParent, err := repo.GetByID(context.Background(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedParent.Category != models.CategoryActive {
		t.Fatalf("parent not activated by explicit start: category=%s", storedParent.Category)
	}
	if planner.Status != models.StatusPending || planner.Category != models.CategoryActive {
		t.Fatalf("planner not runnable after explicit start: category=%s status=%s", planner.Category, planner.Status)
	}
}

func TestSwarmServiceAppliesPlannerOutputOnPlannerCompletion(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, execRepo, nil)
	parent, err := svc.CreateSwarmTask(context.Background(), CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export", Prompt: "Build export", MaxWorkers: 2, ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	planner, err := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: %v", err)
	}
	exec := &models.Execution{TaskID: planner.ID, Status: models.ExecRunning, PromptSent: requireFullSwarmTestTask(t, repo, planner.ID).Prompt}
	if err := execRepo.Create(context.Background(), exec); err != nil {
		t.Fatalf("create planner execution: %v", err)
	}
	plannerJSON := `{"workers":[{"title":"Backend worker","prompt":"Do backend","worker_kind":"backend","ownership":["internal/service"],"isolation":"worktree","write_scope":["internal/service"],"required":true}],"reviewer_prompt":"Review workers","merger_prompt":"Integrate workers"}`
	if err := execRepo.Complete(context.Background(), exec.ID, models.ExecCompleted, plannerJSON, "", 0, 1); err != nil {
		t.Fatalf("complete planner execution: %v", err)
	}
	if err := repo.UpdateStatus(context.Background(), planner.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := svc.OnChildCompleted(context.Background(), planner.ID); err != nil {
		t.Fatalf("OnChildCompleted planner: %v", err)
	}
	children, err := repo.ListSwarmChildren(context.Background(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[models.SwarmRole]int{}
	for _, child := range children {
		counts[child.SwarmRole]++
	}
	if counts[models.SwarmRoleWorker] != 1 || counts[models.SwarmRoleReviewer] != 1 || counts[models.SwarmRoleMerger] != 1 {
		t.Fatalf("planner completion did not create swarm children: %#v", counts)
	}
	if err := svc.OnChildCompleted(context.Background(), planner.ID); err != nil {
		t.Fatalf("duplicate OnChildCompleted planner: %v", err)
	}
	children, err = repo.ListSwarmChildren(context.Background(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	counts = map[models.SwarmRole]int{}
	for _, child := range children {
		counts[child.SwarmRole]++
	}
	if counts[models.SwarmRoleWorker] != 1 || counts[models.SwarmRoleReviewer] != 1 || counts[models.SwarmRoleMerger] != 1 {
		t.Fatalf("duplicate planner completion created extra children: %#v", counts)
	}
}

func TestSwarmServiceTerminalizesChildSwarmStatusOnCompletion(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, execRepo, nil)
	ctx := context.Background()

	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export", Prompt: "Build export", MaxWorkers: 1, ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	planner, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: %v", err)
	}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, PlannerOutput{Workers: []PlannerWorker{{Title: "Backend worker", Prompt: "Do backend", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", WriteScope: []string{"internal/service"}, Required: true}}, ReviewerPrompt: "Review", MergerPrompt: "Integrate"}); err != nil {
		t.Fatal(err)
	}
	planner, err = repo.GetByID(ctx, planner.ID)
	if err != nil || planner == nil {
		t.Fatalf("reload planner: %v", err)
	}
	if planner.Status != models.StatusCompleted || planner.SwarmStatus != "planned" {
		t.Fatalf("planner status not terminalized: status=%s swarm_status=%s", planner.Status, planner.SwarmStatus)
	}

	worker, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	if err != nil || worker == nil {
		t.Fatalf("worker missing: %v", err)
	}
	if err := repo.UpdateStatus(ctx, worker.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := svc.OnChildCompleted(ctx, worker.ID); err != nil {
		t.Fatal(err)
	}
	worker, _ = repo.GetByID(ctx, worker.ID)
	if worker.SwarmStatus != "completed" {
		t.Fatalf("worker swarm_status not terminalized: %s", worker.SwarmStatus)
	}

	reviewer, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleReviewer)
	if err != nil || reviewer == nil {
		t.Fatalf("reviewer missing: %v", err)
	}
	if reviewer.Status != models.StatusPending || reviewer.SwarmStatus != "ready" {
		t.Fatalf("reviewer not started after worker completion: status=%s swarm_status=%s", reviewer.Status, reviewer.SwarmStatus)
	}
	if err := repo.UpdateStatus(ctx, reviewer.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := svc.OnChildCompleted(ctx, reviewer.ID); err != nil {
		t.Fatal(err)
	}
	reviewer, _ = repo.GetByID(ctx, reviewer.ID)
	if reviewer.SwarmStatus != "reviewed" {
		t.Fatalf("reviewer swarm_status not terminalized: %s", reviewer.SwarmStatus)
	}

	merger, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleMerger)
	if err != nil || merger == nil {
		t.Fatalf("merger missing: %v", err)
	}
	if merger.Status != models.StatusPending || merger.SwarmStatus != "ready" {
		t.Fatalf("merger not started after reviewer completion: status=%s swarm_status=%s", merger.Status, merger.SwarmStatus)
	}
	exec := &models.Execution{TaskID: merger.ID, Status: models.ExecRunning, PromptSent: requireFullSwarmTestTask(t, repo, merger.ID).Prompt}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatal(err)
	}
	if err := execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "Final integrated output", "", 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, merger.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := svc.OnChildCompleted(ctx, merger.ID); err != nil {
		t.Fatal(err)
	}
	merger, _ = repo.GetByID(ctx, merger.ID)
	if merger.SwarmStatus != "integrated" {
		t.Fatalf("merger swarm_status not terminalized: %s", merger.SwarmStatus)
	}
}

func TestSwarmServiceCompletesReviewerOnlySwarmWithoutMerger(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, execRepo, nil)
	ctx := context.Background()

	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export", Prompt: "Build export", MaxWorkers: 1, ReviewerEnabled: true, MergerEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	planner, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: %v", err)
	}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, PlannerOutput{Workers: []PlannerWorker{{Title: "Backend worker", Prompt: "Do backend", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", WriteScope: []string{"internal/service"}, Required: true}}, ReviewerPrompt: "Review"}); err != nil {
		t.Fatal(err)
	}
	if merger, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleMerger); err != nil || merger != nil {
		t.Fatalf("merger should not exist when disabled, merger=%#v err=%v", merger, err)
	}
	worker, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	if err != nil || worker == nil {
		t.Fatalf("worker missing: %v", err)
	}
	if err := repo.UpdateStatus(ctx, worker.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := svc.OnChildCompleted(ctx, worker.ID); err != nil {
		t.Fatal(err)
	}
	reviewer, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleReviewer)
	if err != nil || reviewer == nil {
		t.Fatalf("reviewer missing: %v", err)
	}
	if reviewer.Status != models.StatusPending {
		t.Fatalf("reviewer not started after worker completion: %#v", reviewer)
	}
	if err := repo.UpdateStatus(ctx, reviewer.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := svc.OnChildCompleted(ctx, reviewer.ID); err != nil {
		t.Fatal(err)
	}

	parent, err = repo.GetByID(ctx, parent.ID)
	if err != nil || parent == nil {
		t.Fatalf("get parent: %v", err)
	}
	if parent.Status != models.StatusCompleted || parent.Category != models.CategoryCompleted || parent.SwarmStatus != "current" {
		t.Fatalf("parent not completed without merger: status=%s category=%s swarm_status=%s", parent.Status, parent.Category, parent.SwarmStatus)
	}
	parentCfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	if parentCfg.MergedGeneration != parentCfg.Generation || parentCfg.Generation != 1 {
		t.Fatalf("parent freshness not marked complete without merger: %#v", parentCfg)
	}
	parentExecs, err := execRepo.ListByTask(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(parentExecs) != 0 {
		t.Fatalf("merger-disabled swarm should not fabricate parent execution, got %#v", parentExecs)
	}
}

func TestSwarmServiceCompletesWorkerOnlySwarmWithoutReviewerOrMerger(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, nil, nil)
	ctx := context.Background()

	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export", Prompt: "Build export", MaxWorkers: 1, ReviewerEnabled: false, MergerEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	planner, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: %v", err)
	}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, PlannerOutput{Workers: []PlannerWorker{{Title: "Backend worker", Prompt: "Do backend", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", WriteScope: []string{"internal/service"}, Required: true}}}); err != nil {
		t.Fatal(err)
	}
	if reviewer, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleReviewer); err != nil || reviewer != nil {
		t.Fatalf("reviewer should not exist when disabled, reviewer=%#v err=%v", reviewer, err)
	}
	if merger, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleMerger); err != nil || merger != nil {
		t.Fatalf("merger should not exist when disabled, merger=%#v err=%v", merger, err)
	}
	worker, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	if err != nil || worker == nil {
		t.Fatalf("worker missing: %v", err)
	}
	if err := repo.UpdateStatus(ctx, worker.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := svc.OnChildCompleted(ctx, worker.ID); err != nil {
		t.Fatal(err)
	}

	parent, err = repo.GetByID(ctx, parent.ID)
	if err != nil || parent == nil {
		t.Fatalf("get parent: %v", err)
	}
	if parent.Status != models.StatusCompleted || parent.Category != models.CategoryCompleted || parent.SwarmStatus != "current" {
		t.Fatalf("worker-only parent not completed: status=%s category=%s swarm_status=%s", parent.Status, parent.Category, parent.SwarmStatus)
	}
	parentCfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	if parentCfg.MergedGeneration != parentCfg.Generation || parentCfg.Generation != 1 {
		t.Fatalf("worker-only parent freshness not marked complete: %#v", parentCfg)
	}
}

func TestSwarmServiceStartsMergerAfterWorkersWhenReviewerDisabled(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	workerSvc := newTestWorkerService(t)
	svc := NewSwarmService(taskSvc, repo, nil, workerSvc)
	ctx := context.Background()

	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export", Prompt: "Build export", MaxWorkers: 1, ReviewerEnabled: false, MergerEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	planner, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: %v", err)
	}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, PlannerOutput{Workers: []PlannerWorker{{Title: "Backend worker", Prompt: "Do backend", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", WriteScope: []string{"internal/service"}, Required: true}}, MergerPrompt: "Integrate"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-workerSvc.Submitted():
	default:
		t.Fatal("expected initial worker submission")
	}
	if reviewer, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleReviewer); err != nil || reviewer != nil {
		t.Fatalf("reviewer should not exist when disabled, reviewer=%#v err=%v", reviewer, err)
	}
	worker, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	if err != nil || worker == nil {
		t.Fatalf("worker missing: %v", err)
	}
	if err := repo.UpdateStatus(ctx, worker.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}

	if err := svc.OnChildCompleted(ctx, worker.ID); err != nil {
		t.Fatal(err)
	}

	merger, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleMerger)
	if err != nil || merger == nil {
		t.Fatalf("merger missing: %v", err)
	}
	if merger.Status != models.StatusPending || merger.Category != models.CategoryActive || merger.SwarmStatus != "ready" {
		t.Fatalf("merger not started after worker completion without reviewer: status=%s category=%s swarm_status=%s", merger.Status, merger.Category, merger.SwarmStatus)
	}
	intCfg, _ := models.ParseSwarmConfig(merger.SwarmConfig)
	if intCfg.RerunGeneration != 1 {
		t.Fatalf("merger target generation=%d, want 1", intCfg.RerunGeneration)
	}
	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != merger.ID {
			t.Fatalf("submitted task ID=%s, want merger %s", submitted.ID, merger.ID)
		}
		if submitted.Status != models.StatusPending || submitted.Category != models.CategoryActive {
			t.Fatalf("submitted merger not runnable: status=%s category=%s", submitted.Status, submitted.Category)
		}
	default:
		t.Fatal("expected merger to be submitted")
	}
}

func TestPlannerPromptRoleBoundsPlannerToDelegationOnly(t *testing.T) {
	prompt := plannerPrompt("Fix the bug", 3)

	required := []string{
		"Your only job is to decompose the goal into worker tasks and handoff instructions.",
		"You are not a worker, reviewer, or merger.",
		"Do not implement the requested feature or bug fix yourself.",
		"Do not modify, create, delete, format, or regenerate files.",
		"Do not run build, test, formatter, generator, git, or shell commands.",
		"Every entry in workers runs immediately and in parallel with every other worker.",
		"There is no dependency scheduling between workers",
		"natural-language phrasing like \"after the other workers complete\" or \"once implementation is done\" inside a worker's title/prompt will NOT be enforced.",
		"Never create a worker whose job is to validate, test, or review the output of other workers in this same plan.",
		"That work belongs in reviewer_prompt",
		"Only put independent, immediately-runnable implementation objectives in workers.",
		"Return exactly one raw JSON object and nothing else.",
		"Do not wrap the JSON in Markdown fences.",
		"\"workers\"",
		"\"reviewer_prompt\"",
		"\"merger_prompt\"",
		"\"notes\"",
	}
	for _, want := range required {
		if !strings.Contains(prompt, want) {
			t.Fatalf("planner prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestCoordinatorFollowupPromptRoleBoundsPlannerToDelegationOnly(t *testing.T) {
	prompt := coordinatorFollowupPrompt("Parent goal", "Follow-up", 2)

	required := []string{
		"Your only job is to decide which workers need new delegated work.",
		"You are not a worker, reviewer, or merger.",
		"Do not implement the follow-up yourself.",
		"Do not modify, create, delete, format, or regenerate files.",
		"Do not run build, test, formatter, generator, git, or shell commands.",
		"Every entry in workers runs immediately and in parallel with every other worker, including carried-forward existing workers.",
		"There is no dependency scheduling between workers",
		"natural-language phrasing like \"after the other workers complete\" inside a worker's title/prompt will NOT be enforced.",
		"Never create or update a worker whose job is to validate, test, or review the output of other workers in this plan.",
		"That work belongs in reviewer_prompt",
		"Return exactly one raw JSON object and nothing else.",
		"Do not wrap the JSON in Markdown fences.",
		"For existing affected workers, include their existing task_id.",
		"\"workers\"",
		"\"reviewer_prompt\"",
		"\"merger_prompt\"",
		"\"notes\"",
	}
	for _, want := range required {
		if !strings.Contains(prompt, want) {
			t.Fatalf("coordinator prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestParsePlannerOutputJSONExtractsPlannerObjectFromTranscript(t *testing.T) {
	raw := `I’ll produce the swarm task JSON directly and first load context.
[Using tool: memory_view]
{"handle":"chat_thread_system.md","body":"not the planner"}

Here is the bounded plan:
{"workers":[{"title":"Email runtime fixer","prompt":"Fix email runtime switch project","worker_kind":"backend","ownership":["internal/service"],"isolation":"worktree","write_scope":["internal/service"],"read_scope":["."],"required":true}],"reviewer_prompt":"Review email runtime fix","merger_prompt":"Integrate email runtime fix","notes":"One worker is enough."}`

	out, err := ParsePlannerOutputJSON(raw)
	if err != nil {
		t.Fatalf("ParsePlannerOutputJSON: %v", err)
	}
	if len(out.Workers) != 1 || out.Workers[0].Title != "Email runtime fixer" {
		t.Fatalf("parsed wrong planner workers: %#v", out.Workers)
	}
	if out.ReviewerPrompt == "" || out.MergerPrompt == "" {
		t.Fatalf("expected reviewer and merger prompts: %#v", out)
	}
}

func TestParsePlannerOutputJSONExtractsFencedPlannerObject(t *testing.T) {
	raw := "Planner output:\n```json\n{" + `"workers":[{"title":"Backend worker","prompt":"Do backend","worker_kind":"backend","ownership":["internal/service"],"isolation":"worktree","write_scope":["internal/service"],"required":true}],"reviewer_prompt":"Review","merger_prompt":"Integrate"` + "}\n```"

	out, err := ParsePlannerOutputJSON(raw)
	if err != nil {
		t.Fatalf("ParsePlannerOutputJSON: %v", err)
	}
	if len(out.Workers) != 1 || out.Workers[0].Title != "Backend worker" {
		t.Fatalf("parsed wrong planner output: %#v", out)
	}
}

func TestParsePlannerOutputJSONPrefersFinalPlannerObject(t *testing.T) {
	raw := "Earlier transcript contained a stale candidate:\n" +
		`{"workers":[{"title":"Stale worker","prompt":"Do the old plan","worker_kind":"backend","ownership":["old"],"isolation":"worktree","write_scope":["old"],"required":true}],"reviewer_prompt":"Review stale","merger_prompt":"Integrate stale"}` +
		"\n\nAfter considering the follow-up, use this final planner JSON:\n```json\n" +
		`{"workers":[{"title":"Final worker","prompt":"Do the final plan","worker_kind":"backend","ownership":["internal/service"],"isolation":"worktree","write_scope":["internal/service"],"required":true}],"reviewer_prompt":"Review final","merger_prompt":"Integrate final"}` +
		"\n```"

	out, err := ParsePlannerOutputJSON(raw)
	if err != nil {
		t.Fatalf("ParsePlannerOutputJSON: %v", err)
	}
	if len(out.Workers) != 1 || out.Workers[0].Title != "Final worker" {
		t.Fatalf("parsed stale planner output: %#v", out.Workers)
	}
	if out.ReviewerPrompt != "Review final" || out.MergerPrompt != "Integrate final" {
		t.Fatalf("parsed stale prompts: %#v", out)
	}
}

func TestSwarmServiceStartPlannerReactivatesExistingPlannerBeforeSubmit(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	workerSvc := newTestWorkerService(t)
	svc := NewSwarmService(taskSvc, repo, nil, nil)
	parent, err := svc.CreateSwarmTask(context.Background(), CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export", Prompt: "Build export", MaxWorkers: 2, ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	planner, err := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: %v", err)
	}
	if err := repo.UpdateCategory(context.Background(), planner.ID, models.CategoryBacklog); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(context.Background(), planner.ID, models.StatusFailed); err != nil {
		t.Fatal(err)
	}
	svc.workerSvc = workerSvc

	if err := svc.StartPlanner(context.Background(), parent.ID); err != nil {
		t.Fatalf("StartPlanner: %v", err)
	}

	select {
	case submitted := <-workerSvc.Submitted():
		if submitted.ID != planner.ID {
			t.Fatalf("submitted task ID=%s, want planner %s", submitted.ID, planner.ID)
		}
		if submitted.Category != models.CategoryActive || submitted.Status != models.StatusPending {
			t.Fatalf("submitted planner not runnable: category=%s status=%s", submitted.Category, submitted.Status)
		}
		if submitted.StartsNewContext {
			t.Fatal("manual planner start must not add a scheduled context boundary")
		}
	default:
		t.Fatal("expected planner to be submitted")
	}
	planner, err = repo.GetByID(context.Background(), planner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if planner.Category != models.CategoryActive || planner.Status != models.StatusPending {
		t.Fatalf("persisted planner not runnable: category=%s status=%s", planner.Category, planner.Status)
	}
}

func TestSwarmServiceInvalidPlannerExecutionBlocksParent(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, execRepo, nil)
	parent, err := svc.CreateSwarmTask(context.Background(), CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export", Prompt: "Build export", MaxWorkers: 2, ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	planner, err := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: %v", err)
	}
	exec := &models.Execution{TaskID: planner.ID, Status: models.ExecRunning, PromptSent: requireFullSwarmTestTask(t, repo, planner.ID).Prompt}
	if err := execRepo.Create(context.Background(), exec); err != nil {
		t.Fatal(err)
	}
	if err := execRepo.Complete(context.Background(), exec.ID, models.ExecCompleted, "not json", "", 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(context.Background(), planner.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := svc.OnChildCompleted(context.Background(), planner.ID); err == nil {
		t.Fatal("expected invalid planner JSON error")
	}
	planner, _ = repo.GetByID(context.Background(), planner.ID)
	if planner.Status != models.StatusFailed || planner.SwarmStatus != "invalid_plan" {
		t.Fatalf("planner not marked invalid: status=%s swarm_status=%s", planner.Status, planner.SwarmStatus)
	}
	parent, _ = repo.GetByID(context.Background(), parent.ID)
	if parent.Status != models.StatusBlocked || parent.SwarmStatus != "blocked" {
		t.Fatalf("parent not blocked: status=%s swarm_status=%s", parent.Status, parent.SwarmStatus)
	}
}

func TestSwarmServiceMergerCompletionPersistsParentResult(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, execRepo, nil)
	parent, err := svc.CreateSwarmTask(context.Background(), CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export", Prompt: "Build export", MaxWorkers: 1, ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	planner, _ := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRolePlanner)
	if planner == nil {
		t.Fatal("planner missing")
	}
	output := PlannerOutput{Workers: []PlannerWorker{{Title: "Worker", Prompt: "Do work", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", Required: true}}, ReviewerPrompt: "Review", MergerPrompt: "Integrate"}
	if err := svc.ApplyPlannerOutput(context.Background(), planner.ID, output); err != nil {
		t.Fatal(err)
	}
	children, err := repo.ListSwarmChildren(context.Background(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		cfg, _ := models.ParseSwarmConfig(child.SwarmConfig)
		switch child.SwarmRole {
		case models.SwarmRoleWorker:
			cfg.CompletedGeneration = 1
			child.SwarmConfig, _ = cfg.JSON()
			if err := repo.UpdateSwarmFields(context.Background(), child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateStatus(context.Background(), child.ID, models.StatusCompleted); err != nil {
				t.Fatal(err)
			}
		case models.SwarmRoleReviewer:
			cfg.ReviewedGeneration = 1
			child.SwarmConfig, _ = cfg.JSON()
			if err := repo.UpdateSwarmFields(context.Background(), child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateStatus(context.Background(), child.ID, models.StatusCompleted); err != nil {
				t.Fatal(err)
			}
		}
	}
	merger, _ := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRoleMerger)
	if merger == nil {
		t.Fatal("merger missing")
	}
	exec := &models.Execution{TaskID: merger.ID, Status: models.ExecRunning, PromptSent: requireFullSwarmTestTask(t, repo, merger.ID).Prompt}
	if err := execRepo.Create(context.Background(), exec); err != nil {
		t.Fatal(err)
	}
	if err := execRepo.UpdateDiffOutput(context.Background(), exec.ID, "diff --git a/final.go b/final.go"); err != nil {
		t.Fatal(err)
	}
	if err := execRepo.Complete(context.Background(), exec.ID, models.ExecCompleted, "Final integrated summary", "", 12, 34); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(context.Background(), merger.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := svc.OnChildCompleted(context.Background(), merger.ID); err != nil {
		t.Fatal(err)
	}
	updatedParent, err := repo.GetByID(context.Background(), parent.ID)
	if err != nil || updatedParent == nil {
		t.Fatalf("get parent: %v", err)
	}
	if updatedParent.Status != models.StatusCompleted || updatedParent.Category != models.CategoryCompleted || updatedParent.MergeStatus != models.MergeStatusPending {
		t.Fatalf("parent not finalized with pending merge: status=%s category=%s merge=%s", updatedParent.Status, updatedParent.Category, updatedParent.MergeStatus)
	}
	parentExecs, err := execRepo.ListByTask(context.Background(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(parentExecs) != 1 || parentExecs[0].Output != "Final integrated summary" || parentExecs[0].DiffOutput != "" {
		t.Fatalf("parent execution summary not stored through light list: %#v", parentExecs)
	}
	parentExec, err := execRepo.GetByID(context.Background(), parentExecs[0].ID)
	if err != nil || parentExec == nil {
		t.Fatalf("get parent execution: %v", err)
	}
	if parentExec.Output != "Final integrated summary" || parentExec.DiffOutput != "diff --git a/final.go b/final.go" {
		t.Fatalf("parent result execution mismatch: output=%q diff=%q", parentExec.Output, parentExec.DiffOutput)
	}
	if err := svc.OnChildCompleted(context.Background(), merger.ID); err != nil {
		t.Fatal(err)
	}
	parentExecs, err = execRepo.ListByTask(context.Background(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(parentExecs) != 1 {
		t.Fatalf("merger completion should be idempotent, got %d parent executions", len(parentExecs))
	}
}

func TestTaskServiceUpdateCategoryNotifiesSwarmChildCancellation(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, nil, nil)
	taskSvc.SetSwarmService(svc)
	ctx := context.Background()
	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Swarm parent", Prompt: "Build result", Category: models.CategoryActive, MaxWorkers: 1, WorkerIsolation: "worktree", ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	planner, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: %v", err)
	}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, PlannerOutput{
		Workers:        []PlannerWorker{{Title: "API worker", Prompt: "Update API", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", WriteScope: []string{"internal/service"}, Required: true}},
		ReviewerPrompt: "Review worker",
		MergerPrompt:   "Integrate worker",
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	if err != nil || worker == nil {
		t.Fatalf("worker missing: %v", err)
	}
	if err := repo.UpdateStatus(ctx, worker.ID, models.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateCategory(ctx, worker.ID, models.CategoryActive); err != nil {
		t.Fatal(err)
	}

	if err := taskSvc.UpdateCategory(ctx, worker.ID, models.CategoryCompleted); err != nil {
		t.Fatalf("UpdateCategory: %v", err)
	}

	updatedWorker, err := repo.GetByID(ctx, worker.ID)
	if err != nil || updatedWorker == nil {
		t.Fatalf("updated worker missing: %v", err)
	}
	if updatedWorker.Status != models.StatusCancelled || updatedWorker.Category != models.CategoryCompleted {
		t.Fatalf("worker status/category = %s/%s", updatedWorker.Status, updatedWorker.Category)
	}
	updatedParent, err := repo.GetByID(ctx, parent.ID)
	if err != nil || updatedParent == nil {
		t.Fatalf("updated parent missing: %v", err)
	}
	if updatedParent.Status != models.StatusBlocked || updatedParent.SwarmStatus != "needs_coordination" {
		t.Fatalf("parent status/swarm_status = %s/%s", updatedParent.Status, updatedParent.SwarmStatus)
	}
}

func TestTaskServiceUpdateCategoryNotifiesPendingSwarmChildCancellation(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, nil, nil)
	taskSvc.SetSwarmService(svc)
	ctx := context.Background()
	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Swarm parent", Prompt: "Build result", Category: models.CategoryActive, MaxWorkers: 1, WorkerIsolation: "worktree", ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	planner, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil || planner == nil {
		t.Fatalf("planner missing: %v", err)
	}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, PlannerOutput{
		Workers:        []PlannerWorker{{Title: "API worker", Prompt: "Update API", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", WriteScope: []string{"internal/service"}, Required: true}},
		ReviewerPrompt: "Review worker",
		MergerPrompt:   "Integrate worker",
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	if err != nil || worker == nil {
		t.Fatalf("worker missing: %v", err)
	}
	if err := repo.UpdateStatus(ctx, worker.ID, models.StatusPending); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateCategory(ctx, worker.ID, models.CategoryActive); err != nil {
		t.Fatal(err)
	}

	if err := taskSvc.UpdateCategory(ctx, worker.ID, models.CategoryCompleted); err != nil {
		t.Fatalf("UpdateCategory: %v", err)
	}

	updatedWorker, err := repo.GetByID(ctx, worker.ID)
	if err != nil || updatedWorker == nil {
		t.Fatalf("updated worker missing: %v", err)
	}
	if updatedWorker.Status != models.StatusCancelled || updatedWorker.Category != models.CategoryCompleted {
		t.Fatalf("worker status/category = %s/%s", updatedWorker.Status, updatedWorker.Category)
	}
	updatedParent, err := repo.GetByID(ctx, parent.ID)
	if err != nil || updatedParent == nil {
		t.Fatalf("updated parent missing: %v", err)
	}
	if updatedParent.Status != models.StatusBlocked || updatedParent.SwarmStatus != "needs_coordination" {
		t.Fatalf("parent status/swarm_status = %s/%s", updatedParent.Status, updatedParent.SwarmStatus)
	}
}

func TestSwarmServiceChildCancellationSetsRoleSpecificParentState(t *testing.T) {
	roles := []struct {
		name       string
		role       models.SwarmRole
		wantStatus string
	}{
		{name: "worker", role: models.SwarmRoleWorker, wantStatus: "needs_coordination"},
		{name: "reviewer", role: models.SwarmRoleReviewer, wantStatus: "needs_review"},
		{name: "merger", role: models.SwarmRoleMerger, wantStatus: "needs_integration"},
	}
	for _, tc := range roles {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			repo := repository.NewTaskRepo(db, nil)
			taskSvc := NewTaskService(repo, nil, nil)
			svc := NewSwarmService(taskSvc, repo, nil, nil)
			parent, err := svc.CreateSwarmTask(context.Background(), CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export", Prompt: "Build export", MaxWorkers: 1, ReviewerEnabled: true, MergerEnabled: true})
			if err != nil {
				t.Fatal(err)
			}
			planner, _ := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRolePlanner)
			if planner == nil {
				t.Fatal("planner missing")
			}
			output := PlannerOutput{Workers: []PlannerWorker{{Title: "Worker", Prompt: "Do work", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", Required: true}}, ReviewerPrompt: "Review", MergerPrompt: "Integrate"}
			if err := svc.ApplyPlannerOutput(context.Background(), planner.ID, output); err != nil {
				t.Fatal(err)
			}
			child, _ := repo.FindSwarmChildByRole(context.Background(), parent.ID, tc.role)
			if child == nil {
				t.Fatalf("%s child missing", tc.role)
			}
			if err := repo.UpdateStatus(context.Background(), child.ID, models.StatusCancelled); err != nil {
				t.Fatal(err)
			}
			if err := svc.OnChildCompleted(context.Background(), child.ID); err != nil {
				t.Fatal(err)
			}
			updatedParent, err := repo.GetByID(context.Background(), parent.ID)
			if err != nil || updatedParent == nil {
				t.Fatalf("get parent: %v", err)
			}
			if updatedParent.Status != models.StatusBlocked || updatedParent.SwarmStatus != tc.wantStatus {
				t.Fatalf("parent after %s cancel: status=%s swarm_status=%s", tc.role, updatedParent.Status, updatedParent.SwarmStatus)
			}
		})
	}
}

func TestSwarmServiceParentFollowupCoordinatesAffectedWorkers(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, nil, nil)
	parent, err := svc.CreateSwarmTask(context.Background(), CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export", Prompt: "Build export", MaxWorkers: 3, ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	planner, _ := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRolePlanner)
	if planner == nil {
		t.Fatal("planner missing")
	}
	initialOutput := PlannerOutput{Workers: []PlannerWorker{{Title: "Backend worker", Prompt: "Do backend", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", Required: true}, {Title: "Frontend worker", Prompt: "Do frontend", WorkerKind: "frontend", Ownership: []string{"web/templates"}, Isolation: "worktree", Required: true}}, ReviewerPrompt: "Review", MergerPrompt: "Integrate"}
	if err := svc.ApplyPlannerOutput(context.Background(), planner.ID, initialOutput); err != nil {
		t.Fatal(err)
	}
	children, _ := repo.ListSwarmChildren(context.Background(), parent.ID)
	var backend, frontend *models.Task
	for i := range children {
		child := children[i]
		cfg, _ := models.ParseSwarmConfig(child.SwarmConfig)
		switch child.SwarmRole {
		case models.SwarmRoleWorker:
			cfg.CompletedGeneration = 1
			child.SwarmConfig, _ = cfg.JSON()
			if err := repo.UpdateSwarmFields(context.Background(), child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateStatus(context.Background(), child.ID, models.StatusCompleted); err != nil {
				t.Fatal(err)
			}
			if cfg.WorkerKind == "backend" {
				backend = &child
			} else if cfg.WorkerKind == "frontend" {
				frontend = &child
			}
		case models.SwarmRoleReviewer:
			cfg.ReviewedGeneration = 1
			child.SwarmConfig, _ = cfg.JSON()
			if err := repo.UpdateSwarmFields(context.Background(), child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateStatus(context.Background(), child.ID, models.StatusCompleted); err != nil {
				t.Fatal(err)
			}
		case models.SwarmRoleMerger:
			cfg.MergedGeneration = 1
			child.SwarmConfig, _ = cfg.JSON()
			if err := repo.UpdateSwarmFields(context.Background(), child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateStatus(context.Background(), child.ID, models.StatusCompleted); err != nil {
				t.Fatal(err)
			}
		}
	}
	if backend == nil || frontend == nil {
		t.Fatalf("workers missing: backend=%#v frontend=%#v", backend, frontend)
	}
	parent, _ = repo.GetByID(context.Background(), parent.ID)
	parentCfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	parentCfg.MergedGeneration = 1
	parent.SwarmConfig, _ = parentCfg.JSON()
	if err := repo.UpdateSwarmFields(context.Background(), parent.ID, parent.SwarmRole, "current", parent.SwarmConfig, parent.SwarmSequence); err != nil {
		t.Fatal(err)
	}

	if err := svc.HandleParentFollowup(context.Background(), parent.ID, "Only update backend behavior"); err != nil {
		t.Fatal(err)
	}
	planner, _ = repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRolePlanner)
	if planner == nil || planner.Status != models.StatusPending {
		t.Fatalf("planner was not prepared for coordination follow-up: %#v", planner)
	}
	fullPlanner := requireFullSwarmTestTask(t, repo, planner.ID)
	if !strings.Contains(fullPlanner.Prompt, "Only update backend behavior") {
		t.Fatalf("planner prompt was not prepared for coordination follow-up: %q", fullPlanner.Prompt)
	}
	followupOutput := PlannerOutput{Workers: []PlannerWorker{{TaskID: backend.ID, Title: "Backend worker", Prompt: "Update backend only", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", Required: true}}, ReviewerPrompt: "Review backend update", MergerPrompt: "Integrate backend update"}
	if err := svc.ApplyPlannerOutput(context.Background(), planner.ID, followupOutput); err != nil {
		t.Fatal(err)
	}
	backendAfter, _ := repo.GetByID(context.Background(), backend.ID)
	frontendAfter, _ := repo.GetByID(context.Background(), frontend.ID)
	backendCfg, _ := models.ParseSwarmConfig(backendAfter.SwarmConfig)
	frontendCfg, _ := models.ParseSwarmConfig(frontendAfter.SwarmConfig)
	if backendAfter.Status != models.StatusPending || backendCfg.RerunGeneration != 2 || backendCfg.CompletedGeneration >= 2 {
		t.Fatalf("affected backend not queued for generation 2: status=%s cfg=%#v", backendAfter.Status, backendCfg)
	}
	if frontendAfter.Status != models.StatusCompleted || frontendCfg.CompletedGeneration != 2 {
		t.Fatalf("unaffected frontend not carried forward: status=%s cfg=%#v", frontendAfter.Status, frontendCfg)
	}
	reviewer, _ := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRoleReviewer)
	merger, _ := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRoleMerger)
	if reviewer == nil || reviewer.Status != models.StatusBlocked || merger == nil || merger.Status != models.StatusBlocked {
		t.Fatalf("reviewer/merger should wait for affected worker: reviewer=%#v merger=%#v", reviewer, merger)
	}
	backendCfg.CompletedGeneration = 2
	backendAfter.SwarmConfig, _ = backendCfg.JSON()
	if err := repo.UpdateSwarmFields(context.Background(), backendAfter.ID, backendAfter.SwarmRole, backendAfter.SwarmStatus, backendAfter.SwarmConfig, backendAfter.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(context.Background(), backendAfter.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := svc.OnChildCompleted(context.Background(), backendAfter.ID); err != nil {
		t.Fatal(err)
	}
	reviewer, _ = repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRoleReviewer)
	if reviewer == nil || reviewer.Status != models.StatusPending {
		t.Fatalf("reviewer not rerun after affected worker completed: %#v", reviewer)
	}
	revCfg, _ := models.ParseSwarmConfig(reviewer.SwarmConfig)
	revCfg.ReviewedGeneration = 2
	reviewer.SwarmConfig, _ = revCfg.JSON()
	if err := repo.UpdateSwarmFields(context.Background(), reviewer.ID, reviewer.SwarmRole, reviewer.SwarmStatus, reviewer.SwarmConfig, reviewer.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(context.Background(), reviewer.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := svc.OnChildCompleted(context.Background(), reviewer.ID); err != nil {
		t.Fatal(err)
	}
	merger, _ = repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRoleMerger)
	if merger == nil || merger.Status != models.StatusPending {
		t.Fatalf("merger not rerun after reviewer completed: %#v", merger)
	}
}

func TestSwarmServiceStartsReviewerAndMergerOnce(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, nil, nil)
	parent, err := svc.CreateSwarmTask(context.Background(), CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export", Prompt: "Build export", MaxWorkers: 2, ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	planner, _ := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRolePlanner)
	if planner == nil {
		t.Fatal("planner missing")
	}
	output := PlannerOutput{Workers: []PlannerWorker{{Title: "Worker A", Prompt: "A", WorkerKind: "backend", Ownership: []string{"a"}, Isolation: "worktree", Required: true}, {Title: "Worker B", Prompt: "B", WorkerKind: "frontend", Ownership: []string{"b"}, Isolation: "worktree", Required: true}}, ReviewerPrompt: "Review", MergerPrompt: "Integrate"}
	if err := svc.ApplyPlannerOutput(context.Background(), planner.ID, output); err != nil {
		t.Fatal(err)
	}
	children, _ := repo.ListSwarmChildren(context.Background(), parent.ID)
	workerID := ""
	reviewerID := ""
	for _, child := range children {
		if child.SwarmRole == models.SwarmRoleWorker {
			workerID = child.ID
			if err := repo.UpdateStatus(context.Background(), child.ID, models.StatusCompleted); err != nil {
				t.Fatal(err)
			}
			cfg, _ := models.ParseSwarmConfig(child.SwarmConfig)
			cfg.CompletedGeneration = 1
			b, _ := cfg.JSON()
			child.SwarmConfig = b
			if err := repo.UpdateSwarmFields(context.Background(), child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				t.Fatal(err)
			}
		}
		if child.SwarmRole == models.SwarmRoleReviewer {
			reviewerID = child.ID
			if err := repo.UpdateCategory(context.Background(), child.ID, models.CategoryCompleted); err != nil {
				t.Fatal(err)
			}
		}
	}
	if workerID == "" {
		t.Fatal("worker missing")
	}
	if reviewerID == "" {
		t.Fatal("reviewer missing")
	}
	if err := svc.OnChildCompleted(context.Background(), workerID); err != nil {
		t.Fatal(err)
	}
	if err := svc.OnChildCompleted(context.Background(), workerID); err != nil {
		t.Fatal(err)
	}
	reviewer, _ := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRoleReviewer)
	if reviewer == nil || reviewer.Status != models.StatusPending || reviewer.Category != models.CategoryActive {
		t.Fatalf("reviewer not pending and active once: %#v", reviewer)
	}
	if err := repo.UpdateStatus(context.Background(), reviewer.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	cfg, _ := models.ParseSwarmConfig(reviewer.SwarmConfig)
	cfg.ReviewedGeneration = 1
	b, _ := cfg.JSON()
	reviewer.SwarmConfig = b
	if err := repo.UpdateSwarmFields(context.Background(), reviewer.ID, reviewer.SwarmRole, reviewer.SwarmStatus, reviewer.SwarmConfig, reviewer.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	if err := svc.OnChildCompleted(context.Background(), reviewer.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.OnChildCompleted(context.Background(), reviewer.ID); err != nil {
		t.Fatal(err)
	}
	merger, _ := repo.FindSwarmChildByRole(context.Background(), parent.ID, models.SwarmRoleMerger)
	if merger == nil || merger.Status != models.StatusPending {
		t.Fatalf("merger not pending once: %#v", merger)
	}
}

func TestSwarmServiceRerunReviewerStartsMergerAfterReviewerCompletes(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, execRepo, nil)
	ctx := context.Background()

	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export", Prompt: "Build export", MaxWorkers: 1, ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	planner, _ := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if planner == nil {
		t.Fatal("planner missing")
	}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, PlannerOutput{Workers: []PlannerWorker{{Title: "Worker", Prompt: "Do work", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", Required: true}}, ReviewerPrompt: "Review", MergerPrompt: "Integrate"}); err != nil {
		t.Fatal(err)
	}
	children, err := repo.ListSwarmChildren(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		cfg, _ := models.ParseSwarmConfig(child.SwarmConfig)
		switch child.SwarmRole {
		case models.SwarmRoleWorker:
			cfg.CompletedGeneration = 1
			child.SwarmConfig, _ = cfg.JSON()
			if err := repo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateStatus(ctx, child.ID, models.StatusCompleted); err != nil {
				t.Fatal(err)
			}
		case models.SwarmRoleReviewer:
			cfg.ReviewedGeneration = 1
			child.SwarmConfig, _ = cfg.JSON()
			if err := repo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateStatus(ctx, child.ID, models.StatusCompleted); err != nil {
				t.Fatal(err)
			}
		case models.SwarmRoleMerger:
			cfg.MergedGeneration = 1
			child.SwarmConfig, _ = cfg.JSON()
			if err := repo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateStatus(ctx, child.ID, models.StatusCompleted); err != nil {
				t.Fatal(err)
			}
		}
	}
	parent, _ = repo.GetByID(ctx, parent.ID)
	parentCfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	parentCfg.Generation = 1
	parentCfg.MergedGeneration = 1
	parent.SwarmConfig, _ = parentCfg.JSON()
	if err := repo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, "current", parent.SwarmConfig, parent.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, parent.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateCategory(ctx, parent.ID, models.CategoryCompleted); err != nil {
		t.Fatal(err)
	}

	rerunReviewer, err := svc.RerunRole(ctx, parent.ID, models.SwarmRoleReviewer)
	if err != nil {
		t.Fatalf("RerunRole reviewer: %v", err)
	}
	if rerunReviewer == nil || rerunReviewer.Status != models.StatusPending || rerunReviewer.Category != models.CategoryActive {
		t.Fatalf("reviewer not queued for active rerun: %#v", rerunReviewer)
	}
	persistedReviewer, err := repo.GetByID(ctx, rerunReviewer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedReviewer.Category != models.CategoryActive {
		t.Fatalf("reviewer rerun category = %s, want active", persistedReviewer.Category)
	}
	parent, _ = repo.GetByID(ctx, parent.ID)
	parentCfg, _ = models.ParseSwarmConfig(parent.SwarmConfig)
	if parent.SwarmStatus != "needs_review" || parentCfg.MergedGeneration >= parentCfg.Generation {
		t.Fatalf("parent integration freshness not invalidated: status=%s cfg=%#v", parent.SwarmStatus, parentCfg)
	}
	if parent.Status != models.StatusRunning || parent.Category != models.CategoryActive {
		t.Fatalf("parent not reactivated for reviewer rerun: status=%s category=%s", parent.Status, parent.Category)
	}
	if err := repo.UpdateStatus(ctx, rerunReviewer.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := svc.OnChildCompleted(ctx, rerunReviewer.ID); err != nil {
		t.Fatal(err)
	}
	merger, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleMerger)
	if err != nil || merger == nil {
		t.Fatalf("merger missing: %v", err)
	}
	if merger.Status != models.StatusPending || merger.Category != models.CategoryActive {
		t.Fatalf("merger not rerun as active task after reviewer retry completed: %#v", merger)
	}
	if err := repo.UpdateStatus(ctx, parent.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateCategory(ctx, parent.ID, models.CategoryCompleted); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, merger.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	rerunMerger, err := svc.RerunRole(ctx, parent.ID, models.SwarmRoleMerger)
	if err != nil {
		t.Fatalf("RerunRole merger: %v", err)
	}
	if rerunMerger == nil || rerunMerger.Status != models.StatusPending || rerunMerger.Category != models.CategoryActive {
		t.Fatalf("merger not queued for active rerun: %#v", rerunMerger)
	}
	persistedMerger, err := repo.GetByID(ctx, rerunMerger.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedMerger.Category != models.CategoryActive {
		t.Fatalf("merger rerun category = %s, want active", persistedMerger.Category)
	}
	parent, _ = repo.GetByID(ctx, parent.ID)
	parentCfg, _ = models.ParseSwarmConfig(parent.SwarmConfig)
	if parent.SwarmStatus != "needs_integration" || parent.Status != models.StatusRunning || parent.Category != models.CategoryActive {
		t.Fatalf("parent not reactivated for merger rerun: swarm=%s status=%s category=%s", parent.SwarmStatus, parent.Status, parent.Category)
	}
	mergerCfg, _ := models.ParseSwarmConfig(rerunMerger.SwarmConfig)
	if parentCfg.MergedGeneration >= mergerCfg.RerunGeneration {
		t.Fatalf("parent integration freshness not invalidated for merger rerun: parent=%#v merger=%#v", parentCfg, mergerCfg)
	}
	integrationRun := &models.Execution{TaskID: rerunMerger.ID, Status: models.ExecRunning, PromptSent: "Integrate again"}
	if err := execRepo.Create(ctx, integrationRun); err != nil {
		t.Fatal(err)
	}
	if err := execRepo.Complete(ctx, integrationRun.ID, models.ExecCompleted, "Final rerun result", "", 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, rerunMerger.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := svc.OnChildCompleted(ctx, rerunMerger.ID); err != nil {
		t.Fatal(err)
	}
	parent, _ = repo.GetByID(ctx, parent.ID)
	parentCfg, _ = models.ParseSwarmConfig(parent.SwarmConfig)
	if parent.SwarmStatus != "current" || parent.Status != models.StatusCompleted || parentCfg.MergedGeneration < mergerCfg.RerunGeneration {
		t.Fatalf("merger rerun completion did not refresh parent result: swarm=%s status=%s cfg=%#v", parent.SwarmStatus, parent.Status, parentCfg)
	}
}

func TestSwarmServiceOnChildCompletedIgnoresStaleMergerCompletion(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, execRepo, nil)
	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export", Prompt: "Build export", MaxWorkers: 1, ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	planner, _ := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if planner == nil {
		t.Fatal("planner missing")
	}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, PlannerOutput{Workers: []PlannerWorker{{Title: "Worker", Prompt: "Do work", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", Required: true}}, ReviewerPrompt: "Review", MergerPrompt: "Integrate"}); err != nil {
		t.Fatal(err)
	}
	children, err := repo.ListSwarmChildren(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var reviewer, merger *models.Task
	for i := range children {
		child := children[i]
		cfg, _ := models.ParseSwarmConfig(child.SwarmConfig)
		switch child.SwarmRole {
		case models.SwarmRoleWorker:
			cfg.CompletedGeneration = 1
			child.SwarmConfig, _ = cfg.JSON()
			if err := repo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateStatus(ctx, child.ID, models.StatusCompleted); err != nil {
				t.Fatal(err)
			}
		case models.SwarmRoleReviewer:
			cfg.ReviewedGeneration = 1
			child.SwarmConfig, _ = cfg.JSON()
			if err := repo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateStatus(ctx, child.ID, models.StatusCompleted); err != nil {
				t.Fatal(err)
			}
			reviewer, _ = repo.GetByID(ctx, child.ID)
		case models.SwarmRoleMerger:
			cfg.MergedGeneration = 1
			cfg.RerunGeneration = 1
			child.SwarmConfig, _ = cfg.JSON()
			if err := repo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateStatus(ctx, child.ID, models.StatusCompleted); err != nil {
				t.Fatal(err)
			}
			merger, _ = repo.GetByID(ctx, child.ID)
		}
	}
	if reviewer == nil || merger == nil {
		t.Fatal("reviewer/merger missing")
	}
	exec := &models.Execution{TaskID: merger.ID, Status: models.ExecRunning, PromptSent: requireFullSwarmTestTask(t, repo, merger.ID).Prompt}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatal(err)
	}
	if err := execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "stale final summary", "", 1, 1); err != nil {
		t.Fatal(err)
	}
	parent, _ = repo.GetByID(ctx, parent.ID)
	parentCfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	parentCfg.Generation = 2
	parentCfg.ReviewedGeneration = 1
	parentCfg.MergedGeneration = 1
	parent.SwarmConfig, _ = parentCfg.JSON()
	if err := repo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, "needs_review", parent.SwarmConfig, parent.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, parent.ID, models.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, reviewer.ID, models.StatusPending); err != nil {
		t.Fatal(err)
	}

	if err := svc.OnChildCompleted(ctx, merger.ID); err != nil {
		t.Fatal(err)
	}
	updatedParent, err := repo.GetByID(ctx, parent.ID)
	if err != nil || updatedParent == nil {
		t.Fatalf("get parent: %v", err)
	}
	updatedCfg, _ := models.ParseSwarmConfig(updatedParent.SwarmConfig)
	if updatedParent.Status == models.StatusCompleted || updatedParent.SwarmStatus == "current" || updatedCfg.MergedGeneration != 1 {
		t.Fatalf("stale merger completion updated parent: status=%s swarm=%s cfg=%#v", updatedParent.Status, updatedParent.SwarmStatus, updatedCfg)
	}
	parentExecs, err := execRepo.ListByTask(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(parentExecs) != 0 {
		t.Fatalf("stale merger completion copied parent result: %#v", parentExecs)
	}
}

func TestSwarmServiceOnChildCompletedIgnoresStaleWorkerCompletion(t *testing.T) {
	ctx := context.Background()
	repo, svc, parent, children := newCompletedSwarmForServiceTest(t, ctx)
	worker := children[models.SwarmRoleWorker]
	reviewer := children[models.SwarmRoleReviewer]
	merger := children[models.SwarmRoleMerger]
	if worker == nil || reviewer == nil || merger == nil {
		t.Fatal("required children missing")
	}

	parentCfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	parentCfg.Generation = 2
	parentCfg.ReviewedGeneration = 1
	parentCfg.MergedGeneration = 1
	parent.SwarmConfig, _ = parentCfg.JSON()
	if err := repo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, "needs_review", parent.SwarmConfig, parent.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, parent.ID, models.StatusRunning); err != nil {
		t.Fatal(err)
	}

	workerCfg, _ := models.ParseSwarmConfig(worker.SwarmConfig)
	workerCfg.RerunGeneration = 1
	workerCfg.CompletedGeneration = 1
	worker.SwarmConfig, _ = workerCfg.JSON()
	if err := repo.UpdateSwarmFields(ctx, worker.ID, worker.SwarmRole, "completed", worker.SwarmConfig, worker.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, worker.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}

	reviewerCfg, _ := models.ParseSwarmConfig(reviewer.SwarmConfig)
	reviewerCfg.ReviewedGeneration = 1
	reviewer.SwarmConfig, _ = reviewerCfg.JSON()
	if err := repo.UpdateSwarmFields(ctx, reviewer.ID, reviewer.SwarmRole, "completed", reviewer.SwarmConfig, reviewer.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, reviewer.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}

	mergerCfg, _ := models.ParseSwarmConfig(merger.SwarmConfig)
	mergerCfg.MergedGeneration = 1
	merger.SwarmConfig, _ = mergerCfg.JSON()
	if err := repo.UpdateSwarmFields(ctx, merger.ID, merger.SwarmRole, "completed", merger.SwarmConfig, merger.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, merger.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}

	if err := svc.OnChildCompleted(ctx, worker.ID); err != nil {
		t.Fatal(err)
	}

	updatedWorker, err := repo.GetByID(ctx, worker.ID)
	if err != nil || updatedWorker == nil {
		t.Fatalf("get worker: %v", err)
	}
	updatedWorkerCfg, _ := models.ParseSwarmConfig(updatedWorker.SwarmConfig)
	if updatedWorkerCfg.CompletedGeneration != 1 {
		t.Fatalf("stale worker completion advanced worker freshness: %#v", updatedWorkerCfg)
	}
	updatedReviewer, err := repo.GetByID(ctx, reviewer.ID)
	if err != nil || updatedReviewer == nil {
		t.Fatalf("get reviewer: %v", err)
	}
	if updatedReviewer.Status == models.StatusPending {
		t.Fatalf("stale worker completion started reviewer: %#v", updatedReviewer)
	}
	updatedParent, err := repo.GetByID(ctx, parent.ID)
	if err != nil || updatedParent == nil {
		t.Fatalf("get parent: %v", err)
	}
	if updatedParent.Status == models.StatusCompleted || updatedParent.SwarmStatus == "current" {
		t.Fatalf("stale worker completion finalized parent: status=%s swarm=%s", updatedParent.Status, updatedParent.SwarmStatus)
	}
}

func TestSwarmServiceOnChildCompletedIgnoresStaleReviewerCompletion(t *testing.T) {
	ctx := context.Background()
	repo, svc, parent, children := newCompletedSwarmForServiceTest(t, ctx)
	worker := children[models.SwarmRoleWorker]
	reviewer := children[models.SwarmRoleReviewer]
	merger := children[models.SwarmRoleMerger]
	if worker == nil || reviewer == nil || merger == nil {
		t.Fatal("required children missing")
	}

	parentCfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	parentCfg.Generation = 2
	parentCfg.ReviewedGeneration = 1
	parentCfg.MergedGeneration = 1
	parent.SwarmConfig, _ = parentCfg.JSON()
	if err := repo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, "needs_review", parent.SwarmConfig, parent.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, parent.ID, models.StatusRunning); err != nil {
		t.Fatal(err)
	}

	workerCfg, _ := models.ParseSwarmConfig(worker.SwarmConfig)
	workerCfg.RerunGeneration = 2
	workerCfg.CompletedGeneration = 1
	worker.SwarmConfig, _ = workerCfg.JSON()
	if err := repo.UpdateSwarmFields(ctx, worker.ID, worker.SwarmRole, "pending", worker.SwarmConfig, worker.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, worker.ID, models.StatusPending); err != nil {
		t.Fatal(err)
	}

	reviewerCfg, _ := models.ParseSwarmConfig(reviewer.SwarmConfig)
	reviewerCfg.RerunGeneration = 1
	reviewerCfg.ReviewedGeneration = 1
	reviewer.SwarmConfig, _ = reviewerCfg.JSON()
	if err := repo.UpdateSwarmFields(ctx, reviewer.ID, reviewer.SwarmRole, "completed", reviewer.SwarmConfig, reviewer.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, reviewer.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}

	mergerCfg, _ := models.ParseSwarmConfig(merger.SwarmConfig)
	mergerCfg.MergedGeneration = 1
	mergerCfg.RerunGeneration = 1
	merger.SwarmConfig, _ = mergerCfg.JSON()
	if err := repo.UpdateSwarmFields(ctx, merger.ID, merger.SwarmRole, "completed", merger.SwarmConfig, merger.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, merger.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}

	if err := svc.OnChildCompleted(ctx, reviewer.ID); err != nil {
		t.Fatal(err)
	}

	updatedReviewer, err := repo.GetByID(ctx, reviewer.ID)
	if err != nil || updatedReviewer == nil {
		t.Fatalf("get reviewer: %v", err)
	}
	updatedReviewerCfg, _ := models.ParseSwarmConfig(updatedReviewer.SwarmConfig)
	if updatedReviewerCfg.ReviewedGeneration != 1 {
		t.Fatalf("stale reviewer completion advanced review freshness: %#v", updatedReviewerCfg)
	}
	updatedMerger, err := repo.GetByID(ctx, merger.ID)
	if err != nil || updatedMerger == nil {
		t.Fatalf("get merger: %v", err)
	}
	if updatedMerger.Status == models.StatusPending {
		t.Fatalf("stale reviewer completion started merger: %#v", updatedMerger)
	}
	updatedParent, err := repo.GetByID(ctx, parent.ID)
	if err != nil || updatedParent == nil {
		t.Fatalf("get parent: %v", err)
	}
	if updatedParent.Status == models.StatusCompleted || updatedParent.SwarmStatus == "current" {
		t.Fatalf("stale reviewer completion finalized parent: status=%s swarm=%s", updatedParent.Status, updatedParent.SwarmStatus)
	}
}

func TestSwarmServiceRecomputeParentStatusMovesBlockedParentToBacklogWhenNoChildrenRunnable(t *testing.T) {
	ctx := context.Background()
	repo, svc, parent, children := newCompletedSwarmForServiceTest(t, ctx)

	if err := repo.UpdateStatus(ctx, parent.ID, models.StatusBlocked); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateCategory(ctx, parent.ID, models.CategoryActive); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, "needs_review", parent.SwarmConfig, parent.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	for role, child := range children {
		if child == nil || role == models.SwarmRoleParent {
			continue
		}
		if role == models.SwarmRoleReviewer || role == models.SwarmRoleMerger {
			if err := repo.UpdateStatus(ctx, child.ID, models.StatusCancelled); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateCategory(ctx, child.ID, models.CategoryBacklog); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := repo.UpdateStatus(ctx, child.ID, models.StatusCompleted); err != nil {
			t.Fatal(err)
		}
		if err := repo.UpdateCategory(ctx, child.ID, models.CategoryCompleted); err != nil {
			t.Fatal(err)
		}
	}

	if err := svc.RecomputeParentStatus(ctx, parent.ID); err != nil {
		t.Fatalf("RecomputeParentStatus: %v", err)
	}
	updatedParent, err := repo.GetByID(ctx, parent.ID)
	if err != nil || updatedParent == nil {
		t.Fatalf("get parent: %v", err)
	}
	if updatedParent.Status != models.StatusBlocked {
		t.Fatalf("expected parent to remain blocked without runnable children, got %s", updatedParent.Status)
	}
	if updatedParent.Category != models.CategoryBacklog {
		t.Fatalf("expected blocked parent with no runnable children to move to backlog, got %s", updatedParent.Category)
	}
}

func TestSwarmServiceRecomputeParentStatusDoesNotLetStaleMergerOverrideActiveWork(t *testing.T) {
	ctx := context.Background()
	repo, svc, parent, children := newCompletedSwarmForServiceTest(t, ctx)

	reviewer := children[models.SwarmRoleReviewer]
	if reviewer == nil {
		t.Fatal("reviewer missing")
	}
	if err := repo.UpdateStatus(ctx, reviewer.ID, models.StatusPending); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecomputeParentStatus(ctx, parent.ID); err != nil {
		t.Fatalf("RecomputeParentStatus with pending reviewer: %v", err)
	}
	updatedParent, err := repo.GetByID(ctx, parent.ID)
	if err != nil || updatedParent == nil {
		t.Fatalf("get parent: %v", err)
	}
	if updatedParent.Status != models.StatusRunning {
		t.Fatalf("pending reviewer should keep parent running despite old completed merger, got %s", updatedParent.Status)
	}

	if err := repo.UpdateStatus(ctx, reviewer.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	parentCfg, _ := models.ParseSwarmConfig(updatedParent.SwarmConfig)
	parentCfg.Generation = 2
	parentCfg.MergedGeneration = 1
	updatedParent.SwarmConfig, _ = parentCfg.JSON()
	if err := repo.UpdateSwarmFields(ctx, updatedParent.ID, updatedParent.SwarmRole, "needs_integration", updatedParent.SwarmConfig, updatedParent.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecomputeParentStatus(ctx, parent.ID); err != nil {
		t.Fatalf("RecomputeParentStatus with stale integration: %v", err)
	}
	updatedParent, err = repo.GetByID(ctx, parent.ID)
	if err != nil || updatedParent == nil {
		t.Fatalf("get parent after stale integration: %v", err)
	}
	if updatedParent.Status == models.StatusCompleted {
		t.Fatalf("stale integrated_generation should not complete parent")
	}
}

func TestSwarmServiceHandleChildFollowupIsRoleSpecific(t *testing.T) {
	tests := []struct {
		name                  string
		role                  models.SwarmRole
		wantParentStatus      string
		wantGeneration        int
		wantReviewed          int
		wantIntegrated        int
		wantChildCompleted    int
		wantChildReviewed     int
		wantChildIntegrated   int
		wantChildRerunAtLeast int
	}{
		{name: "worker", role: models.SwarmRoleWorker, wantParentStatus: "needs_review", wantGeneration: 2, wantReviewed: 1, wantIntegrated: 1, wantChildCompleted: 1, wantChildRerunAtLeast: 2},
		{name: "reviewer", role: models.SwarmRoleReviewer, wantParentStatus: "needs_review", wantGeneration: 1, wantReviewed: 1, wantIntegrated: 0, wantChildReviewed: 0, wantChildRerunAtLeast: 1},
		{name: "merger", role: models.SwarmRoleMerger, wantParentStatus: "needs_integration", wantGeneration: 1, wantReviewed: 1, wantIntegrated: 0, wantChildIntegrated: 0, wantChildRerunAtLeast: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo, svc, parent, children := newCompletedSwarmForServiceTest(t, ctx)
			child := children[tc.role]
			if child == nil {
				t.Fatalf("%s child missing", tc.role)
			}
			if err := svc.HandleChildFollowup(ctx, child.ID, "please adjust this role"); err != nil {
				t.Fatalf("HandleChildFollowup: %v", err)
			}
			updatedParent, err := repo.GetByID(ctx, parent.ID)
			if err != nil || updatedParent == nil {
				t.Fatalf("get parent: %v", err)
			}
			parentCfg, _ := models.ParseSwarmConfig(updatedParent.SwarmConfig)
			if updatedParent.SwarmStatus != tc.wantParentStatus || updatedParent.Status != models.StatusRunning || updatedParent.Category != models.CategoryActive {
				t.Fatalf("parent state after %s follow-up: swarm=%s status=%s category=%s", tc.role, updatedParent.SwarmStatus, updatedParent.Status, updatedParent.Category)
			}
			if parentCfg.Generation != tc.wantGeneration || parentCfg.ReviewedGeneration != tc.wantReviewed || parentCfg.MergedGeneration != tc.wantIntegrated {
				t.Fatalf("parent cfg after %s follow-up: %#v", tc.role, parentCfg)
			}
			updatedChild, err := repo.GetByID(ctx, child.ID)
			if err != nil || updatedChild == nil {
				t.Fatalf("get child: %v", err)
			}
			childCfg, _ := models.ParseSwarmConfig(updatedChild.SwarmConfig)
			if childCfg.CompletedGeneration != tc.wantChildCompleted || childCfg.ReviewedGeneration != tc.wantChildReviewed || childCfg.MergedGeneration != tc.wantChildIntegrated || childCfg.RerunGeneration < tc.wantChildRerunAtLeast {
				t.Fatalf("child cfg after %s follow-up: %#v", tc.role, childCfg)
			}
			if tc.role == models.SwarmRoleMerger {
				reviewer := children[models.SwarmRoleReviewer]
				updatedReviewer, _ := repo.GetByID(ctx, reviewer.ID)
				if updatedReviewer.Status != models.StatusCompleted {
					t.Fatalf("merger follow-up should not rerun reviewer: %#v", updatedReviewer)
				}
			}
		})
	}
}

func newCompletedSwarmForServiceTest(t *testing.T, ctx context.Context) (*repository.TaskRepo, *SwarmService, *models.Task, map[models.SwarmRole]*models.Task) {
	t.Helper()
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, nil, nil)
	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export", Prompt: "Build export", MaxWorkers: 1, ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	planner, _ := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if planner == nil {
		t.Fatal("planner missing")
	}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, PlannerOutput{Workers: []PlannerWorker{{Title: "Worker", Prompt: "Do work", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", Required: true}}, ReviewerPrompt: "Review", MergerPrompt: "Integrate"}); err != nil {
		t.Fatal(err)
	}
	children, err := repo.ListSwarmChildren(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	byRole := map[models.SwarmRole]*models.Task{}
	for i := range children {
		child := children[i]
		cfg, _ := models.ParseSwarmConfig(child.SwarmConfig)
		switch child.SwarmRole {
		case models.SwarmRolePlanner:
			if err := repo.UpdateStatus(ctx, child.ID, models.StatusCompleted); err != nil {
				t.Fatal(err)
			}
		case models.SwarmRoleWorker:
			cfg.CompletedGeneration = 1
			child.SwarmConfig, _ = cfg.JSON()
			if err := repo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateStatus(ctx, child.ID, models.StatusCompleted); err != nil {
				t.Fatal(err)
			}
		case models.SwarmRoleReviewer:
			cfg.ReviewedGeneration = 1
			child.SwarmConfig, _ = cfg.JSON()
			if err := repo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateStatus(ctx, child.ID, models.StatusCompleted); err != nil {
				t.Fatal(err)
			}
		case models.SwarmRoleMerger:
			cfg.MergedGeneration = 1
			child.SwarmConfig, _ = cfg.JSON()
			if err := repo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateStatus(ctx, child.ID, models.StatusCompleted); err != nil {
				t.Fatal(err)
			}
		}
		updated, err := repo.GetByID(ctx, child.ID)
		if err != nil || updated == nil {
			t.Fatalf("reload %s child: %v", child.SwarmRole, err)
		}
		byRole[child.SwarmRole] = updated
	}
	parent, _ = repo.GetByID(ctx, parent.ID)
	parentCfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	parentCfg.Generation = 1
	parentCfg.ReviewedGeneration = 1
	parentCfg.MergedGeneration = 1
	parent.SwarmConfig, _ = parentCfg.JSON()
	if err := repo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, "current", parent.SwarmConfig, parent.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, parent.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateCategory(ctx, parent.ID, models.CategoryCompleted); err != nil {
		t.Fatal(err)
	}
	parent, _ = repo.GetByID(ctx, parent.ID)
	return repo, svc, parent, byRole
}

func TestSwarmServiceRerunRoleRejectsActiveRoleExecutionWithoutRetargeting(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	taskSvc := NewTaskService(repo, nil, nil)
	svc := NewSwarmService(taskSvc, repo, execRepo, nil)
	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Build export", Prompt: "Build export", MaxWorkers: 1, ReviewerEnabled: true, MergerEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	planner, _ := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if planner == nil {
		t.Fatal("planner missing")
	}
	if err := svc.ApplyPlannerOutput(ctx, planner.ID, PlannerOutput{Workers: []PlannerWorker{{Title: "Worker", Prompt: "Do work", WorkerKind: "backend", Ownership: []string{"internal/service"}, Isolation: "worktree", Required: true}}, ReviewerPrompt: "Review", MergerPrompt: "Integrate"}); err != nil {
		t.Fatal(err)
	}
	reviewer, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleReviewer)
	if err != nil || reviewer == nil {
		t.Fatalf("reviewer missing: %v", err)
	}
	cfg, _ := models.ParseSwarmConfig(reviewer.SwarmConfig)
	cfg.RerunGeneration = 1
	cfg.ReviewedGeneration = 0
	reviewer.SwarmConfig, _ = cfg.JSON()
	if err := repo.UpdateSwarmFields(ctx, reviewer.ID, reviewer.SwarmRole, reviewer.SwarmStatus, reviewer.SwarmConfig, reviewer.SwarmSequence); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateStatus(ctx, reviewer.ID, models.StatusRunning); err != nil {
		t.Fatal(err)
	}
	exec := &models.Execution{TaskID: reviewer.ID, Status: models.ExecRunning, PromptSent: "active reviewer run"}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatal(err)
	}
	parentBefore, _ := repo.GetByID(ctx, parent.ID)
	reviewerBefore, _ := repo.GetByID(ctx, reviewer.ID)

	_, err = svc.RerunRole(ctx, parent.ID, models.SwarmRoleReviewer)
	if !errors.Is(err, ErrSwarmRoleActive) {
		t.Fatalf("RerunRole error = %v, want ErrSwarmRoleActive", err)
	}
	parentAfter, _ := repo.GetByID(ctx, parent.ID)
	reviewerAfter, _ := repo.GetByID(ctx, reviewer.ID)
	if parentAfter.SwarmConfig != parentBefore.SwarmConfig || parentAfter.SwarmStatus != parentBefore.SwarmStatus || parentAfter.Status != parentBefore.Status || parentAfter.Category != parentBefore.Category {
		t.Fatalf("parent mutated despite rejected active rerun: before=%#v after=%#v", parentBefore, parentAfter)
	}
	if reviewerAfter.SwarmConfig != reviewerBefore.SwarmConfig || reviewerAfter.SwarmStatus != reviewerBefore.SwarmStatus || reviewerAfter.Status != reviewerBefore.Status {
		t.Fatalf("reviewer mutated despite rejected active rerun: before=%#v after=%#v", reviewerBefore, reviewerAfter)
	}
}

type failedChildRepairFixture struct {
	t         *testing.T
	ctx       context.Context
	repo      *repository.TaskRepo
	execRepo  *repository.ExecutionRepo
	svc       *SwarmService
	workerSvc *WorkerService
	parent    *models.Task
	workers   []*models.Task
	reviewer  *models.Task
	merger    *models.Task
}

func newFailedChildRepairFixture(t *testing.T, workers int, reviewerEnabled, mergerEnabled bool) *failedChildRepairFixture {
	t.Helper()
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	workerSvc := newTestWorkerService(t)
	svc := NewSwarmService(NewTaskService(repo, nil, nil), repo, execRepo, workerSvc)
	parent, err := svc.CreateSwarmTask(ctx, CreateSwarmTaskRequest{ProjectID: "default", Title: "Repair failed child", Prompt: "repair", MaxWorkers: workers, ReviewerEnabled: reviewerEnabled, MergerEnabled: mergerEnabled})
	require.NoError(t, err)
	planner, err := repo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	plan := PlannerOutput{ReviewerPrompt: "review", MergerPrompt: "merge"}
	for i := 0; i < workers; i++ {
		plan.Workers = append(plan.Workers, PlannerWorker{Title: fmt.Sprintf("Repair worker %d", i), Prompt: "work", WorkerKind: "backend", Ownership: []string{fmt.Sprintf("scope-%d", i)}, Isolation: "worktree", Required: true})
	}
	require.NoError(t, svc.ApplyPlannerOutput(ctx, planner.ID, plan))
	for i := 0; i < workers; i++ {
		select {
		case <-workerSvc.Submitted():
		default:
			t.Fatal("missing initial worker submission")
		}
	}
	children, err := repo.ListSwarmChildren(ctx, parent.ID)
	require.NoError(t, err)
	f := &failedChildRepairFixture{t: t, ctx: ctx, repo: repo, execRepo: execRepo, svc: svc, workerSvc: workerSvc, parent: parent}
	for i := range children {
		child := children[i]
		switch child.SwarmRole {
		case models.SwarmRoleWorker:
			f.workers = append(f.workers, &child)
		case models.SwarmRoleReviewer:
			f.reviewer = &child
		case models.SwarmRoleMerger:
			f.merger = &child
		}
	}
	sort.Slice(f.workers, func(i, j int) bool { return f.workers[i].SwarmSequence < f.workers[j].SwarmSequence })
	return f
}

func (f *failedChildRepairFixture) terminal(child *models.Task, status models.TaskStatus) {
	f.t.Helper()
	require.NoError(f.t, f.repo.UpdateStatus(f.ctx, child.ID, status))
	require.NoError(f.t, f.svc.OnChildCompleted(f.ctx, child.ID))
}

func (f *failedChildRepairFixture) assertNoSubmission() {
	f.t.Helper()
	select {
	case task := <-f.workerSvc.Submitted():
		f.t.Fatalf("unexpected downstream submission: role=%s id=%s", task.SwarmRole, task.ID)
	default:
	}
}

func (f *failedChildRepairFixture) submittedRole(role models.SwarmRole) {
	f.t.Helper()
	select {
	case task := <-f.workerSvc.Submitted():
		require.Equal(f.t, role, task.SwarmRole)
	case <-time.After(time.Second):
		f.t.Fatalf("missing %s submission", role)
	}
}

func TestSwarmServiceFailedWorkerFollowupRepairStartsReviewerOnce(t *testing.T) {
	f := newFailedChildRepairFixture(t, 2, true, true)
	f.terminal(f.workers[0], models.StatusCompleted)
	f.terminal(f.workers[1], models.StatusFailed)
	f.assertNoSubmission()

	require.NoError(t, f.svc.HandleChildFollowup(f.ctx, f.workers[1].ID, "repair failure"))
	f.terminal(f.workers[1], models.StatusCompleted)
	f.submittedRole(models.SwarmRoleReviewer)
	require.NoError(t, f.svc.OnChildCompleted(f.ctx, f.workers[1].ID))
	f.assertNoSubmission()
}

func TestSwarmServiceFailedWorkerRepairWaitsForRunningSibling(t *testing.T) {
	f := newFailedChildRepairFixture(t, 2, true, true)
	f.terminal(f.workers[0], models.StatusFailed)
	require.NoError(t, f.svc.HandleChildFollowup(f.ctx, f.workers[0].ID, "repair failure"))
	f.terminal(f.workers[0], models.StatusCompleted)
	f.assertNoSubmission()
	f.terminal(f.workers[1], models.StatusCompleted)
	f.submittedRole(models.SwarmRoleReviewer)
}

func TestSwarmServiceFailedWorkerFollowupFailureKeepsReviewerBlocked(t *testing.T) {
	f := newFailedChildRepairFixture(t, 2, true, true)
	f.terminal(f.workers[0], models.StatusCompleted)
	f.terminal(f.workers[1], models.StatusFailed)
	require.NoError(t, f.svc.HandleChildFollowup(f.ctx, f.workers[1].ID, "repair failure"))
	f.terminal(f.workers[1], models.StatusFailed)
	f.assertNoSubmission()
	reviewer, err := f.repo.GetByID(f.ctx, f.reviewer.ID)
	require.NoError(t, err)
	require.NotEqual(t, models.StatusPending, reviewer.Status)
}

func TestSwarmServiceRecomputeRecoversMissedWorkerCompletionCallback(t *testing.T) {
	f := newFailedChildRepairFixture(t, 2, true, true)
	require.NoError(t, f.repo.UpdateStatus(f.ctx, f.workers[0].ID, models.StatusFailed))
	require.NoError(t, f.svc.RecomputeParentStatus(f.ctx, f.parent.ID))
	f.assertNoSubmission()
	failedWorker, err := f.repo.GetByID(f.ctx, f.workers[0].ID)
	require.NoError(t, err)
	require.Equal(t, "failed", failedWorker.SwarmStatus)
	require.NoError(t, f.svc.HandleChildFollowup(f.ctx, f.workers[0].ID, "repair failure"))
	require.NoError(t, f.repo.UpdateStatus(f.ctx, f.workers[0].ID, models.StatusCompleted))
	f.assertNoSubmission()

	// The repaired worker's callback is lost while its sibling is still running.
	require.NoError(t, f.svc.RecomputeParentStatus(f.ctx, f.parent.ID))
	f.assertNoSubmission()

	// The sibling's callback is also lost. Concurrent reconciliation must recover
	// both durable completions and claim the reviewer exactly once.
	require.NoError(t, f.repo.UpdateStatus(f.ctx, f.workers[1].ID, models.StatusCompleted))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, f.svc.RecomputeParentStatus(f.ctx, f.parent.ID))
		}()
	}
	wg.Wait()
	f.submittedRole(models.SwarmRoleReviewer)
	f.assertNoSubmission()

	for _, worker := range f.workers {
		persisted, err := f.repo.GetByID(f.ctx, worker.ID)
		require.NoError(t, err)
		cfg, err := models.ParseSwarmConfig(persisted.SwarmConfig)
		require.NoError(t, err)
		require.Equal(t, 1, cfg.CompletedGeneration)
		require.Equal(t, "completed", persisted.SwarmStatus)
	}
}

func TestSwarmServiceRecomputeRecoversMissedReviewerCompletionCallback(t *testing.T) {
	f := newFailedChildRepairFixture(t, 1, true, true)
	f.terminal(f.workers[0], models.StatusCompleted)
	f.submittedRole(models.SwarmRoleReviewer)
	require.NoError(t, f.repo.UpdateStatus(f.ctx, f.reviewer.ID, models.StatusCompleted))
	f.assertNoSubmission()

	require.NoError(t, f.svc.RecomputeParentStatus(f.ctx, f.parent.ID))
	f.submittedRole(models.SwarmRoleMerger)
	require.NoError(t, f.svc.RecomputeParentStatus(f.ctx, f.parent.ID))
	f.assertNoSubmission()

	reviewer, err := f.repo.GetByID(f.ctx, f.reviewer.ID)
	require.NoError(t, err)
	cfg, err := models.ParseSwarmConfig(reviewer.SwarmConfig)
	require.NoError(t, err)
	require.Equal(t, 1, cfg.ReviewedGeneration)
	require.Equal(t, "reviewed", reviewer.SwarmStatus)

	// Merger completion is durable before its callback; reconciliation must still
	// publish the result and terminalize the parent without another merger run.
	mergerExec := &models.Execution{TaskID: f.merger.ID, Status: models.ExecRunning, PromptSent: requireFullSwarmTestTask(t, f.repo, f.merger.ID).Prompt}
	require.NoError(t, f.execRepo.Create(f.ctx, mergerExec))
	require.NoError(t, f.execRepo.Complete(f.ctx, mergerExec.ID, models.ExecCompleted, "recovered merged result", "", 0, 1))
	require.NoError(t, f.repo.UpdateStatus(f.ctx, f.merger.ID, models.StatusCompleted))
	require.NoError(t, f.svc.RecomputeParentStatus(f.ctx, f.parent.ID))
	f.assertNoSubmission()
	merger, err := f.repo.GetByID(f.ctx, f.merger.ID)
	require.NoError(t, err)
	mergerCfg, err := models.ParseSwarmConfig(merger.SwarmConfig)
	require.NoError(t, err)
	require.Equal(t, 1, mergerCfg.MergedGeneration)
	require.Equal(t, "integrated", merger.SwarmStatus)
	parent, err := f.repo.GetByID(f.ctx, f.parent.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusCompleted, parent.Status)
	require.Equal(t, models.CategoryCompleted, parent.Category)
}

func TestSwarmServiceConcurrentTerminalCallbacksDoNotDuplicateRoles(t *testing.T) {
	f := newFailedChildRepairFixture(t, 2, true, true)
	for _, worker := range f.workers {
		require.NoError(t, f.repo.UpdateStatus(f.ctx, worker.ID, models.StatusCompleted))
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(id string) { defer wg.Done(); require.NoError(t, f.svc.OnChildCompleted(f.ctx, id)) }(f.workers[i%2].ID)
		go func() { defer wg.Done(); require.NoError(t, f.svc.RecomputeParentStatus(f.ctx, f.parent.ID)) }()
	}
	wg.Wait()
	f.submittedRole(models.SwarmRoleReviewer)
	f.assertNoSubmission()

	require.NoError(t, f.repo.UpdateStatus(f.ctx, f.reviewer.ID, models.StatusCompleted))
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); require.NoError(t, f.svc.OnChildCompleted(f.ctx, f.reviewer.ID)) }()
		go func() { defer wg.Done(); require.NoError(t, f.svc.RecomputeParentStatus(f.ctx, f.parent.ID)) }()
	}
	wg.Wait()
	f.submittedRole(models.SwarmRoleMerger)
	f.assertNoSubmission()
}

func TestSwarmServiceFailedReviewerFollowupRepairStartsMergerOnce(t *testing.T) {
	f := newFailedChildRepairFixture(t, 1, true, true)
	f.terminal(f.workers[0], models.StatusCompleted)
	f.submittedRole(models.SwarmRoleReviewer)
	f.terminal(f.reviewer, models.StatusFailed)
	f.assertNoSubmission()
	require.NoError(t, f.svc.HandleChildFollowup(f.ctx, f.reviewer.ID, "repair review"))
	f.terminal(f.reviewer, models.StatusCompleted)
	f.submittedRole(models.SwarmRoleMerger)
	require.NoError(t, f.svc.OnChildCompleted(f.ctx, f.reviewer.ID))
	f.assertNoSubmission()
}

func TestSwarmServiceFailedWorkerRepairHonorsDisabledRoles(t *testing.T) {
	for _, tc := range []struct {
		name            string
		reviewerEnabled bool
		mergerEnabled   bool
		wantRole        models.SwarmRole
	}{
		{name: "reviewer disabled", reviewerEnabled: false, mergerEnabled: true, wantRole: models.SwarmRoleMerger},
		{name: "merger disabled", reviewerEnabled: true, mergerEnabled: false, wantRole: models.SwarmRoleReviewer},
		{name: "both disabled", reviewerEnabled: false, mergerEnabled: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFailedChildRepairFixture(t, 1, tc.reviewerEnabled, tc.mergerEnabled)
			f.terminal(f.workers[0], models.StatusFailed)
			require.NoError(t, f.svc.HandleChildFollowup(f.ctx, f.workers[0].ID, "repair"))
			f.terminal(f.workers[0], models.StatusCompleted)
			if tc.wantRole != "" {
				f.submittedRole(tc.wantRole)
			} else {
				f.assertNoSubmission()
				parent, err := f.repo.GetByID(f.ctx, f.parent.ID)
				require.NoError(t, err)
				require.Equal(t, models.StatusCompleted, parent.Status)
			}
		})
	}
}

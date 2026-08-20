package service

import (
	"context"
	"os"
	"testing"

	"github.com/openvibely/openvibely/internal/memory"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func newTestMemoryResolver(t *testing.T) (*memory.PathResolver, string) {
	t.Helper()
	dir := t.TempDir()
	resolver, err := memory.NewPathResolver("", "")
	if err != nil {
		t.Fatalf("NewPathResolver: %v", err)
	}
	if err := resolver.SetProjectDirOverride("test-project", dir); err != nil {
		t.Fatalf("SetProjectDirOverride: %v", err)
	}
	return resolver, dir
}

// TestNewMemoryService verifies the constructor returns a non-nil service.
func TestNewMemoryService(t *testing.T) {
	resolver, _ := newTestMemoryResolver(t)
	store := memory.NewFileStore(resolver)

	svc := NewMemoryService(nil, nil, nil, nil, store, resolver)
	if svc == nil {
		t.Fatal("expected non-nil MemoryService")
	}
}

// TestMemoryService_SetLifecycleRepo verifies nil safety on a nil service receiver.
func TestMemoryService_SetLifecycleRepo_NilSafe(t *testing.T) {
	var svc *MemoryService
	// Should not panic
	svc.SetLifecycleRepo(nil)
}

// TestMemoryService_SetLifecycleRepo verifies the repo is stored.
func TestMemoryService_SetLifecycleRepo_Stores(t *testing.T) {
	resolver, _ := newTestMemoryResolver(t)
	store := memory.NewFileStore(resolver)
	svc := NewMemoryService(nil, nil, nil, nil, store, resolver)

	db := testutil.NewTestDB(t)
	repo := repository.NewLifecycleRepo(db)
	svc.SetLifecycleRepo(repo)

	if svc.lifecycleRepo != repo {
		t.Error("expected lifecycleRepo to be set")
	}
}

// TestMemoryService_EnsureProject_NilTaskRepo verifies that EnsureProject succeeds
// when taskRepo/scheduleRepo/agentRepo are nil (consolidation-task path is skipped).
// A directory override is pre-configured so the filesystem path succeeds.
func TestMemoryService_EnsureProject_NilTaskRepo(t *testing.T) {
	resolver, _ := newTestMemoryResolver(t)
	store := memory.NewFileStore(resolver)
	svc := NewMemoryService(nil, nil, nil, nil, store, resolver)

	err := svc.EnsureProject(context.Background(), "test-project")
	if err != nil {
		t.Fatalf("EnsureProject with nil repos failed: %v", err)
	}
}

// TestMemoryService_EnsureProject_NilTaskRepo_Idempotent verifies double-call safety.
func TestMemoryService_EnsureProject_NilTaskRepo_Idempotent(t *testing.T) {
	resolver, _ := newTestMemoryResolver(t)
	store := memory.NewFileStore(resolver)
	svc := NewMemoryService(nil, nil, nil, nil, store, resolver)

	for i := 0; i < 3; i++ {
		if err := svc.EnsureProject(context.Background(), "test-project"); err != nil {
			t.Fatalf("EnsureProject call %d failed: %v", i+1, err)
		}
	}
}

// TestMemoryService_EnsureProject_NoRepoPath verifies that EnsureProject returns an
// error when the project has no local repo_path configured.
func TestMemoryService_EnsureProject_NoRepoPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)

	resolver, _ := memory.NewPathResolver("", "")
	store := memory.NewFileStore(resolver)

	svc := NewMemoryService(taskRepo, scheduleRepo, agentRepo, projectRepo, store, resolver)

	// Create a project with no repo_path
	p := &models.Project{Name: "No-Path Project"}
	if err := projectRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	err := svc.EnsureProject(context.Background(), p.ID)
	// Should fail because no local repo_path is set
	if err == nil {
		t.Fatal("expected error for project with no repo_path, got nil")
	}
}

// TestMemoryService_EnsureProject_WithRepoPath exercises the full EnsureProject path:
// memory directory setup, agent reconciliation, and consolidation task/schedule creation.
func TestMemoryService_EnsureProject_WithRepoPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)

	resolver, _ := memory.NewPathResolver("", "")
	store := memory.NewFileStore(resolver)

	svc := NewMemoryService(taskRepo, scheduleRepo, agentRepo, projectRepo, store, resolver)

	// Create a project with a real temp dir as the repo path
	repoPath := t.TempDir()
	p := &models.Project{Name: "Repo-Path Project", RepoPath: repoPath}
	if err := projectRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	ctx := context.Background()
	if err := svc.EnsureProject(ctx, p.ID); err != nil {
		t.Fatalf("EnsureProject failed: %v", err)
	}

	// Verify the memory directory was created
	expectedDir := repoPath + "/.openvibely/memories"
	if _, err := os.Stat(expectedDir); err != nil {
		t.Errorf("expected memory dir %s to exist: %v", expectedDir, err)
	}

	// Verify the consolidation task was created
	task, err := taskRepo.GetByProjectAndTitle(ctx, p.ID, memoryConsolidationTaskTitle)
	if err != nil {
		t.Fatalf("GetByProjectAndTitle: %v", err)
	}
	if task == nil {
		t.Fatal("expected consolidation task to be created")
	}
	if task.Category != models.CategoryScheduled {
		t.Errorf("expected category scheduled, got %q", task.Category)
	}

	// Verify the schedule was created
	schedules, err := scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(schedules))
	}
	if schedules[0].RepeatType != models.RepeatDaily {
		t.Errorf("expected daily schedule, got %q", schedules[0].RepeatType)
	}
	if !schedules[0].ClearContextOnStart {
		t.Fatal("expected memory consolidation schedule to clear context on start")
	}
}

func TestMemoryService_EnsureProject_RepairsMaintenanceScheduleClearContext(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)

	resolver, _ := memory.NewPathResolver("", "")
	store := memory.NewFileStore(resolver)

	svc := NewMemoryService(taskRepo, scheduleRepo, agentRepo, projectRepo, store, resolver)

	p := &models.Project{Name: "Repair Project", RepoPath: t.TempDir()}
	if err := projectRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	ctx := context.Background()
	if err := svc.EnsureProject(ctx, p.ID); err != nil {
		t.Fatalf("EnsureProject initial: %v", err)
	}
	task, err := taskRepo.GetByProjectAndTitle(ctx, p.ID, memoryConsolidationTaskTitle)
	if err != nil || task == nil {
		t.Fatalf("expected consolidation task: %v", err)
	}
	schedules, err := scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask initial: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected one schedule, got %d", len(schedules))
	}
	originalRunAt := schedules[0].RunAt
	originalNextRun := schedules[0].NextRun
	if err := scheduleRepo.UpdateClearContextOnStart(ctx, schedules[0].ID, task.ID, false); err != nil {
		t.Fatalf("make schedule stale: %v", err)
	}

	if err := svc.EnsureProject(ctx, p.ID); err != nil {
		t.Fatalf("EnsureProject repair: %v", err)
	}
	repaired, err := scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask repaired: %v", err)
	}
	if len(repaired) != 1 {
		t.Fatalf("expected one repaired schedule, got %d", len(repaired))
	}
	if !repaired[0].ClearContextOnStart {
		t.Fatal("expected stale memory consolidation schedule clear-context flag to be repaired")
	}
	if !repaired[0].RunAt.Equal(originalRunAt) {
		t.Fatalf("repair changed run_at: got %s want %s", repaired[0].RunAt, originalRunAt)
	}
	if originalNextRun == nil || repaired[0].NextRun == nil || !repaired[0].NextRun.Equal(*originalNextRun) {
		t.Fatalf("repair changed next_run: got %v want %v", repaired[0].NextRun, originalNextRun)
	}
}

// TestMemoryService_EnsureProject_WithRepoPath_Idempotent verifies that calling
// EnsureProject multiple times does not create duplicate tasks or schedules.
func TestMemoryService_EnsureProject_WithRepoPath_Idempotent(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)

	resolver, _ := memory.NewPathResolver("", "")
	store := memory.NewFileStore(resolver)

	svc := NewMemoryService(taskRepo, scheduleRepo, agentRepo, projectRepo, store, resolver)

	repoPath := t.TempDir()
	p := &models.Project{Name: "Idempotent Project", RepoPath: repoPath}
	if err := projectRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := svc.EnsureProject(ctx, p.ID); err != nil {
			t.Fatalf("EnsureProject call %d failed: %v", i+1, err)
		}
	}

	// Verify exactly one consolidation task and one schedule
	task, err := taskRepo.GetByProjectAndTitle(ctx, p.ID, memoryConsolidationTaskTitle)
	if err != nil || task == nil {
		t.Fatalf("expected consolidation task: %v", err)
	}
	schedules, err := scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(schedules) != 1 {
		t.Errorf("expected exactly 1 schedule after 3 calls, got %d", len(schedules))
	}
}

func TestMemoryService_EnsureGlobalAgentsCreatesAndRepairsMemoryCuratorAndHooks(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	staleProject := &models.Project{Name: "stale-memory-agent-project", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, staleProject); err != nil {
		t.Fatalf("create stale project: %v", err)
	}
	resolver, _ := memory.NewPathResolver("", "")
	store := memory.NewFileStore(resolver)
	svc := NewMemoryService(nil, nil, agentRepo, nil, store, resolver)
	svc.SetLifecycleRepo(lifecycleRepo)

	if err := svc.EnsureGlobalAgents(ctx); err != nil {
		t.Fatalf("EnsureGlobalAgents create: %v", err)
	}
	agent, err := agentRepo.GetBySystemKind(ctx, models.AgentSystemKindMemoryCurator)
	if err != nil || agent == nil {
		t.Fatalf("GetBySystemKind(memory_curator): %v %#v", err, agent)
	}
	if agent.Key != models.AgentSystemKindMemoryCurator || agent.GeneratedStatus != models.AgentStatusProtected || agent.CreatedBy != models.AgentCreatedBySystem || agent.SelectableAsPrimary || !agent.Enabled {
		t.Fatalf("unexpected fresh Memory Curator identity: %#v", agent)
	}
	if !AgentAllowsTool(agent, models.AgentToolScopedFiles) || !AgentAllowsTool(agent, "memory_view") || !agent.ToolConfig.SkipDefaultTools || !agent.ToolConfig.DisableRuntimeWorktree || len(agent.ToolConfig.ScopedFiles) != 1 {
		t.Fatalf("unexpected fresh Memory Curator tool grants: tools=%v config=%#v", agent.Tools, agent.ToolConfig)
	}
	if !agent.PermissionDefaults.ReadTaskPrompt || !agent.PermissionDefaults.ReadTaskExecution || !agent.PermissionDefaults.ReadProjectMemory || !agent.PermissionDefaults.WriteProjectMemory || agent.PermissionDefaults.ReadAgents || agent.PermissionDefaults.ReadSkills {
		t.Fatalf("unexpected fresh Memory Curator permission defaults: %#v", agent.PermissionDefaults)
	}
	if len(agent.SourceRefs) != 1 || agent.SourceRefs[0] != bundledMemoryDeclarationPath {
		t.Fatalf("unexpected Memory Curator source refs: %#v", agent.SourceRefs)
	}
	for _, want := range []string{"recall_memory", "update_memory", "consolidate_memory"} {
		found := false
		for _, skill := range agent.Skills {
			if skill.Name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected Memory Curator skill %q loaded from root index, got %#v", want, agent.Skills)
		}
	}
	hooks, err := lifecycleRepo.HooksByAgent(ctx, agent.ID)
	if err != nil {
		t.Fatalf("HooksByAgent fresh: %v", err)
	}
	if len(hooks) != 2 {
		t.Fatalf("expected two fresh Memory Curator hooks, got %d: %#v", len(hooks), hooks)
	}

	agent.Name = "Broken Memory Agent"
	agent.Description = "stale"
	agent.SystemPrompt = "stale prompt"
	agent.Model = "stale-model"
	agent.Tools = []string{"memory_view"}
	agent.ToolConfig = models.AgentToolConfig{}
	agent.Skills = nil
	agent.SystemKind = "memory"
	agent.Key = "memory"
	agent.Scope = models.AgentScopeProject
	agent.ProjectID = staleProject.ID
	agent.SelectableAsPrimary = true
	agent.Enabled = false
	agent.GeneratedStatus = models.AgentStatusGenerated
	agent.CreatedBy = models.AgentCreatedByAgent
	agent.SourceRefs = []string{"stale"}
	agent.PermissionDefaults = models.AgentPermissionDefaults{ReadAgents: true, ReadSkills: true}
	agent.ModelDefaults = models.AgentModelDefaults{Model: "stale", Temperature: 0.9, MaxTokens: 99}
	if err := agentRepo.Update(ctx, agent); err != nil {
		t.Fatalf("make Memory Curator stale: %v", err)
	}
	for _, hook := range hooks {
		switch hook.When {
		case models.LifecycleRouteTask:
			hook.Blocking = true
			hook.OutputContract = models.OutputContractActivitySummary
		case models.LifecycleAfterComplete:
			hook.PayloadJSON = "{}"
		}
		if err := lifecycleRepo.UpdateHook(ctx, &hook); err != nil {
			t.Fatalf("make hook stale: %v", err)
		}
	}
	for _, legacy := range []models.AgentLifecycleHook{
		{AgentID: agent.ID, When: models.LifecycleBeforeRun, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Enabled: true},
		{AgentID: agent.ID, When: models.LifecycleScheduled, SkillKey: "consolidate_memory", OutputContract: models.OutputContractActivitySummary, Enabled: true},
	} {
		legacy := legacy
		if err := lifecycleRepo.CreateHook(ctx, &legacy); err != nil {
			t.Fatalf("create legacy hook: %v", err)
		}
	}

	if err := svc.EnsureGlobalAgents(ctx); err != nil {
		t.Fatalf("EnsureGlobalAgents repair: %v", err)
	}
	repaired, err := agentRepo.GetBySystemKind(ctx, models.AgentSystemKindMemoryCurator)
	if err != nil || repaired == nil {
		t.Fatalf("GetBySystemKind repaired: %v %#v", err, repaired)
	}
	if repaired.ID != agent.ID {
		t.Fatalf("expected Memory Curator repaired in place, got id=%s want=%s", repaired.ID, agent.ID)
	}
	if repaired.Name == "Broken Memory Agent" || repaired.Key != models.AgentSystemKindMemoryCurator || repaired.Scope != models.AgentScopeGlobal || repaired.ProjectID != "" || repaired.SelectableAsPrimary || !repaired.Enabled || repaired.GeneratedStatus != models.AgentStatusProtected || repaired.CreatedBy != models.AgentCreatedBySystem {
		t.Fatalf("Memory Curator stale identity was not repaired: %#v", repaired)
	}
	if !AgentAllowsTool(repaired, models.AgentToolScopedFiles) || !repaired.ToolConfig.SkipDefaultTools || !repaired.ToolConfig.DisableRuntimeWorktree || len(repaired.ToolConfig.ScopedFiles) != 1 {
		t.Fatalf("Memory Curator stale tool config was not repaired: tools=%v config=%#v", repaired.Tools, repaired.ToolConfig)
	}
	if !repaired.PermissionDefaults.ReadTaskPrompt || !repaired.PermissionDefaults.ReadTaskExecution || !repaired.PermissionDefaults.ReadProjectMemory || !repaired.PermissionDefaults.WriteProjectMemory || repaired.PermissionDefaults.ReadAgents || repaired.PermissionDefaults.ReadSkills {
		t.Fatalf("Memory Curator stale permissions were not repaired: %#v", repaired.PermissionDefaults)
	}
	if repaired.ModelDefaults.Model != "inherit" || repaired.ModelDefaults.Temperature != 0 || repaired.ModelDefaults.MaxTokens != 0 {
		t.Fatalf("Memory Curator stale model defaults were not repaired: %#v", repaired.ModelDefaults)
	}
	repairedHooks, err := lifecycleRepo.HooksByAgent(ctx, repaired.ID)
	if err != nil {
		t.Fatalf("HooksByAgent repaired: %v", err)
	}
	if len(repairedHooks) != 2 {
		t.Fatalf("expected stale legacy Memory hooks deleted, got %d: %#v", len(repairedHooks), repairedHooks)
	}
	have := map[string]models.AgentLifecycleHook{}
	for _, hook := range repairedHooks {
		have[string(hook.When)+"/"+hook.SkillKey] = hook
	}
	if hook, ok := have["route_task/recall_memory"]; !ok || hook.Blocking || hook.OutputContract != models.OutputContractSelectedMemories || !hook.Enabled {
		t.Fatalf("Memory route hook was not repaired: %#v", hook)
	}
	if hook, ok := have["after_complete/update_memory"]; !ok || hook.OutputContract != models.OutputContractActivitySummary || hook.PayloadJSON != `{"blocks":["conversation_transcript","assigned_agent"]}` || !hook.Enabled {
		t.Fatalf("Memory after-complete hook was not repaired: %#v", hook)
	}
}

func TestMemoryService_EnsureGlobalAgentsRepairsLegacyMemoryAliasInPlace(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	resolver, _ := memory.NewPathResolver("", "")
	store := memory.NewFileStore(resolver)
	svc := NewMemoryService(nil, nil, agentRepo, nil, store, resolver)
	svc.SetLifecycleRepo(lifecycleRepo)

	legacy := &models.Agent{
		Name:                "System: Memory",
		Key:                 "memory",
		SystemKind:          "memory",
		Scope:               models.AgentScopeGlobal,
		SelectableAsPrimary: true,
		Enabled:             false,
		GeneratedStatus:     models.AgentStatusGenerated,
		CreatedBy:           models.AgentCreatedByAgent,
	}
	if err := agentRepo.Create(ctx, legacy); err != nil {
		t.Fatalf("create legacy Memory alias: %v", err)
	}
	superseded := &models.Agent{Name: "System: Memory Consolidator", Key: "memory_consolidator", SystemKind: "memory_consolidator", Scope: models.AgentScopeGlobal, Enabled: true}
	if err := agentRepo.Create(ctx, superseded); err != nil {
		t.Fatalf("create superseded Memory agent: %v", err)
	}
	for _, legacyHook := range []models.AgentLifecycleHook{
		{AgentID: legacy.ID, When: models.LifecycleBeforeRun, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Enabled: true},
		{AgentID: legacy.ID, When: models.LifecycleScheduled, SkillKey: "consolidate_memory", OutputContract: models.OutputContractActivitySummary, Enabled: true},
	} {
		legacyHook := legacyHook
		if err := lifecycleRepo.CreateHook(ctx, &legacyHook); err != nil {
			t.Fatalf("create legacy hook: %v", err)
		}
	}

	if err := svc.EnsureGlobalAgents(ctx); err != nil {
		t.Fatalf("EnsureGlobalAgents: %v", err)
	}
	agent, err := agentRepo.GetBySystemKind(ctx, models.AgentSystemKindMemoryCurator)
	if err != nil || agent == nil {
		t.Fatalf("GetBySystemKind(memory_curator): %v %#v", err, agent)
	}
	if agent.ID != legacy.ID {
		t.Fatalf("expected legacy Memory alias repaired in place, got id=%s want=%s", agent.ID, legacy.ID)
	}
	if agent.Key != models.AgentSystemKindMemoryCurator || agent.Name != memoryAgentName || agent.GeneratedStatus != models.AgentStatusProtected || agent.CreatedBy != models.AgentCreatedBySystem || agent.SelectableAsPrimary || !agent.Enabled {
		t.Fatalf("legacy Memory alias was not repaired to canonical protected row: %#v", agent)
	}
	deleted, err := agentRepo.GetByKey(ctx, "memory_consolidator")
	if err != nil {
		t.Fatalf("GetByKey(memory_consolidator): %v", err)
	}
	if deleted != nil {
		t.Fatalf("expected superseded Memory Consolidator deleted, got %#v", deleted)
	}
	hooks, err := lifecycleRepo.HooksByAgent(ctx, agent.ID)
	if err != nil {
		t.Fatalf("HooksByAgent: %v", err)
	}
	if len(hooks) != 2 {
		t.Fatalf("expected only declared Memory hooks after legacy cleanup, got %#v", hooks)
	}
	for _, hook := range hooks {
		if hook.When == models.LifecycleBeforeRun || hook.When == models.LifecycleScheduled {
			t.Fatalf("legacy Memory hook was not deleted: %#v", hook)
		}
	}
}

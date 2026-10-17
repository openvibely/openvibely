package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/memory"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

type memoryScheduleTestRepos struct {
	tasks      persistence.TaskStore
	schedules  persistence.ScheduleStore
	agents     persistence.AgentStore
	lifecycles persistence.LifecycleStore
	projectID  string
}

func newMemoryScheduleTestService(t *testing.T) (*MemoryService, memoryScheduleTestRepos) {
	t.Helper()
	storeEnv := teststores.New(t)
	projectRepo := storeEnv.Stores.Projects
	project := &models.Project{Name: "Memory test", RepoPath: t.TempDir()}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	taskRepo := storeEnv.Stores.Tasks
	scheduleRepo := storeEnv.Stores.Schedules
	agentRepo := storeEnv.Stores.Agents
	lifecycleRepo := storeEnv.Stores.Lifecycle
	resolver, err := memory.NewPathResolver(t.TempDir(), "")
	if err != nil {
		t.Fatalf("path resolver: %v", err)
	}
	store := memory.NewFileStore(resolver)
	svc := NewMemoryService(taskRepo, scheduleRepo, agentRepo, projectRepo, store, resolver)
	svc.SetLifecycleRepo(lifecycleRepo)
	return svc, memoryScheduleTestRepos{tasks: taskRepo, schedules: scheduleRepo, agents: agentRepo, lifecycles: lifecycleRepo, projectID: project.ID}
}

func TestMemoryServiceEnsureProjectUsesRepoMemoryDir(t *testing.T) {
	ctx := context.Background()
	svc, repos := newMemoryScheduleTestService(t)
	project, err := svc.projectRepo.GetByID(ctx, repos.projectID)
	if err != nil || project == nil {
		t.Fatalf("get project: %v", err)
	}
	wantDir, err := memory.SharedRepoMemoryDir(project.RepoPath)
	if err != nil {
		t.Fatalf("shared dir: %v", err)
	}
	if err := svc.EnsureProject(ctx, repos.projectID); err != nil {
		t.Fatalf("ensure project: %v", err)
	}
	got, err := svc.pathResolver.ProjectDir(repos.projectID)
	if err != nil {
		t.Fatalf("project dir: %v", err)
	}
	if got != wantDir {
		t.Fatalf("project dir = %q, want repo memory dir %q", got, wantDir)
	}
	if info, err := os.Stat(wantDir); err != nil || !info.IsDir() {
		t.Fatalf("expected repo memory dir to exist: info=%v err=%v", info, err)
	}
	wantIndex := wantDir + string(os.PathSeparator) + memory.IndexFileName
	if _, err := os.Stat(wantIndex); err != nil {
		t.Fatalf("expected repo-local memory index to exist: %v", err)
	}
}

func TestMemoryServiceEnsureProjectMigratesLegacyMemoryDir(t *testing.T) {
	ctx := context.Background()
	svc, repos := newMemoryScheduleTestService(t)
	project, err := svc.projectRepo.GetByID(ctx, repos.projectID)
	if err != nil || project == nil {
		t.Fatalf("get project: %v", err)
	}
	legacyDir, err := memory.SharedRepoLegacyMemoryDir(project.RepoPath)
	if err != nil {
		t.Fatalf("legacy dir: %v", err)
	}
	newDir, err := memory.SharedRepoMemoryDir(project.RepoPath)
	if err != nil {
		t.Fatalf("new dir: %v", err)
	}
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, memory.LegacyIndexFileName), []byte("# Legacy\n"), 0o644); err != nil {
		t.Fatalf("write legacy index: %v", err)
	}

	if err := svc.EnsureProject(ctx, repos.projectID); err != nil {
		t.Fatalf("ensure project: %v", err)
	}
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("expected legacy dir to be moved away, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(newDir, memory.IndexFileName)); err != nil {
		t.Fatalf("expected migrated index at new path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newDir, memory.LegacyIndexFileName)); !os.IsNotExist(err) {
		t.Fatalf("expected old index filename to be migrated, err=%v", err)
	}
	got, err := svc.pathResolver.ProjectDir(repos.projectID)
	if err != nil {
		t.Fatalf("project dir: %v", err)
	}
	if got != newDir {
		t.Fatalf("project dir = %q, want %q", got, newDir)
	}
}

func TestMemoryServiceEnsureProjectMergesAndRemovesLegacyMemoryDirWhenBothExist(t *testing.T) {
	ctx := context.Background()
	svc, repos := newMemoryScheduleTestService(t)
	project, err := svc.projectRepo.GetByID(ctx, repos.projectID)
	if err != nil || project == nil {
		t.Fatalf("get project: %v", err)
	}
	legacyDir, err := memory.SharedRepoLegacyMemoryDir(project.RepoPath)
	if err != nil {
		t.Fatalf("legacy dir: %v", err)
	}
	newDir, err := memory.SharedRepoMemoryDir(project.RepoPath)
	if err != nil {
		t.Fatalf("new dir: %v", err)
	}
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy dir: %v", err)
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatalf("mkdir new dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "legacy_topic.md"), []byte("legacy"), 0o644); err != nil {
		t.Fatalf("write legacy topic: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, memory.LegacyIndexFileName), []byte("# Legacy\n"), 0o644); err != nil {
		t.Fatalf("write legacy index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "conflict.md"), []byte("legacy conflict"), 0o644); err != nil {
		t.Fatalf("write legacy conflict: %v", err)
	}
	if err := os.WriteFile(filepath.Join(newDir, memory.IndexFileName), []byte("# New\n"), 0o644); err != nil {
		t.Fatalf("write new index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "conflict.md"), []byte("new conflict"), 0o644); err != nil {
		t.Fatalf("write new conflict: %v", err)
	}

	if err := svc.EnsureProject(ctx, repos.projectID); err != nil {
		t.Fatalf("ensure project: %v", err)
	}
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("expected legacy dir to be removed after merge, err=%v", err)
	}
	if got, err := os.ReadFile(filepath.Join(newDir, "legacy_topic.md")); err != nil || string(got) != "legacy" {
		t.Fatalf("expected legacy topic to be moved into new dir: got=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(newDir, memory.IndexFileName)); err != nil || string(got) != "# New\n" {
		t.Fatalf("expected existing new index to remain: got=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(newDir, "conflict.md")); err != nil || string(got) != "new conflict" {
		t.Fatalf("expected existing conflicting file to remain: got=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(newDir, "conflict.legacy1.md")); err != nil || string(got) != "legacy conflict" {
		t.Fatalf("expected conflicting legacy file to be preserved with suffix: got=%q err=%v", got, err)
	}
}

func TestMemoryServiceEnsureProjectCreatesMemoryCuratorAgentAndSchedule(t *testing.T) {
	ctx := context.Background()
	svc, repos := newMemoryScheduleTestService(t)

	if err := svc.EnsureProject(ctx, repos.projectID); err != nil {
		t.Fatalf("ensure project: %v", err)
	}

	agent, err := repos.agents.GetBySystemKind(ctx, models.AgentSystemKindMemoryCurator)
	if err != nil {
		t.Fatalf("get memory curator agent: %v", err)
	}
	if agent == nil {
		t.Fatal("expected memory curator agent")
	}
	hasScopedFiles := false
	for _, tool := range agent.Tools {
		if tool == models.AgentToolScopedFiles {
			hasScopedFiles = true
			break
		}
	}
	if !hasScopedFiles {
		t.Fatalf("expected Memory Curator agent to declare %q tool, got %v", models.AgentToolScopedFiles, agent.Tools)
	}
	if len(agent.ToolConfig.ScopedFiles) != 1 || agent.ToolConfig.ScopedFiles[0].Directory != ".openvibely/memories" || !agent.ToolConfig.SkipDefaultTools || !agent.ToolConfig.DisableRuntimeWorktree {
		t.Fatalf("unexpected scoped file config: %+v", agent.ToolConfig)
	}
	if agent.SystemPrompt == "" || agent.Name != memoryAgentName {
		t.Fatalf("unexpected built-in agent: %#v", agent)
	}
	wantSkills := map[string]bool{"consolidate_memory": false}
	for _, skill := range agent.Skills {
		if _, ok := wantSkills[skill.Name]; ok {
			wantSkills[skill.Name] = true
		}
	}
	for skill, found := range wantSkills {
		if !found {
			t.Fatalf("expected Memory Curator skill %q in %#v", skill, agent.Skills)
		}
	}
	existingHooks, err := repos.lifecycles.HooksByAgent(ctx, agent.ID)
	if err != nil {
		t.Fatalf("list initial memory hooks: %v", err)
	}
	for _, hook := range existingHooks {
		if hook.When == models.LifecycleRouteTask && hook.SkillKey == "recall_memory" {
			hook.Blocking = true
			if err := repos.lifecycles.UpdateHook(ctx, &hook); err != nil {
				t.Fatalf("make recall route hook stale blocking: %v", err)
			}
		}
	}
	if err := repos.lifecycles.CreateHook(ctx, &models.AgentLifecycleHook{AgentID: agent.ID, When: models.LifecycleScheduled, SkillKey: "consolidate_memory", OutputContract: models.OutputContractActivitySummary, Blocking: true, Enabled: true}); err != nil {
		t.Fatalf("create stale scheduled hook: %v", err)
	}
	if err := repos.lifecycles.CreateHook(ctx, &models.AgentLifecycleHook{AgentID: agent.ID, When: models.LifecycleBeforeRun, SkillKey: "recall_memory", OutputContract: models.OutputContractContextBlock, Blocking: true, Enabled: true}); err != nil {
		t.Fatalf("create stale before_run recall hook: %v", err)
	}
	if err := svc.EnsureProject(ctx, repos.projectID); err != nil {
		t.Fatalf("repair project after stale hook: %v", err)
	}
	hooks, err := repos.lifecycles.HooksByAgent(ctx, agent.ID)
	if err != nil {
		t.Fatalf("list memory hooks: %v", err)
	}
	if len(hooks) != 0 {
		t.Fatalf("obsolete hooks remain: %#v", hooks)
	}

	tasks, err := repos.tasks.ListByProject(ctx, repos.projectID, string(models.CategoryScheduled))
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected one scheduled task, got %d", len(tasks))
	}
	task := tasks[0]
	if task.AgentDefinitionID == nil || *task.AgentDefinitionID != agent.ID {
		t.Fatalf("scheduled task agent = %v, want %s", task.AgentDefinitionID, agent.ID)
	}
	if task.CreatedVia != models.TaskOriginWeb {
		t.Fatalf("scheduled task created_via = %q, want normal web origin", task.CreatedVia)
	}
	for _, forbidden := range []string{"OpenVibely", "built-in system agent", "Built-in system agent"} {
		if strings.Contains(task.Prompt, forbidden) {
			t.Fatalf("scheduled memory consolidation prompt contains unnecessary wording %q: %q", forbidden, task.Prompt)
		}
	}
	for _, want := range []string{"this project's durable memory", "Keep MEMORIES.md as the compact index", "Do not store transient logs"} {
		if !strings.Contains(task.Prompt, want) {
			t.Fatalf("scheduled memory consolidation prompt missing %q: %q", want, task.Prompt)
		}
	}
	schedules, err := repos.schedules.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(schedules) != 1 || schedules[0].RepeatType != models.RepeatDaily || schedules[0].RepeatInterval != 1 {
		t.Fatalf("unexpected schedule: %#v", schedules)
	}
}

func TestMemoryServiceEnsureProjectDoesNotRewriteUserScheduledTaskAssignedToMemoryAgent(t *testing.T) {
	ctx := context.Background()
	svc, repos := newMemoryScheduleTestService(t)

	if err := svc.EnsureProject(ctx, repos.projectID); err != nil {
		t.Fatalf("ensure project first: %v", err)
	}
	agent, err := repos.agents.GetBySystemKind(ctx, models.AgentSystemKindMemoryCurator)
	if err != nil || agent == nil {
		t.Fatalf("get memory agent: %v", err)
	}
	agentID := agent.ID
	userTask := &models.Task{
		ProjectID:         repos.projectID,
		Title:             "User scheduled memory review",
		Category:          models.CategoryScheduled,
		Status:            models.StatusPending,
		Prompt:            "Use the memory agent for a manual review.",
		AgentDefinitionID: &agentID,
		Tag:               models.TagNone,
		ChainConfig:       "{}",
		CreatedVia:        models.TaskOriginWeb,
	}
	if err := repos.tasks.Create(ctx, userTask); err != nil {
		t.Fatalf("create user scheduled task: %v", err)
	}

	if err := svc.EnsureProject(ctx, repos.projectID); err != nil {
		t.Fatalf("ensure project second: %v", err)
	}
	got, err := repos.tasks.GetByID(ctx, userTask.ID)
	if err != nil {
		t.Fatalf("get user task: %v", err)
	}
	if got.Title != userTask.Title || got.Prompt != userTask.Prompt {
		t.Fatalf("user task was rewritten: %#v", got)
	}
	systemTask, err := repos.tasks.GetByProjectAndTitle(ctx, repos.projectID, memoryConsolidationTaskTitle)
	if err != nil || systemTask == nil {
		t.Fatalf("get system task: task=%#v err=%v", systemTask, err)
	}
	if systemTask.ID == userTask.ID {
		t.Fatal("system task reused user task id")
	}
}

func TestMemoryServiceEnsureProjectIsIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, repos := newMemoryScheduleTestService(t)

	if err := svc.EnsureProject(ctx, repos.projectID); err != nil {
		t.Fatalf("ensure project first: %v", err)
	}
	if err := svc.EnsureProject(ctx, repos.projectID); err != nil {
		t.Fatalf("ensure project second: %v", err)
	}

	agents, err := repos.agents.ListBySystemKind(ctx, models.AgentSystemKindMemoryCurator)
	if err != nil {
		t.Fatalf("list memory curator agents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected one live memory curator agent, got %d", len(agents))
	}
	tasks, err := repos.tasks.ListByProject(ctx, repos.projectID, string(models.CategoryScheduled))
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected one scheduled task, got %d", len(tasks))
	}
	schedules, err := repos.schedules.ListByTask(ctx, tasks[0].ID)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected one schedule, got %d", len(schedules))
	}
}

func containsMemoryScheduleHookID(hooks []models.AgentLifecycleHook, id string) bool {
	for _, hook := range hooks {
		if hook.ID == id {
			return true
		}
	}
	return false
}

func TestMemoryServiceEnsureProjectRepairsLegacyMemoryAgents(t *testing.T) {
	ctx := context.Background()
	svc, repos := newMemoryScheduleTestService(t)
	legacy := &models.Agent{
		Name:                memoryAgentName,
		Description:         "legacy memory curator",
		SystemPrompt:        "legacy prompt",
		Model:               "inherit",
		Tools:               []string{models.AgentToolScopedFiles},
		ToolConfig:          models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: ".openvibely/memory", Permissions: []string{"read", "write"}}}, SkipDefaultTools: true},
		SystemKind:          "memory",
		Key:                 "memory",
		Scope:               models.AgentScopeGlobal,
		SelectableAsPrimary: false,
		Enabled:             true,
		GeneratedStatus:     models.AgentStatusProtected,
		CreatedBy:           models.AgentCreatedBySystem,
	}
	if err := repos.agents.Create(ctx, legacy); err != nil {
		t.Fatalf("create legacy memory agent: %v", err)
	}
	legacyHook := &models.AgentLifecycleHook{
		AgentID:        legacy.ID,
		When:           models.LifecycleBeforeRun,
		SkillKey:       "recall_memory",
		OutputContract: models.OutputContractContextBlock,
		Enabled:        true,
	}
	if err := repos.lifecycles.CreateHook(ctx, legacyHook); err != nil {
		t.Fatalf("create legacy memory hook: %v", err)
	}
	archivedLegacy := &models.Agent{
		Name:                memoryAgentName,
		Description:         "already archived legacy memory curator",
		SystemPrompt:        "archived legacy prompt",
		Model:               "inherit",
		SystemKind:          "memory",
		Key:                 "archived_memory",
		Scope:               models.AgentScopeGlobal,
		SelectableAsPrimary: false,
		Enabled:             true,
		GeneratedStatus:     models.AgentStatusProtected,
		CreatedBy:           models.AgentCreatedBySystem,
	}
	if err := repos.agents.Create(ctx, archivedLegacy); err != nil {
		t.Fatalf("create archived legacy memory agent: %v", err)
	}
	archivedLegacyHook := &models.AgentLifecycleHook{
		AgentID:        archivedLegacy.ID,
		When:           models.LifecycleBeforeRun,
		SkillKey:       "recall_memory",
		OutputContract: models.OutputContractContextBlock,
		Enabled:        true,
	}
	if err := repos.lifecycles.CreateHook(ctx, archivedLegacyHook); err != nil {
		t.Fatalf("create archived legacy memory hook: %v", err)
	}
	if err := repos.agents.MarkArchived(ctx, archivedLegacy.ID, legacy.ID, "preexisting archive"); err != nil {
		t.Fatalf("archive legacy memory agent: %v", err)
	}
	duplicate := &models.Agent{
		Name:                memoryAgentName,
		Description:         "duplicate memory curator",
		SystemPrompt:        "duplicate prompt",
		Model:               "inherit",
		Tools:               []string{models.AgentToolScopedFiles},
		ToolConfig:          models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: ".openvibely/memory", Permissions: []string{"read", "write"}}}, SkipDefaultTools: true},
		SystemKind:          models.AgentSystemKindMemoryCurator,
		Key:                 "memory_curator",
		Scope:               models.AgentScopeGlobal,
		SelectableAsPrimary: false,
		Enabled:             true,
		GeneratedStatus:     models.AgentStatusProtected,
		CreatedBy:           models.AgentCreatedBySystem,
	}
	if err := repos.agents.Create(ctx, duplicate); err != nil {
		t.Fatalf("create duplicate memory curator agent: %v", err)
	}
	consolidator := &models.Agent{
		Name:                "System: Memory Consolidator",
		Description:         "obsolete memory consolidator",
		SystemPrompt:        "legacy consolidator prompt",
		Model:               "inherit",
		SystemKind:          "memory_consolidator",
		Key:                 "memory_consolidator",
		Scope:               models.AgentScopeGlobal,
		SelectableAsPrimary: false,
		Enabled:             true,
		GeneratedStatus:     models.AgentStatusProtected,
		CreatedBy:           models.AgentCreatedBySystem,
	}
	if err := repos.agents.Create(ctx, consolidator); err != nil {
		t.Fatalf("create legacy consolidator agent: %v", err)
	}
	orphanDuplicate := &models.Agent{
		Name:                memoryAgentName,
		Description:         "orphan duplicate memory curator",
		SystemPrompt:        "orphan duplicate prompt",
		Model:               "inherit",
		Scope:               models.AgentScopeGlobal,
		SelectableAsPrimary: false,
		Enabled:             true,
		GeneratedStatus:     models.AgentStatusProtected,
		CreatedBy:           models.AgentCreatedBySystem,
	}
	if err := repos.agents.Create(ctx, orphanDuplicate); err != nil {
		t.Fatalf("create orphan duplicate memory curator agent: %v", err)
	}
	orphanConsolidator := &models.Agent{
		Name:                "System: Memory Consolidator",
		Description:         "orphan obsolete memory consolidator",
		SystemPrompt:        "orphan legacy consolidator prompt",
		Model:               "inherit",
		Scope:               models.AgentScopeGlobal,
		SelectableAsPrimary: false,
		Enabled:             true,
		GeneratedStatus:     models.AgentStatusProtected,
		CreatedBy:           models.AgentCreatedBySystem,
	}
	if err := repos.agents.Create(ctx, orphanConsolidator); err != nil {
		t.Fatalf("create orphan legacy consolidator agent: %v", err)
	}

	if err := svc.EnsureProject(ctx, repos.projectID); err != nil {
		t.Fatalf("ensure project: %v", err)
	}

	canonical, err := repos.agents.GetBySystemKind(ctx, models.AgentSystemKindMemoryCurator)
	if err != nil || canonical == nil {
		t.Fatalf("get canonical memory curator: agent=%#v err=%v", canonical, err)
	}
	if canonical.ID != duplicate.ID {
		t.Fatalf("expected existing memory_curator row to be repaired, got %s want %s", canonical.ID, duplicate.ID)
	}
	if len(canonical.ToolConfig.ScopedFiles) != 1 || canonical.ToolConfig.ScopedFiles[0].Directory != ".openvibely/memories" {
		t.Fatalf("canonical scoped dir was not repaired: %+v", canonical.ToolConfig)
	}
	if !stringSliceContains(canonical.Tools, "memory_view") {
		t.Fatalf("canonical memory curator tools missing memory_view: %+v", canonical.Tools)
	}
	liveCurators, err := repos.agents.ListBySystemKind(ctx, models.AgentSystemKindMemoryCurator)
	if err != nil {
		t.Fatalf("list live curators: %v", err)
	}
	if len(liveCurators) != 1 {
		t.Fatalf("expected one live memory_curator row, got %d", len(liveCurators))
	}
	for _, oldID := range []string{legacy.ID, archivedLegacy.ID, consolidator.ID, orphanDuplicate.ID, orphanConsolidator.ID} {
		old, err := repos.agents.GetByID(ctx, oldID)
		if err != nil {
			t.Fatalf("get old memory agent %s: %v", oldID, err)
		}
		if old != nil {
			t.Fatalf("legacy/duplicate memory agent should be deleted, got %#v", old)
		}
	}
	hooks, err := repos.lifecycles.HooksForWhen(ctx, models.LifecycleBeforeRun)
	if err != nil {
		t.Fatalf("list before-run hooks: %v", err)
	}
	if containsMemoryScheduleHookID(hooks, legacyHook.ID) || containsMemoryScheduleHookID(hooks, archivedLegacyHook.ID) {
		t.Fatalf("legacy memory hooks should be deleted with the agents, got %+v", hooks)
	}
	allAgents, err := repos.agents.ListIncludingArchived(ctx)
	if err != nil {
		t.Fatalf("list live agents: %v", err)
	}
	for _, agent := range allAgents {
		if agent.GeneratedStatus == "archived" {
			continue
		}
		if agent.ID == orphanDuplicate.ID || agent.ID == orphanConsolidator.ID || agent.ID == consolidator.ID || agent.ID == legacy.ID {
			t.Fatalf("legacy/duplicate memory agent should be hidden from live list: %#v", agent)
		}
		if agent.Name == "System: Memory Consolidator" {
			t.Fatalf("memory consolidator should not remain a live agent: %#v", agent)
		}
	}
	task, err := repos.tasks.GetByProjectAndTitle(ctx, repos.projectID, memoryConsolidationTaskTitle)
	if err != nil || task == nil {
		t.Fatalf("get consolidation task: task=%#v err=%v", task, err)
	}
	if task.AgentDefinitionID == nil || *task.AgentDefinitionID != canonical.ID {
		t.Fatalf("consolidation task agent = %v, want %s", task.AgentDefinitionID, canonical.ID)
	}
}

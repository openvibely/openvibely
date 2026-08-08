package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/builtinskills"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAgentLibraryMaintenanceService_ProjectDeclarationsRetainPrecedenceAcrossContextChanges(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	projectAModel := &models.Project{Name: "Project A", RepoPath: t.TempDir()}
	projectBModel := &models.Project{Name: "Project B", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, projectAModel); err != nil {
		t.Fatalf("create project A: %v", err)
	}
	if err := projectRepo.Create(ctx, projectBModel); err != nil {
		t.Fatalf("create project B: %v", err)
	}
	globalRoot := t.TempDir()
	projectA := t.TempDir()
	projectB := t.TempDir()
	writeAgentRootDeclaration(t, globalRoot, "shared", "Global", "global", "")
	writeAgentRootDeclaration(t, projectA, "shared", "Project A", "project", projectAModel.ID)
	writeAgentRootDeclaration(t, projectB, "shared", "Project B", "project", projectBModel.ID)

	svc := &AgentLibraryMaintenanceService{agentRepo: agentRepo, agentsRootPath: globalRoot}
	assertAgent := func(projectRoot, wantName, wantProjectID string) {
		t.Helper()
		if err := svc.SyncRootDeclarations(ctx, projectRoot); err != nil {
			t.Fatalf("sync %s: %v", wantName, err)
		}
		agent, err := agentRepo.GetByKey(ctx, "shared")
		if err != nil || agent == nil {
			t.Fatalf("get shared after %s: err=%v agent=%#v", wantName, err, agent)
		}
		if agent.Name != wantName || agent.Scope != models.AgentScopeProject || agent.ProjectID != wantProjectID {
			t.Fatalf("shared declaration precedence after %s: %#v", wantName, agent)
		}
	}

	assertAgent(projectA, "Project A", projectAModel.ID)
	assertAgent(projectB, "Project B", projectBModel.ID)
	readsAfterBothProjects := svc.DeclarationSyncMetrics()
	assertAgent(projectA, "Project A", projectAModel.ID)
	if got := svc.DeclarationSyncMetrics(); got != readsAfterBothProjects {
		t.Fatalf("project switch reread unchanged declarations: before=%#v after=%#v", readsAfterBothProjects, got)
	}

	writeAgentRootDeclaration(t, globalRoot, "shared", "Changed Global", "global", "")
	assertAgent(projectA, "Project A", projectAModel.ID)
	if got := svc.DeclarationSyncMetrics(); got.ContentReads != readsAfterBothProjects.ContentReads+1 || got.Parses != readsAfterBothProjects.Parses+1 {
		t.Fatalf("global change should read/parse only the changed global declaration: before=%#v after=%#v", readsAfterBothProjects, got)
	}
}

func TestAgentLibraryMaintenanceService_ProjectDeclarationRemovalRestoresGlobalPrecedence(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Project", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	globalRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeAgentRootDeclaration(t, globalRoot, "shared", "Global", "global", "")
	writeAgentRootDeclaration(t, projectRoot, "shared", "Project", "project", project.ID)

	svc := &AgentLibraryMaintenanceService{agentRepo: agentRepo, agentsRootPath: globalRoot}
	if err := svc.SyncRootDeclarations(ctx, projectRoot); err != nil {
		t.Fatalf("cold sync: %v", err)
	}
	cold := svc.DeclarationSyncMetrics()
	if err := os.RemoveAll(filepath.Join(projectRoot, "agents")); err != nil {
		t.Fatalf("remove project agents directory: %v", err)
	}
	if err := svc.SyncRootDeclarations(ctx, projectRoot); err != nil {
		t.Fatalf("sync after project declaration removal: %v", err)
	}
	if got := svc.DeclarationSyncMetrics(); got != cold {
		t.Fatalf("removal reread unchanged global declaration: before=%#v after=%#v", cold, got)
	}
	assertAgentDeclarationState(t, ctx, agentRepo, "shared", "Global", models.AgentScopeGlobal, "")
}

func TestAgentLibraryMaintenanceService_ProjectDeclarationRekeyRestoresGlobalPrecedence(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Project", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	globalRoot := t.TempDir()
	projectRoot := t.TempDir()
	writeAgentRootDeclaration(t, globalRoot, "shared", "Global", "global", "")
	writeAgentRootDeclaration(t, projectRoot, "shared", "Project", "project", project.ID)

	svc := &AgentLibraryMaintenanceService{agentRepo: agentRepo, agentsRootPath: globalRoot}
	if err := svc.SyncRootDeclarations(ctx, projectRoot); err != nil {
		t.Fatalf("cold sync: %v", err)
	}
	cold := svc.DeclarationSyncMetrics()
	writeAgentRootDeclarationAt(t, projectRoot, "shared", "project_other", "Project Other", "project", project.ID)
	if err := svc.SyncRootDeclarations(ctx, projectRoot); err != nil {
		t.Fatalf("sync after project declaration re-key: %v", err)
	}
	if got := svc.DeclarationSyncMetrics(); got.ContentReads != cold.ContentReads+1 || got.Parses != cold.Parses+1 {
		t.Fatalf("re-key should read/parse only the changed project declaration: before=%#v after=%#v", cold, got)
	}
	assertAgentDeclarationState(t, ctx, agentRepo, "shared", "Global", models.AgentScopeGlobal, "")
	assertAgentDeclarationState(t, ctx, agentRepo, "project_other", "Project Other", models.AgentScopeProject, project.ID)
}

func assertAgentDeclarationState(t *testing.T, ctx context.Context, agentRepo *repository.AgentRepo, key, name string, scope models.AgentScope, projectID string) {
	t.Helper()
	agent, err := agentRepo.GetByKey(ctx, key)
	if err != nil || agent == nil {
		t.Fatalf("get agent %s: err=%v agent=%#v", key, err, agent)
	}
	if agent.Name != name || agent.Scope != scope || agent.ProjectID != projectID {
		t.Fatalf("agent %s state: got name=%q scope=%q project_id=%q, want name=%q scope=%q project_id=%q", key, agent.Name, agent.Scope, agent.ProjectID, name, scope, projectID)
	}
}

func TestAgentLibraryMaintenanceService_ProtectedRepairUsesCachedDeclarations(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	root := t.TempDir()
	if err := builtinskills.SyncTo(root); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}
	svc := &AgentLibraryMaintenanceService{agentRepo: agentRepo, lifecycleRepo: lifecycleRepo, agentsRootPath: root}
	if err := svc.SyncRootDeclarations(ctx, ""); err != nil {
		t.Fatalf("cold sync: %v", err)
	}
	cold := svc.DeclarationSyncMetrics()
	if cold.ContentReads == 0 || cold.Parses == 0 {
		t.Fatalf("protected declarations were not instrumented: %#v", cold)
	}

	goal, err := agentRepo.GetBySystemKind(ctx, models.AgentSystemKindGoal)
	if err != nil || goal == nil {
		t.Fatalf("get goal: err=%v agent=%#v", err, goal)
	}
	goal.Name = "Broken Goal"
	if err := agentRepo.Update(ctx, goal); err != nil {
		t.Fatalf("corrupt goal: %v", err)
	}
	if err := svc.SyncRootDeclarations(ctx, ""); err != nil {
		t.Fatalf("warm repair sync: %v", err)
	}
	if warm := svc.DeclarationSyncMetrics(); warm != cold {
		t.Fatalf("warm protected repair reread/reparsed unchanged declarations: cold=%#v warm=%#v", cold, warm)
	}
	repaired, err := agentRepo.GetBySystemKind(ctx, models.AgentSystemKindGoal)
	if err != nil || repaired == nil || repaired.Name == "Broken Goal" {
		t.Fatalf("protected repair did not use cached declaration: err=%v agent=%#v", err, repaired)
	}
}

func writeAgentRootDeclaration(t *testing.T, root, key, name, scope, projectID string) {
	t.Helper()
	writeAgentRootDeclarationAt(t, root, key, key, name, scope, projectID)
}

func writeAgentRootDeclarationAt(t *testing.T, root, directoryKey, declarationKey, name, scope, projectID string) {
	t.Helper()
	dir := filepath.Join(root, "agents", directoryKey)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir declaration: %v", err)
	}
	content := "---\nkind: openvibely.agent_skill\nversion: 1\nagent:\n  key: " + declarationKey + "\n  name: " + name + "\n  scope: " + scope + "\n  project_id: " + projectID + "\n  selectable_as_primary: true\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILLS.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write declaration: %v", err)
	}
}

func TestAgentLibraryMaintenanceService_SyncRootDeclarationsWithoutLifecycleRepo(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	root := t.TempDir()
	dir := filepath.Join(root, "agents", "hooked")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir declaration: %v", err)
	}
	declaration := `---
kind: openvibely.agent_skill
version: 1
agent:
  key: hooked
  name: Hooked
  scope: global
  selectable_as_primary: true
lifecycle_hooks:
  after_complete:
    skill: validate_change
---
# Hooked
`
	if err := os.WriteFile(filepath.Join(dir, "SKILLS.md"), []byte(declaration), 0o644); err != nil {
		t.Fatalf("write declaration: %v", err)
	}

	svc := &AgentLibraryMaintenanceService{agentRepo: agentRepo, agentsRootPath: root}
	if err := svc.SyncRootDeclarations(ctx, ""); err != nil {
		t.Fatalf("sync without lifecycle repo: %v", err)
	}
	agent, err := agentRepo.GetByKey(ctx, "hooked")
	if err != nil || agent == nil {
		t.Fatalf("declaration was not applied without lifecycle repo: err=%v agent=%#v", err, agent)
	}
}

func TestAgentLibraryMaintenanceService_WarmSyncSkipsUnchangedDeclarationContent(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	root := t.TempDir()
	writeDeclaration := func(key, name string) {
		t.Helper()
		dir := filepath.Join(root, "agents", key)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir declaration: %v", err)
		}
		content := "---\nkind: openvibely.agent_skill\nversion: 1\nagent:\n  key: " + key + "\n  name: " + name + "\n  scope: global\n  selectable_as_primary: true\nlifecycle_hooks:\n  after_complete:\n    skill: validate_change\n    output_contract: activity_summary\n---\n# " + name + "\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILLS.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write declaration: %v", err)
		}
	}
	writeDeclaration("first", "First")
	svc := &AgentLibraryMaintenanceService{agentRepo: agentRepo, lifecycleRepo: lifecycleRepo, agentsRootPath: root}

	if err := svc.SyncRootDeclarations(ctx, ""); err != nil {
		t.Fatalf("cold SyncRootDeclarations: %v", err)
	}
	cold := svc.DeclarationSyncMetrics()
	if cold.ContentReads != 3 || cold.Parses != 3 {
		t.Fatalf("expected cold reads/parses for one ordinary and two protected declarations, got %#v", cold)
	}
	if _, err := db.Exec(`
		CREATE TEMP TABLE declaration_write_counts (kind TEXT NOT NULL);
		CREATE TEMP TRIGGER count_agent_updates AFTER UPDATE ON agents BEGIN
			INSERT INTO declaration_write_counts(kind) VALUES ('agent_update');
		END;
		CREATE TEMP TRIGGER count_hook_inserts AFTER INSERT ON agent_lifecycle_hooks BEGIN
			INSERT INTO declaration_write_counts(kind) VALUES ('hook_insert');
		END;
		CREATE TEMP TRIGGER count_hook_updates AFTER UPDATE ON agent_lifecycle_hooks BEGIN
			INSERT INTO declaration_write_counts(kind) VALUES ('hook_update');
		END;
		CREATE TEMP TRIGGER count_hook_deletes AFTER DELETE ON agent_lifecycle_hooks BEGIN
			INSERT INTO declaration_write_counts(kind) VALUES ('hook_delete');
		END;
	`); err != nil {
		t.Fatalf("install declaration write instrumentation: %v", err)
	}
	if err := svc.SyncRootDeclarations(ctx, ""); err != nil {
		t.Fatalf("warm SyncRootDeclarations: %v", err)
	}
	warm := svc.DeclarationSyncMetrics()
	if warm != cold {
		t.Fatalf("unchanged warm sync performed content I/O: cold=%#v warm=%#v", cold, warm)
	}
	var writes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM declaration_write_counts`).Scan(&writes); err != nil {
		t.Fatalf("count declaration writes: %v", err)
	}
	if writes != 0 {
		t.Fatalf("unchanged warm sync performed %d agent or lifecycle-hook writes", writes)
	}

	declarationPath := filepath.Join(root, "agents", "first", "SKILLS.md")
	info, err := os.Stat(declarationPath)
	if err != nil {
		t.Fatalf("stat unchanged declaration: %v", err)
	}
	writeDeclaration("first", "Other")
	if err := os.Chtimes(declarationPath, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("restore declaration mtime: %v", err)
	}
	if err := svc.SyncRootDeclarations(ctx, ""); err != nil {
		t.Fatalf("changed SyncRootDeclarations: %v", err)
	}
	changed := svc.DeclarationSyncMetrics()
	if changed.ContentReads != warm.ContentReads+1 || changed.Parses != warm.Parses+1 {
		t.Fatalf("changed declaration not read and parsed once: warm=%#v changed=%#v", warm, changed)
	}
	agent, err := agentRepo.GetByKey(ctx, "first")
	if err != nil || agent == nil || agent.Name != "Other" {
		t.Fatalf("changed declaration not applied: err=%v agent=%#v", err, agent)
	}

	writeDeclaration("second", "Second")
	if err := svc.SyncRootDeclarations(ctx, ""); err != nil {
		t.Fatalf("added SyncRootDeclarations: %v", err)
	}
	added := svc.DeclarationSyncMetrics()
	if added.ContentReads != changed.ContentReads+1 || added.Parses != changed.Parses+1 {
		t.Fatalf("added declaration not read and parsed once: changed=%#v added=%#v", changed, added)
	}
	second, err := agentRepo.GetByKey(ctx, "second")
	if err != nil || second == nil {
		t.Fatalf("added declaration not materialized: err=%v agent=%#v", err, second)
	}
}

func TestAgentLibraryMaintenanceService_SyncRootDeclarationsAppliesAgentMetadata(t *testing.T) {
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	root := t.TempDir()
	agentDir := filepath.Join(root, "agents", "backend")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := `---
kind: openvibely.agent_skill
version: 1
agent:
  key: backend
  name: Backend
  description: Backend work
  scope: project
  selectable_as_primary: true
tools:
  - skill_view
permissions:
  read_task_prompt: true
lifecycle_hooks:
  after_complete:
    skill: learn_backend
    output_contract: learning_summary
---
# Backend Skills

## backend/learn_backend

[Learn Backend](skills/learn_backend/SKILL.md) — learns from backend work.
`
	if err := os.WriteFile(filepath.Join(agentDir, "SKILLS.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILLS.md: %v", err)
	}
	svc := &AgentLibraryMaintenanceService{agentRepo: agentRepo, lifecycleRepo: lifecycleRepo, agentsRootPath: root}
	if err := svc.SyncRootDeclarations(context.Background(), ""); err != nil {
		t.Fatalf("SyncRootDeclarations: %v", err)
	}
	agent, err := agentRepo.GetByKey(context.Background(), "backend")
	if err != nil || agent == nil {
		t.Fatalf("GetByKey: %v agent=%#v", err, agent)
	}
	if agent.Name != "Backend" || !agent.SelectableAsPrimary || len(agent.Tools) != 1 || agent.Tools[0] != "skill_view" || !agent.PermissionDefaults.ReadTaskPrompt {
		t.Fatalf("metadata not applied from root SKILLS.md: %#v", agent)
	}
	if !strings.Contains(agent.SystemPrompt, "# Backend Skills") {
		t.Fatalf("expected markdown body used as prompt/overview, got %q", agent.SystemPrompt)
	}
	hooks, err := lifecycleRepo.HooksByAgent(context.Background(), agent.ID)
	if err != nil {
		t.Fatalf("HooksByAgent: %v", err)
	}
	if len(hooks) != 1 || hooks[0].SkillKey != "learn_backend" || hooks[0].OutputContract != models.OutputContractLearningSummary {
		t.Fatalf("hook not applied from root SKILLS.md: %#v", hooks)
	}
}

func TestAgentLibraryMaintenanceService_SyncRootDeclarationsSkipsProtectedSystemAgents(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	root := t.TempDir()
	if err := builtinskills.SyncTo(root); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}

	svc := &AgentLibraryMaintenanceService{agentRepo: agentRepo, lifecycleRepo: lifecycleRepo, agentsRootPath: root}
	if err := svc.SyncRootDeclarations(ctx, ""); err != nil {
		t.Fatalf("SyncRootDeclarations: %v", err)
	}
	memoryAgent, err := agentRepo.GetByKey(ctx, "memory_curator")
	if err != nil {
		t.Fatalf("GetByKey(memory): %v", err)
	}
	if memoryAgent != nil {
		t.Fatalf("generic root sync must skip protected Memory Curator, got %#v", memoryAgent)
	}
	goalAgent, err := agentRepo.GetByKey(ctx, models.AgentSystemKindGoal)
	if err != nil || goalAgent == nil {
		t.Fatalf("expected seeded Goal Agent to remain available: %v %#v", err, goalAgent)
	}
	if goalAgent.SystemKind != models.AgentSystemKindGoal || goalAgent.GeneratedStatus != models.AgentStatusProtected || goalAgent.CreatedBy != models.AgentCreatedBySystem {
		t.Fatalf("generic root sync must not rewrite Goal Agent through importer path: %#v", goalAgent)
	}
	skillCurator, err := agentRepo.GetByKey(ctx, "skill_curator")
	if err != nil || skillCurator == nil {
		t.Fatalf("expected seeded Skill Curator to remain available: %v %#v", err, skillCurator)
	}
	if skillCurator.SystemKind != models.AgentSystemKindSkillCurator || skillCurator.GeneratedStatus != models.AgentStatusProtected {
		t.Fatalf("generic root sync must not rewrite Skill Curator through importer path: %#v", skillCurator)
	}
}

func TestAgentLibraryMaintenanceService_SyncRootDeclarationsSeedsProtectedGoalAgent(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	agentsRoot := t.TempDir()
	if err := builtinskills.SyncTo(agentsRoot); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}
	svc := &AgentLibraryMaintenanceService{agentRepo: agentRepo, lifecycleRepo: lifecycleRepo, agentsRootPath: agentsRoot}
	if err := svc.SyncRootDeclarations(ctx, ""); err != nil {
		t.Fatalf("SyncRootDeclarations: %v", err)
	}
	agent, err := agentRepo.GetBySystemKind(ctx, models.AgentSystemKindGoal)
	if err != nil || agent == nil {
		t.Fatalf("GetBySystemKind(goal): %v %#v", err, agent)
	}
	if agent.Key != models.AgentSystemKindGoal || agent.GeneratedStatus != models.AgentStatusProtected || agent.CreatedBy != models.AgentCreatedBySystem || agent.SelectableAsPrimary {
		t.Fatalf("expected protected non-selectable Goal Agent, got %#v", agent)
	}
	for _, want := range []string{"get_task_goal", "mark_task_goal_achieved", "report_task_goal_blocked", "send_to_task"} {
		if !AgentAllowsTool(agent, want) {
			t.Fatalf("expected Goal Agent to grant %s, tools=%v", want, agent.Tools)
		}
	}
	if len(agent.SourceRefs) != 1 || agent.SourceRefs[0] != bundledGoalAgentDeclarationPath {
		t.Fatalf("expected goal source ref, got %#v", agent.SourceRefs)
	}
	hooks, err := lifecycleRepo.HooksByAgent(ctx, agent.ID)
	if err != nil {
		t.Fatalf("HooksByAgent: %v", err)
	}
	if len(hooks) != 1 || hooks[0].When != models.LifecycleAfterComplete || hooks[0].SkillKey != "evaluate_task_goal" || hooks[0].OutputContract != models.OutputContractActivitySummary || !hooks[0].Blocking || !hooks[0].Enabled {
		t.Fatalf("unexpected Goal Agent hook: %#v", hooks)
	}
	if !strings.Contains(hooks[0].RunPolicyJSON, `"always"`) {
		t.Fatalf("Goal Agent hook must run after any task turn with an active goal, got run policy %s", hooks[0].RunPolicyJSON)
	}
}

func TestAgentLibraryMaintenanceService_SyncRootDeclarationsRepairsLegacyGoalAgentByKey(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	agentsRoot := t.TempDir()
	if err := builtinskills.SyncTo(agentsRoot); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}

	legacy := &models.Agent{
		Name:                "System: Goal Agent",
		Key:                 models.AgentSystemKindGoal,
		SystemKind:          "",
		Tools:               []string{"get_task_goal", "send_to_task"},
		Scope:               models.AgentScopeGlobal,
		SelectableAsPrimary: false,
		Enabled:             true,
		CreatedBy:           models.AgentCreatedByAgent,
		GeneratedStatus:     models.AgentStatusGenerated,
	}
	if err := agentRepo.Create(ctx, legacy); err != nil {
		t.Fatalf("Create legacy Goal Agent: %v", err)
	}

	svc := &AgentLibraryMaintenanceService{agentRepo: agentRepo, lifecycleRepo: lifecycleRepo, agentsRootPath: agentsRoot}
	if err := svc.SyncRootDeclarations(ctx, ""); err != nil {
		t.Fatalf("SyncRootDeclarations: %v", err)
	}

	agent, err := agentRepo.GetBySystemKind(ctx, models.AgentSystemKindGoal)
	if err != nil || agent == nil {
		t.Fatalf("GetBySystemKind(goal): %v %#v", err, agent)
	}
	if agent.ID != legacy.ID {
		t.Fatalf("expected legacy Goal Agent repaired in place, got id=%s want=%s", agent.ID, legacy.ID)
	}
	if agent.Key != models.AgentSystemKindGoal || agent.SystemKind != models.AgentSystemKindGoal || agent.GeneratedStatus != models.AgentStatusProtected || agent.CreatedBy != models.AgentCreatedBySystem || agent.SelectableAsPrimary {
		t.Fatalf("expected protected repaired Goal Agent, got %#v", agent)
	}
	for _, want := range []string{"get_task_goal", "mark_task_goal_achieved", "report_task_goal_blocked", "send_to_task"} {
		if !AgentAllowsTool(agent, want) {
			t.Fatalf("expected repaired Goal Agent to grant %s, tools=%v", want, agent.Tools)
		}
	}
	hooks, err := lifecycleRepo.HooksByAgent(ctx, agent.ID)
	if err != nil {
		t.Fatalf("HooksByAgent: %v", err)
	}
	if len(hooks) != 1 || hooks[0].When != models.LifecycleAfterComplete || hooks[0].SkillKey != "evaluate_task_goal" || hooks[0].OutputContract != models.OutputContractActivitySummary || !hooks[0].Blocking || !hooks[0].Enabled {
		t.Fatalf("unexpected repaired Goal Agent hook: %#v", hooks)
	}
	if !strings.Contains(hooks[0].RunPolicyJSON, `"always"`) {
		t.Fatalf("repaired Goal Agent hook must run after any task turn with an active goal, got run policy %s", hooks[0].RunPolicyJSON)
	}
}

func TestAgentLibraryMaintenanceService_EnsureProjectCreatesVisibleScheduledTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	agentsRoot := t.TempDir()
	if err := builtinskills.SyncTo(agentsRoot); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}
	svc := NewAgentLibraryMaintenanceService(taskRepo, scheduleRepo, agentRepo)
	svc.SetLifecycleRepo(lifecycleRepo)
	svc.SetAgentsRootPath(agentsRoot)

	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "agent-library-maintenance", Description: "test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	projectID := project.ID
	if err := svc.EnsureProject(ctx, projectID); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	agent, err := agentRepo.GetBySystemKind(ctx, models.AgentSystemKindSkillCurator)
	if err != nil {
		t.Fatalf("GetBySystemKind: %v", err)
	}
	if agent == nil {
		t.Fatal("expected skill curator system agent")
	}
	if agent.GeneratedStatus != models.AgentStatusProtected || agent.SelectableAsPrimary {
		t.Fatalf("expected protected non-selectable agent, got status=%q selectable=%v", agent.GeneratedStatus, agent.SelectableAsPrimary)
	}
	for _, want := range []string{"skill_view", "skills_list", "agent_list", "agent_view", "skill_manage", "skill_import", "agent_skill_manage"} {
		if !AgentAllowsTool(agent, want) {
			t.Fatalf("expected Skill Curator to grant %s, tools=%v", want, agent.Tools)
		}
	}
	if AgentAllowsTool(agent, models.AgentToolScopedFiles) || len(agent.ToolConfig.ScopedFiles) != 0 || agent.ToolConfig.SkipDefaultTools || agent.ToolConfig.DisableRuntimeWorktree {
		t.Fatalf("Skill Curator must not get scoped-file bypasses for agent roots, tools=%v config=%#v", agent.Tools, agent.ToolConfig)
	}
	if !agent.PermissionDefaults.ReadAgents || agent.PermissionDefaults.WriteAgents || !agent.PermissionDefaults.ReadSkills || !agent.PermissionDefaults.WriteSkills || agent.PermissionDefaults.ReadRepositoryFiles || agent.PermissionDefaults.WriteRepositoryFiles {
		t.Fatalf("expected skill-only declaration-backed permission defaults, got %#v", agent.PermissionDefaults)
	}
	if strings.Contains(agent.SystemPrompt, "system_prompt:") {
		t.Fatalf("agent prompt should come from SKILLS.md markdown body, not YAML system_prompt: %q", agent.SystemPrompt)
	}
	for _, want := range []string{"route_task", "observe_task_for_learning", "maintain_skill_library"} {
		found := false
		for _, skill := range agent.Skills {
			if skill.Name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected Skill Curator skill %q loaded from root SKILLS.md index, got %#v", want, agent.Skills)
		}
	}
	if len(agent.SourceRefs) != 1 || agent.SourceRefs[0] != bundledSkillCuratorDeclarationPath {
		t.Fatalf("expected declaration source ref, got %#v", agent.SourceRefs)
	}
	hooks, err := lifecycleRepo.HooksByAgent(ctx, agent.ID)
	if err != nil {
		t.Fatalf("HooksByAgent: %v", err)
	}
	if len(hooks) != 2 {
		t.Fatalf("expected two declaration-backed lifecycle hooks, got %d: %#v", len(hooks), hooks)
	}
	have := map[string]models.AgentLifecycleHook{}
	for _, hook := range hooks {
		have[string(hook.When)+"/"+hook.SkillKey] = hook
	}
	if hook, ok := have["route_task/route_task"]; !ok || hook.OutputContract != models.OutputContractSelectedSkills || hook.Blocking || !hook.Enabled {
		t.Fatalf("bad route_task hook: %#v", hook)
	}
	if hook, ok := have["after_complete/observe_task_for_learning"]; !ok || hook.OutputContract != models.OutputContractLearningSummary || hook.Blocking || !hook.Enabled {
		t.Fatalf("bad observe hook: %#v", hook)
	}
	staleRoute := have["route_task/route_task"]
	staleRoute.Blocking = true
	if err := lifecycleRepo.UpdateHook(ctx, &staleRoute); err != nil {
		t.Fatalf("make route hook stale blocking: %v", err)
	}
	if err := svc.EnsureProject(ctx, projectID); err != nil {
		t.Fatalf("EnsureProject repairs route blocking: %v", err)
	}
	repairedHooks, err := lifecycleRepo.HooksByAgent(ctx, agent.ID)
	if err != nil {
		t.Fatalf("HooksByAgent after repair: %v", err)
	}
	for _, hook := range repairedHooks {
		if hook.When == models.LifecycleRouteTask && hook.SkillKey == "route_task" && hook.Blocking {
			t.Fatalf("Skill Curator route hook should repair to non-blocking: %#v", hook)
		}
	}
	task, err := taskRepo.GetByProjectAndTitle(ctx, projectID, agentLibraryMaintenanceTaskTitle)
	if err != nil {
		t.Fatalf("GetByProjectAndTitle: %v", err)
	}
	if task == nil {
		t.Fatal("expected scheduled maintenance task")
	}
	if task.Category != models.CategoryScheduled || task.AgentDefinitionID == nil || *task.AgentDefinitionID != agent.ID {
		t.Fatalf("unexpected task category/agent: category=%q agent=%v want=%s", task.Category, task.AgentDefinitionID, agent.ID)
	}
	for _, forbidden := range []string{"OpenVibely skill library", "built-in system agent", "Built-in system agent", "non-system agent", "generated skill", "generated skills"} {
		if strings.Contains(task.Prompt, forbidden) {
			t.Fatalf("scheduled skill maintenance prompt should use neutral wording; found %q in: %q", forbidden, task.Prompt)
		}
	}
	for _, want := range []string{"this project's skill library", "Do not create, edit, archive, route, or reassign agents", "Do not change project memory"} {
		if !strings.Contains(task.Prompt, want) {
			t.Fatalf("scheduled skill maintenance prompt missing %q: %q", want, task.Prompt)
		}
	}
	schedules, err := scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected one normal schedule row, got %d", len(schedules))
	}
	if schedules[0].RepeatType != models.RepeatDaily || !schedules[0].Enabled {
		t.Fatalf("unexpected schedule: repeat=%q enabled=%v", schedules[0].RepeatType, schedules[0].Enabled)
	}
	if !schedules[0].ClearContextOnStart {
		t.Fatal("expected skill library maintenance schedule to clear context on start")
	}
	originalRunAt := schedules[0].RunAt
	originalNextRun := schedules[0].NextRun
	if err := scheduleRepo.UpdateClearContextOnStart(ctx, schedules[0].ID, task.ID, false); err != nil {
		t.Fatalf("make schedule stale clear-context policy: %v", err)
	}

	if err := svc.EnsureProject(ctx, projectID); err != nil {
		t.Fatalf("EnsureProject repairs stale schedule context policy: %v", err)
	}
	schedules, err = scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask repaired: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected idempotent schedule creation, got %d schedules", len(schedules))
	}
	if !schedules[0].ClearContextOnStart {
		t.Fatal("expected stale skill library maintenance schedule clear-context flag to be repaired")
	}
	if !schedules[0].RunAt.Equal(originalRunAt) {
		t.Fatalf("repair changed run_at: got %s want %s", schedules[0].RunAt, originalRunAt)
	}
	if originalNextRun == nil || schedules[0].NextRun == nil || !schedules[0].NextRun.Equal(*originalNextRun) {
		t.Fatalf("repair changed next_run: got %v want %v", schedules[0].NextRun, originalNextRun)
	}

	if err := svc.EnsureProject(ctx, projectID); err != nil {
		t.Fatalf("EnsureProject second: %v", err)
	}
	schedules, err = scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask second: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected idempotent schedule creation, got %d schedules", len(schedules))
	}
	hooks, err = lifecycleRepo.HooksByAgent(ctx, agent.ID)
	if err != nil {
		t.Fatalf("HooksByAgent second: %v", err)
	}
	if len(hooks) != 2 {
		t.Fatalf("expected idempotent hook repair, got %d hooks", len(hooks))
	}
}
func TestAgentLibraryMaintenanceService_EnsureProjectSanitizesLegacySystemDeclarationGrants(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	agentsRoot := t.TempDir()
	if err := builtinskills.SyncTo(agentsRoot); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}
	declPath := filepath.Join(agentsRoot, bundledSkillCuratorDeclarationPath)
	legacy, err := os.ReadFile(declPath)
	if err != nil {
		t.Fatalf("read declaration: %v", err)
	}
	legacyText := strings.Replace(string(legacy), "  - skill_view", "  - ScopedFiles\n  - agent_manage\n  - skill_view", 1)
	legacyText = strings.Replace(legacyText, "permissions:\n  read_task_prompt: true", "tool_config:\n  scoped_files:\n    - directory: .openvibely/agents\n      permissions:\n        - read\n        - write\n        - delete\n  skip_default_tools: true\n  disable_runtime_worktree: true\npermissions:\n  read_task_prompt: true", 1)
	legacyText = strings.Replace(legacyText, "  write_skills: true", "  write_skills: true\n  write_agents: true\n  read_repository_files: true\n  write_repository_files: true", 1)
	if err := os.WriteFile(declPath, []byte(legacyText), 0o644); err != nil {
		t.Fatalf("write legacy declaration: %v", err)
	}
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "legacy-system-declaration", Description: "test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	svc := NewAgentLibraryMaintenanceService(taskRepo, scheduleRepo, agentRepo)
	svc.SetLifecycleRepo(lifecycleRepo)
	svc.SetAgentsRootPath(agentsRoot)
	if err := svc.EnsureProject(ctx, project.ID); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	agent, err := agentRepo.GetBySystemKind(ctx, models.AgentSystemKindSkillCurator)
	if err != nil || agent == nil {
		t.Fatalf("load system agent: %v %#v", err, agent)
	}
	for _, denied := range []string{models.AgentToolScopedFiles, "agent_manage"} {
		if AgentAllowsTool(agent, denied) {
			t.Fatalf("sanitized system agent must not grant %s, tools=%v", denied, agent.Tools)
		}
	}
	if len(agent.ToolConfig.ScopedFiles) != 0 || agent.ToolConfig.SkipDefaultTools || agent.ToolConfig.DisableRuntimeWorktree {
		t.Fatalf("sanitized system agent must not keep scoped files config: %#v", agent.ToolConfig)
	}
	if agent.PermissionDefaults.WriteAgents || agent.PermissionDefaults.ReadRepositoryFiles || agent.PermissionDefaults.WriteRepositoryFiles {
		t.Fatalf("sanitized system agent must not keep agent/repo write permissions: %#v", agent.PermissionDefaults)
	}
}

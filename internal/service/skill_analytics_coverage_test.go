package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/agentskills"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestSkillAnalyticsInstrumentationRecordsSelectionViewAndEditEvents(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := repository.NewSkillAnalyticsRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	agentRepo := repository.NewAgentRepo(db)
	project := &models.Project{Name: "Skill Analytics Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agent := &models.Agent{Name: "Skill Agent", Key: "skill-agent", Scope: models.AgentScopeProject}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Skill analytics task", Category: models.CategoryScheduled, Status: models.StatusPending}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	catalog := agentskills.NewCatalog("turn-1", []agentskills.Entry{
		{Handle: "global_skill", Skill: "global_skill", Source: agentskills.SourceGlobal},
		{Handle: "project_skill", Skill: "project_skill", Source: agentskills.SourceProject},
		{Handle: "agent_skill", Skill: "agent_skill", Source: agentskills.SourceAgent, AgentKey: "agent-key"},
	})

	worker := &WorkerService{}
	worker.SetSkillAnalyticsRepo(repo)
	turn := lifecycleTurnContext{TaskRunID: "run-1", AssignedAgent: agent}
	worker.recordSelectedSkillEvents(ctx, *task, catalog,
		[]string{"global_skill", "project_skill", "agent_skill", "project_skill", "missing", " "},
		agentskills.SkillSelectionProvenance{"project_skill": agentskills.ProvenanceAlwaysUse}, turn)

	baseRuntime := &llmcontracts.RuntimeTools{Executor: func(_ context.Context, name string, _ json.RawMessage) (string, bool, bool, error) {
		return "ok:" + name, true, false, nil
	}}
	wrapped := worker.instrumentSkillRuntimeTools(baseRuntime, catalog, skillAnalyticsContext{
		ProjectID: project.ID, TaskID: task.ID, ThreadID: "run-1", AgentID: agent.ID,
	})
	if out, handled, isErr, err := wrapped.Executor(ctx, " skill_view ", json.RawMessage(`{"handle":"project_skill"}`)); out != "ok: skill_view " || !handled || isErr || err != nil {
		t.Fatalf("wrapped skill_view output=%q handled=%v isErr=%v err=%v", out, handled, isErr, err)
	}
	if _, _, _, err := wrapped.Executor(ctx, "skill_view", json.RawMessage(`{"handle":"agent:agent-key/agent_skill","file_path":"references/details.md"}`)); err != nil {
		t.Fatalf("wrapped skill_view with file path: %v", err)
	}
	if _, _, _, err := wrapped.Executor(ctx, "bash", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("wrapped non-skill tool: %v", err)
	}

	editRuntime := instrumentSkillEditRuntimeTools(repo, baseRuntime, skillAnalyticsContext{ProjectID: project.ID, TaskID: task.ID, ThreadID: task.ID})
	for name, input := range map[string]json.RawMessage{
		"skill_manage":       json.RawMessage(`{"action":"create","scope":"project","declaration":"---\nkey: created_skill\n---"}`),
		"skill_import":       json.RawMessage(`{"action":"update","handle":"imported_skill"}`),
		"agent_skill_manage": json.RawMessage(`{"action":"create","handle":"agent_created_skill"}`),
	} {
		if _, _, _, err := editRuntime.Executor(ctx, name, input); err != nil {
			t.Fatalf("edit runtime %s: %v", name, err)
		}
	}

	metrics, err := repo.GetTopSkills(ctx, repository.SkillAnalyticsFilter{ProjectID: project.ID, Limit: 10})
	if err != nil {
		t.Fatalf("GetTopSkills: %v", err)
	}
	byHandle := map[string]models.SkillAnalyticsSkillMetric{}
	for _, metric := range metrics {
		byHandle[metric.SkillHandle] = metric
	}
	if byHandle["global_skill"].SelectedCount != 1 || byHandle["global_skill"].SkillScope != models.SkillScopeGlobal {
		t.Fatalf("global_skill metric = %#v", byHandle["global_skill"])
	}
	if byHandle["project_skill"].SelectedCount != 1 || byHandle["project_skill"].ViewedCount != 1 || byHandle["project_skill"].LoadedCount != 1 || byHandle["project_skill"].SkillScope != models.SkillScopeProject {
		t.Fatalf("project_skill metric = %#v", byHandle["project_skill"])
	}
	if byHandle["agent_skill"].SelectedCount != 1 || byHandle["agent_skill"].ViewedCount != 1 || byHandle["agent_skill"].LoadedCount != 0 || byHandle["agent_skill"].SkillScope != models.SkillScopeAgentOwned {
		t.Fatalf("agent_skill metric = %#v", byHandle["agent_skill"])
	}
	if byHandle["created_skill"].CreatedCount != 1 || byHandle["created_skill"].SkillScope != models.SkillScopeProject {
		t.Fatalf("created_skill metric = %#v", byHandle["created_skill"])
	}
	if byHandle["imported_skill"].EditedCount != 1 || byHandle["imported_skill"].SkillScope != models.SkillScopeGlobal {
		t.Fatalf("imported_skill metric = %#v", byHandle["imported_skill"])
	}
	if byHandle["agent_created_skill"].CreatedCount != 1 || byHandle["agent_created_skill"].SkillScope != models.SkillScopeAgentOwned {
		t.Fatalf("agent_created_skill metric = %#v", byHandle["agent_created_skill"])
	}

	followThrough, err := repo.GetSelectionFollowThrough(ctx, repository.SkillAnalyticsFilter{ProjectID: project.ID, Limit: 10})
	if err != nil {
		t.Fatalf("GetSelectionFollowThrough: %v", err)
	}
	for _, metric := range followThrough {
		if metric.SkillHandle == "project_skill" && (metric.SelectedCount != 1 || metric.LoadedOrViewed != 1 || metric.IgnoredCount != 0) {
			t.Fatalf("project_skill follow-through = %#v", metric)
		}
	}
}

func TestSkillAnalyticsHelpersHandleDefaultsAndInvalidInputs(t *testing.T) {
	if skillAnalyticsSurface(models.Task{Category: models.CategoryChat}, lifecycleTurnContext{}) != models.SkillSurfaceChat {
		t.Fatal("chat task should use chat surface")
	}
	if skillAnalyticsSurface(models.Task{Category: models.CategoryActive}, lifecycleTurnContext{TaskThreadTurn: true}) != models.SkillSurfaceTaskThread {
		t.Fatal("task thread turn should use task-thread surface")
	}
	if turnThreadID("task-1", lifecycleTurnContext{TaskThreadTurn: true}) != "task-1" {
		t.Fatal("task-thread turns should fall back to task id")
	}
	if agentIDFromAgent(nil) != "" || agentIDFromAgent(&models.Agent{ID: "agent-1"}) != "agent-1" {
		t.Fatal("agentIDFromAgent returned unexpected value")
	}
	if handle, scope, eventType := changedSkillFromToolInput("skill_manage", json.RawMessage(`{"declaration":"---\nname: missing key\n---"}`)); handle != "" || scope != models.SkillScopeGlobal || eventType != models.SkillEventEdited {
		t.Fatalf("unexpected missing-key declaration parse: handle=%q scope=%q event=%q", handle, scope, eventType)
	}
	catalog := agentskills.NewCatalog("turn-2", []agentskills.Entry{
		{Handle: "duplicate", Skill: "one", Source: agentskills.SourceGlobal},
		{Handle: "duplicate", Skill: "two", Source: agentskills.SourceProject},
	})
	if _, fullBody, ok := skillEntryFromViewInput(catalog, json.RawMessage(`{"handle":"duplicate"}`)); ok || fullBody {
		t.Fatal("ambiguous skill_view handle should not record analytics")
	}
	if _, _, ok := skillEntryFromViewInput(catalog, json.RawMessage(`{"handle":`)); ok {
		t.Fatal("malformed skill_view input should not record analytics")
	}
	if instrumentSkillRuntimeTools(nil, nil, nil, skillAnalyticsContext{}) != nil {
		t.Fatal("nil runtime should stay nil")
	}
}

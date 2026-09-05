package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/agentskills"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAgentInspectorListAgentsUsesCompactProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := repository.NewAgentRepo(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM agents WHERE id IS NOT NULL`); err != nil {
		t.Fatalf("clear agents: %v", err)
	}

	visible := &models.Agent{
		Name:         "Alpha Agent",
		Key:          "alpha-agent",
		Description:  "Reviews focused changes",
		SystemPrompt: strings.Repeat("private prompt ", 2048),
		Model:        "private-model",
		Tools:        []string{"Read", "Write"},
		ToolConfig:   models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: "src", Permissions: []string{"read"}}}},
		Plugins:      []string{"private-plugin"},
		MCPServers:   []models.MCPServerConfig{{Name: "private-mcp", Command: []string{"private"}}},
		Skills: []models.SkillConfig{
			{Name: "  First Skill  ", Description: "private description", Content: strings.Repeat("private skill body ", 512)},
			{Name: " \t ", Description: "blank name", Content: strings.Repeat("ignored body ", 512)},
			{Name: "Second Skill", Description: "private description", Content: strings.Repeat("private skill body ", 512)},
		},
		Scope:               models.AgentScopeGlobal,
		SelectableAsPrimary: true,
		Enabled:             true,
		PermissionDefaults:  models.AgentPermissionDefaults{ReadAgents: true, WriteSkills: true},
		ModelDefaults:       models.AgentModelDefaults{Model: "private-default", Temperature: 0.2, MaxTokens: 4096},
		GeneratedStatus:     models.AgentStatusGenerated,
		SourceRefs:          []string{"private-source"},
	}
	if err := repo.Create(ctx, visible); err != nil {
		t.Fatalf("create visible agent: %v", err)
	}
	for _, empty := range []*models.Agent{
		{Name: "Empty Skills Agent", Key: "empty-skills-agent", Enabled: true, SelectableAsPrimary: true, Skills: []models.SkillConfig{}},
		{Name: "Null Skills Agent", Key: "null-skills-agent", Scope: models.AgentScopeProject, Enabled: true, SelectableAsPrimary: true},
	} {
		if err := repo.Create(ctx, empty); err != nil {
			t.Fatalf("create empty-skills agent %q: %v", empty.Name, err)
		}
	}

	for _, hidden := range []*models.Agent{
		{Name: "Disabled Agent", Key: "disabled-agent", Enabled: false, SelectableAsPrimary: true},
		{Name: "Protected Agent", Key: "protected-agent", Enabled: true, SelectableAsPrimary: true, GeneratedStatus: models.AgentStatusProtected},
		{Name: "Archived Agent", Key: "archived-agent", Enabled: true, SelectableAsPrimary: true, GeneratedStatus: models.AgentStatusArchived},
		{Name: "System Kind Agent", Key: "system-kind-agent", Enabled: true, SelectableAsPrimary: true, SystemKind: "system"},
		{Name: "Skill Curator", Key: models.AgentSystemKindSkillCurator, Enabled: true, SelectableAsPrimary: true},
		{Name: "Memory Curator", Key: models.AgentSystemKindMemoryCurator, Enabled: true, SelectableAsPrimary: true},
		{Name: "Goal Agent", Key: models.AgentSystemKindGoal, Enabled: true, SelectableAsPrimary: true},
	} {
		if err := repo.Create(ctx, hidden); err != nil {
			t.Fatalf("create hidden agent %q: %v", hidden.Name, err)
		}
	}
	archivedAt := &models.Agent{Name: "Archived At Agent", Key: "archived-at-agent", Enabled: true, SelectableAsPrimary: true}
	if err := repo.Create(ctx, archivedAt); err != nil {
		t.Fatalf("create archived-at agent: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE agents SET archived_at = datetime('now') WHERE id = ?`, archivedAt.ID); err != nil {
		t.Fatalf("archive agent by timestamp: %v", err)
	}

	counter.SetEnabled(false)
	inspector := newAgentInspector(repo, nil, nil)
	got, err := inspector.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListAgents returned %d agents, want three visible agents: %#v", len(got), got)
	}
	want := agentskills.AgentSummary{
		Key:             "alpha-agent",
		Name:            "Alpha Agent",
		Description:     "Reviews focused changes",
		Scope:           "global",
		Enabled:         true,
		Selectable:      true,
		GeneratedStatus: string(models.AgentStatusGenerated),
		AttachedSkills:  []string{"First Skill", "Second Skill"},
	}
	if got[0].Key != want.Key || got[0].Name != want.Name || got[0].Description != want.Description || got[0].Scope != want.Scope || got[0].Enabled != want.Enabled || got[0].Selectable != want.Selectable || got[0].GeneratedStatus != want.GeneratedStatus || strings.Join(got[0].AttachedSkills, "|") != strings.Join(want.AttachedSkills, "|") {
		t.Fatalf("summary = %#v, want %#v", got[0], want)
	}
	if got[1].Name != "Empty Skills Agent" || got[1].Scope != "global" || len(got[1].AttachedSkills) != 0 {
		t.Fatalf("empty skill summary = %#v", got[1])
	}
	if got[2].Name != "Null Skills Agent" || got[2].Scope != "project" || len(got[2].AttachedSkills) != 0 {
		t.Fatalf("null skill summary = %#v", got[2])
	}

	runtimeTools := agentskills.SkillRuntimeTools(agentskills.NewCatalog("agent-list-test", nil), "", "", inspector)
	counter.Reset()
	counter.SetEnabled(true)
	encoded, handled, isErr, err := runtimeTools.Executor(ctx, "agent_list", json.RawMessage(`{}`))
	counter.SetEnabled(false)
	if !handled || isErr || err != nil {
		t.Fatalf("agent_list runtime execution failed handled=%v isErr=%v err=%v output=%q", handled, isErr, err, encoded)
	}
	var decoded []agentskills.AgentSummary
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("agent_list output is not JSON: %v", err)
	}
	if len(decoded) != 3 || decoded[0].Key != "alpha-agent" || len(decoded[0].AttachedSkills) != 2 || decoded[1].AttachedSkills != nil || decoded[2].AttachedSkills != nil {
		t.Fatalf("agent_list JSON summaries = %#v", decoded)
	}

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact query", statements)
	}
	statement := strings.ToLower(statements[0])
	projection := strings.SplitN(statement, "from agents", 2)[0]
	for _, forbidden := range []string{
		"system_prompt", "model", "tools", "tool_config", "plugins", "mcp_servers",
		"permission_defaults_json", "model_defaults_json", "source_refs_json",
		"created_at", "updated_at",
	} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("compact agent-list query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
	for _, required := range []string{"json_each", "json_extract", "json_group_array"} {
		if !strings.Contains(statement, required) {
			t.Fatalf("compact agent-list query must derive skill names with %s: %s", required, statements[0])
		}
	}

	full, err := repo.GetByID(ctx, visible.ID)
	if err != nil {
		t.Fatalf("GetByID full agent: %v", err)
	}
	if full == nil || full.SystemPrompt == "" || full.Model != "private-model" || len(full.Tools) != 2 || len(full.Plugins) != 1 || len(full.MCPServers) != 1 || len(full.Skills) != 3 || !full.PermissionDefaults.ReadAgents || full.ModelDefaults.Model != "private-default" || len(full.SourceRefs) != 1 {
		t.Fatalf("full detail path lost hydrated fields: %#v", full)
	}
}

func BenchmarkLifecycleAgentListProjection(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := repository.NewAgentRepo(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM agents WHERE id IS NOT NULL`); err != nil {
		b.Fatalf("clear agents: %v", err)
	}
	for i := 0; i < 100; i++ {
		agent := lifecycleBenchmarkAgent(i)
		if err := repo.Create(ctx, agent); err != nil {
			b.Fatalf("create benchmark agent %q: %v", agent.Name, err)
		}
	}
	inspector := newAgentInspector(repo, nil, nil)

	b.Run("current", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			summaries, err := inspector.ListAgents(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(summaries) != 100 {
				b.Fatalf("compact inspector summaries = %d, want 100", len(summaries))
			}
		}
	})
}

func lifecycleBenchmarkAgent(index int) *models.Agent {
	return &models.Agent{
		Name:         fmt.Sprintf("Benchmark Agent %03d", index),
		Key:          fmt.Sprintf("benchmark-agent-%03d", index),
		Description:  "Production-shaped lifecycle agent summary fixture",
		SystemPrompt: strings.Repeat("private prompt with configuration details ", 768),
		Model:        "private-model",
		Tools:        []string{"Read", "Write", "Edit", "Bash"},
		ToolConfig:   models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: fmt.Sprintf("src-%03d", index), Permissions: []string{"read", "write"}}}},
		Plugins:      []string{"private-plugin-one", "private-plugin-two"},
		MCPServers: []models.MCPServerConfig{
			{Name: "private-mcp-one", Command: []string{"private", "one"}},
			{Name: "private-mcp-two", URL: "https://private.example/mcp"},
		},
		Skills: []models.SkillConfig{
			{Name: "skill-one", Description: "private skill description", Content: strings.Repeat("private skill body ", 256)},
			{Name: "skill-two", Description: "private skill description", Content: strings.Repeat("private skill body ", 256)},
		},
		Scope:               models.AgentScopeGlobal,
		SelectableAsPrimary: true,
		Enabled:             true,
		PermissionDefaults:  models.AgentPermissionDefaults{ReadAgents: true, ReadSkills: true, ReadRepositoryFiles: true, UseShellOrTools: true},
		ModelDefaults:       models.AgentModelDefaults{Model: "private-default", Temperature: 0.3, MaxTokens: 8192},
		GeneratedStatus:     models.AgentStatusUserEdited,
		SourceRefs:          []string{fmt.Sprintf("agents/benchmark-%03d/SKILLS.md", index)},
	}
}

func writeServiceSkill(t *testing.T, root, skill, body string) {
	t.Helper()
	dir := filepath.Join(root, "skills", skill)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\ntitle: " + skill + "\ndescription: " + skill + " desc\n---\n\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	appendIndexHeader(t, agentskills.SkillsIndexPath(root), skill)
}

func writeServiceAgentSkill(t *testing.T, root, agent, skill, body string) {
	t.Helper()
	dir := filepath.Join(root, "agents", agent, "skills", skill)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\ntitle: " + skill + "\ndescription: " + skill + " desc\n---\n\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	appendIndexHeader(t, agentskills.AgentsIndexPath(root), agent)
	appendIndexHeader(t, agentskills.AgentSkillsIndexPath(root, agent), agent+"/"+skill)
}

func appendIndexHeader(t *testing.T, path, header string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	existing, _ := os.ReadFile(path)
	if strings.Contains(string(existing), "\n## "+header+"\n") || strings.HasPrefix(string(existing), "## "+header+"\n") {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("## " + header + "\n\n"); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogSkillResolver_UsesOwningAgentSkill(t *testing.T) {
	db := testutil.NewTestDB(t)
	agents := repository.NewAgentRepo(db)
	ctx := context.Background()
	a := &models.Agent{Name: "Router", Key: "router", Enabled: true, SelectableAsPrimary: true}
	if err := agents.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeServiceAgentSkill(t, root, "router", "route_task", "route body")
	cat, err := agentskills.BuildCatalog("turn", root, "")
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewCatalogSkillResolver(agents, func() *agentskills.Catalog { return cat }, root, nil)
	body, err := resolver.ResolveSkill(ctx, models.AgentLifecycleHook{AgentID: a.ID, SkillKey: "route_task"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "route body") {
		t.Fatalf("expected skill body, got %q", body)
	}
}

func TestCatalogSkillResolver_DoesNotFallbackToTaskCatalogForOwnedHook(t *testing.T) {
	db := testutil.NewTestDB(t)
	agents := repository.NewAgentRepo(db)
	ctx := context.Background()
	owner := &models.Agent{Name: "Router", Key: "router", Enabled: true, SelectableAsPrimary: true}
	if err := agents.Create(ctx, owner); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeServiceAgentSkill(t, root, "task_agent", "route_task", "wrong task-agent body")
	cat, err := agentskills.BuildAgentCatalog("turn", root, "", "task_agent")
	if err != nil {
		t.Fatal(err)
	}
	ctx = withLifecycleTurnContext(ctx, lifecycleTurnContext{Catalog: cat})
	resolver := NewCatalogSkillResolver(agents, func() *agentskills.Catalog { return cat }, root, nil)
	body, err := resolver.ResolveSkill(ctx, models.AgentLifecycleHook{AgentID: owner.ID, SkillKey: "route_task"})
	if err == nil {
		t.Fatalf("expected missing owning-agent skill error, got body %q", body)
	}
	if strings.Contains(body, "wrong task-agent body") || !strings.Contains(err.Error(), "owning agent") {
		t.Fatalf("expected owning-agent-only resolution error, body=%q err=%v", body, err)
	}
}

func TestLifecycleRuntimeTools_ComposesReadAndWriteTools(t *testing.T) {
	root := t.TempDir()
	writeServiceSkill(t, root, "skill", "skill body")
	cat, err := agentskills.BuildCatalog("turn", root, "")
	if err != nil {
		t.Fatal(err)
	}
	tools := lifecycleRuntimeTools(cat, nil, nil, nil, root, "", "", "")
	if tools == nil || !tools.HasDefinition("skill_view") || !tools.HasDefinition("skills_list") {
		t.Fatalf("missing read tool definitions: %+v", tools)
	}
	out, handled, isErr, err := tools.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"skill"}`))
	if err != nil || !handled || isErr || !strings.Contains(out, "skill body") {
		t.Fatalf("skill_view failed output=%q handled=%v isErr=%v err=%v", out, handled, isErr, err)
	}
	_, handled, _, _ = tools.Executor(context.Background(), "unknown", json.RawMessage(`{}`))
	if handled {
		t.Fatal("unknown tool should not be handled")
	}
	base := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "base"}}}
	merged := llmcontracts.CompositeRuntimeTools(base, tools)
	if !merged.HasDefinition("base") || !merged.HasDefinition("skill_view") {
		t.Fatalf("composite lost definitions: %+v", merged.Definitions)
	}
}

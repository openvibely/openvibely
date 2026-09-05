package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAgentRepoListSkillCatalogRefsUsesCompactProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewAgentRepo(db)
	ctx := context.Background()
	clearAgentsForRuntimeSummaryTest(t, db)
	alphaProjectID := createSkillCatalogRefProject(t, db, "Project Alpha")

	alpha := createSkillCatalogRefAgent(t, repo, "Alpha Agent", "alpha_agent", alphaProjectID)
	zulu := createSkillCatalogRefAgent(t, repo, "Zulu Agent", "zulu_agent", "")
	archived := createSkillCatalogRefAgent(t, repo, "Archived Agent", "archived_agent", alphaProjectID)
	archived.GeneratedStatus = models.AgentStatusArchived
	if err := repo.Update(ctx, archived); err != nil {
		t.Fatalf("archive skill catalog ref agent: %v", err)
	}

	counter.Reset()
	counter.SetEnabled(true)
	refs, err := repo.ListSkillCatalogRefs(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("ListSkillCatalogRefs: %v", err)
	}

	want := []AgentSkillCatalogRef{
		{ID: alpha.ID, Key: "alpha_agent", ProjectID: alphaProjectID},
		{ID: zulu.ID, Key: "zulu_agent"},
	}
	if len(refs) != len(want) {
		t.Fatalf("refs len = %d, want %d: %#v", len(refs), len(want), refs)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("refs[%d] = %#v, want %#v; all=%#v", i, refs[i], want[i], refs)
		}
	}

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact query", statements)
	}
	stmt := strings.ToLower(statements[0])
	projection := strings.Split(stmt, "from agents")[0]
	for _, required := range []string{"select id", "coalesce(key, '')", "project_id"} {
		if !strings.Contains(projection, required) {
			t.Fatalf("skill catalog ref projection = %q, want %q in %s", projection, required, statements[0])
		}
	}
	for _, forbidden := range []string{"name", "description", "system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json", "created_at", "updated_at"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("skill catalog ref query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
	if !strings.Contains(stmt, "coalesce(generated_status, 'user_edited') <> 'archived'") {
		t.Fatalf("skill catalog ref query must exclude archived agents: %s", statements[0])
	}
	if !strings.Contains(stmt, "order by name asc") {
		t.Fatalf("skill catalog ref query must keep name ASC ordering: %s", statements[0])
	}

	full, err := repo.GetByID(ctx, alpha.ID)
	if err != nil {
		t.Fatalf("GetByID full agent: %v", err)
	}
	if full == nil || full.SystemPrompt == "" || len(full.Tools) == 0 || len(full.ToolConfig.ScopedFiles) == 0 || len(full.Plugins) == 0 || len(full.MCPServers) == 0 || len(full.Skills) == 0 || len(full.SourceRefs) == 0 || !full.PermissionDefaults.ReadAgents || full.ModelDefaults.Model != "gpt-5" {
		t.Fatalf("full detail path lost hydrated fields: %#v", full)
	}
}

func TestAgentRepoListSkillCatalogRefsProductionShape(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping production-shaped allocation fixture in short mode")
	}

	db := testutil.NewTestDB(t)
	repo := NewAgentRepo(db)
	ctx := context.Background()
	clearAgentsForRuntimeSummaryTest(t, db)
	alphaProjectID := createSkillCatalogRefProject(t, db, "Project Alpha")

	for i := 0; i < 1000; i++ {
		projectID := ""
		if i%3 == 0 {
			projectID = alphaProjectID
		}
		createSkillCatalogRefAgent(t, repo, fmt.Sprintf("Agent %04d", i), fmt.Sprintf("agent_%04d", i), projectID)
	}
	archived := createSkillCatalogRefAgent(t, repo, "Agent Archived", "agent_archived", alphaProjectID)
	archived.GeneratedStatus = models.AgentStatusArchived
	if err := repo.Update(ctx, archived); err != nil {
		t.Fatalf("archive skill catalog ref agent: %v", err)
	}

	compactRefs, err := repo.ListSkillCatalogRefs(ctx)
	if err != nil {
		t.Fatalf("ListSkillCatalogRefs: %v", err)
	}
	if len(compactRefs) != 1000 {
		t.Fatalf("skill catalog refs = %d, want 1000", len(compactRefs))
	}
	if compactRefs[0].Key != "agent_0000" || compactRefs[len(compactRefs)-1].Key != "agent_0999" {
		t.Fatalf("skill catalog ref ordering changed: first=%q last=%q", compactRefs[0].Key, compactRefs[len(compactRefs)-1].Key)
	}
	if compactRefs[0].ProjectID != alphaProjectID || compactRefs[1].ProjectID != "" {
		t.Fatalf("skill catalog project scoping changed: first=%q second=%q", compactRefs[0].ProjectID, compactRefs[1].ProjectID)
	}
}

func BenchmarkAgentSkillCatalogRefsProjection(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewAgentRepo(db)
	ctx := context.Background()
	clearAgentsForRuntimeSummaryTest(b, db)
	alphaProjectID := createSkillCatalogRefProject(b, db, "Project Alpha")
	for i := 0; i < 1000; i++ {
		projectID := ""
		if i%3 == 0 {
			projectID = alphaProjectID
		}
		createSkillCatalogRefAgent(b, repo, fmt.Sprintf("Agent %04d", i), fmt.Sprintf("agent_%04d", i), projectID)
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		refs, err := repo.ListSkillCatalogRefs(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if len(refs) != 1000 {
			b.Fatalf("refs len = %d, want 1000", len(refs))
		}
	}
}

func createSkillCatalogRefProject(tb testing.TB, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, name string) string {
	tb.Helper()
	var id string
	if err := db.QueryRowContext(context.Background(), `
		INSERT INTO projects (id, name, description, repo_path, repo_url)
		VALUES (lower(hex(randomblob(16))), ?, '', '', '')
		RETURNING id`, name).Scan(&id); err != nil {
		tb.Fatalf("create skill catalog ref project: %v", err)
	}
	return id
}

func createSkillCatalogRefAgent(tb testing.TB, repo *AgentRepo, name, key, projectID string) *models.Agent {
	tb.Helper()
	agent := &models.Agent{
		Name:         name,
		Description:  "production-shaped skill catalog ref agent",
		SystemPrompt: strings.Repeat("large skill analytics prompt with instructions and examples. ", 320),
		Model:        "inherit",
		Tools:        []string{"Read", "Write", "Edit", "Bash", models.AgentToolScopedFiles},
		ToolConfig: models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{
			Directory:   "src",
			Permissions: []string{"read", "write"},
		}}},
		Plugins: []string{"github@marketplace", "playwright@claude-plugins-official"},
		MCPServers: []models.MCPServerConfig{{
			Name:    "playwright",
			Command: []string{"npx", "-y", "@playwright/mcp"},
			Env:     map[string]string{"TOKEN": strings.Repeat("x", 256)},
		}},
		Skills: []models.SkillConfig{{
			Name:        "triage",
			Description: "large skill config",
			Tools:       "Read, Grep, Bash",
			Content:     strings.Repeat("skill body ", 256),
		}},
		Key:                 key,
		ProjectID:           projectID,
		PermissionDefaults:  models.AgentPermissionDefaults{ReadAgents: true, ReadSkills: true, ReadRepositoryFiles: true, UseShellOrTools: true},
		ModelDefaults:       models.AgentModelDefaults{Model: "gpt-5", Temperature: 0.3, MaxTokens: 8192},
		SourceRefs:          []string{"agents/skill-catalog/SKILLS.md", strings.Repeat("ref", 128)},
		Enabled:             true,
		SelectableAsPrimary: true,
	}
	if projectID != "" {
		agent.Scope = models.AgentScopeProject
	}
	if err := repo.Create(context.Background(), agent); err != nil {
		tb.Fatalf("create skill catalog ref agent %q: %v", name, err)
	}
	return agent
}

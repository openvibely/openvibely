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

func TestAgentRepoListRuntimeSummariesUsesCompactProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewAgentRepo(db)
	ctx := context.Background()
	clearAgentsForRuntimeSummaryTest(t, db)

	alpha := runtimeSummaryAgent("Alpha Agent", "alpha work", "inherit", 2, 1)
	alpha.SystemPrompt = strings.Repeat("large alpha prompt ", 2048)
	alpha.ToolConfig = models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: "src", Permissions: []string{"read"}}}}
	alpha.Plugins = []string{"github@marketplace"}
	alpha.PermissionDefaults = models.AgentPermissionDefaults{ReadAgents: true, WriteSkills: true}
	alpha.ModelDefaults = models.AgentModelDefaults{Model: "gpt-5", Temperature: 0.2, MaxTokens: 4096}
	alpha.SourceRefs = []string{"agents/alpha/SKILLS.md"}
	createRuntimeSummaryAgent(t, repo, alpha)

	bravo := runtimeSummaryAgent("Bravo Agent", "bravo work", "gpt-5", 0, 2)
	createRuntimeSummaryAgent(t, repo, bravo)

	archived := runtimeSummaryAgent("Archived Agent", "hidden", "gpt-5", 3, 3)
	archived.GeneratedStatus = models.AgentStatusArchived
	createRuntimeSummaryAgent(t, repo, archived)

	counter.Reset()
	counter.SetEnabled(true)
	summaries, err := repo.ListRuntimeSummaries(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("ListRuntimeSummaries: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries len = %d, want 2: %#v", len(summaries), summaries)
	}
	if summaries[0] != (AgentRuntimeSummary{Name: "Alpha Agent", Description: "alpha work", Model: "inherit", SkillCount: 2, MCPServerCount: 1}) {
		t.Fatalf("first summary = %#v", summaries[0])
	}
	if summaries[1] != (AgentRuntimeSummary{Name: "Bravo Agent", Description: "bravo work", Model: "gpt-5", SkillCount: 0, MCPServerCount: 2}) {
		t.Fatalf("second summary = %#v", summaries[1])
	}

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact query", statements)
	}
	projection := strings.ToLower(strings.Split(statements[0], "from agents")[0])
	for _, forbidden := range []string{"id", "system_prompt", "tools", "tool_config", "plugins", "permission_defaults_json", "model_defaults_json", "source_refs_json", "created_at", "updated_at"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("runtime summary query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
	if !strings.Contains(strings.ToLower(statements[0]), "order by name asc") {
		t.Fatalf("runtime summary query must keep name ASC ordering: %s", statements[0])
	}

	full, err := repo.GetByID(ctx, alpha.ID)
	if err != nil {
		t.Fatalf("GetByID full agent: %v", err)
	}
	if full == nil || full.SystemPrompt == "" || len(full.Plugins) != 1 || len(full.SourceRefs) != 1 || !full.PermissionDefaults.ReadAgents || full.ModelDefaults.Model != "gpt-5" {
		t.Fatalf("full detail path lost hydrated fields: %#v", full)
	}
}

func TestAgentRepoListRuntimeSummariesEmpty(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAgentRepo(db)
	ctx := context.Background()
	clearAgentsForRuntimeSummaryTest(t, db)

	summaries, err := repo.ListRuntimeSummaries(ctx)
	if err != nil {
		t.Fatalf("ListRuntimeSummaries: %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("summaries len = %d, want 0", len(summaries))
	}
}

func BenchmarkAgentRuntimeListSummariesProjection(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewAgentRepo(db)
	ctx := context.Background()
	clearAgentsForRuntimeSummaryTest(b, db)

	for i := 0; i < 100; i++ {
		agent := runtimeSummaryAgent(fmt.Sprintf("Agent %03d", i), "production-shaped runtime summary fixture", "inherit", 4, 3)
		agent.SystemPrompt = strings.Repeat("large production runtime prompt with examples and constraints. ", 600)
		agent.ToolConfig = models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: fmt.Sprintf("dir-%03d", i), Permissions: []string{"read", "write"}}}}
		agent.Tools = []string{"Read", "Write", "Edit", "Bash", models.AgentToolScopedFiles}
		agent.Plugins = []string{"github@marketplace", "playwright@claude-plugins-official"}
		agent.PermissionDefaults = models.AgentPermissionDefaults{ReadAgents: true, ReadSkills: true, ReadRepositoryFiles: true, UseShellOrTools: true}
		agent.ModelDefaults = models.AgentModelDefaults{Model: "gpt-5", Temperature: 0.3, MaxTokens: 8192}
		agent.SourceRefs = []string{fmt.Sprintf("agents/agent-%03d/SKILLS.md", i)}
		createRuntimeSummaryAgent(b, repo, agent)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		summaries, err := repo.ListRuntimeSummaries(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if len(summaries) != 100 {
			b.Fatalf("summaries len = %d, want 100", len(summaries))
		}
	}
}

func runtimeSummaryAgent(name, description, model string, skills, mcpServers int) *models.Agent {
	agentSkills := make([]models.SkillConfig, skills)
	for i := range agentSkills {
		agentSkills[i] = models.SkillConfig{Name: fmt.Sprintf("skill-%d", i), Description: "runtime summary skill", Content: strings.Repeat("skill body ", 64)}
	}
	agentMCPServers := make([]models.MCPServerConfig, mcpServers)
	for i := range agentMCPServers {
		agentMCPServers[i] = models.MCPServerConfig{Name: fmt.Sprintf("mcp-%d", i), Command: []string{"npx", "server"}}
	}
	return &models.Agent{
		Name:                name,
		Description:         description,
		SystemPrompt:        "prompt",
		Model:               model,
		Tools:               []string{"Read", "Bash"},
		Skills:              agentSkills,
		MCPServers:          agentMCPServers,
		Enabled:             true,
		SelectableAsPrimary: true,
	}
}

func createRuntimeSummaryAgent(tb testing.TB, repo *AgentRepo, agent *models.Agent) {
	tb.Helper()
	if err := repo.Create(context.Background(), agent); err != nil {
		tb.Fatalf("create agent %q: %v", agent.Name, err)
	}
}

func clearAgentsForRuntimeSummaryTest(tb testing.TB, db *sql.DB) {
	tb.Helper()
	if _, err := db.ExecContext(context.Background(), `DELETE FROM agents WHERE id IS NOT NULL`); err != nil {
		tb.Fatalf("clear agents: %v", err)
	}
}

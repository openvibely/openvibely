package repository_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAgentRepoListChatAssignableDefinitionsUsesCompactProjectionAndPreservesContext(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := repository.NewAgentRepo(db)
	ctx := context.Background()
	clearAgentsForChatAssignableTest(t, db)

	alpha := chatAssignableTestAgent("Alpha Agent", "alpha", strings.Repeat("Alpha description. ", 20))
	createChatAssignableTestAgent(t, repo, alpha)
	bravo := chatAssignableTestAgent("Bravo Agent", "", "Bravo handles short work.")
	createChatAssignableTestAgent(t, repo, bravo)
	duplicate := chatAssignableTestAgent("Duplicate", "dup-1", "First duplicate")
	createChatAssignableTestAgent(t, repo, duplicate)
	insertLegacyChatAssignableDuplicate(t, db, "duplicate", "dup-2")
	protected := chatAssignableTestAgent("Protected", "protected", "Hidden protected agent")
	protected.SystemKind = models.AgentSystemKindMemoryCurator
	createChatAssignableTestAgent(t, repo, protected)
	disabled := chatAssignableTestAgent("Disabled", "disabled", "Hidden disabled agent")
	disabled.Enabled = false
	createChatAssignableTestAgent(t, repo, disabled)
	nonPrimary := chatAssignableTestAgent("Non Primary", "non_primary", "Hidden non-primary agent")
	nonPrimary.SelectableAsPrimary = false
	createChatAssignableTestAgent(t, repo, nonPrimary)
	archived := chatAssignableTestAgent("Archived", "archived", "Hidden archived status agent")
	archived.GeneratedStatus = models.AgentStatusArchived
	createChatAssignableTestAgent(t, repo, archived)
	archivedAtOnly := chatAssignableTestAgent("Archived At Only", "archived_at_only", "Hidden archived_at agent")
	createChatAssignableTestAgent(t, repo, archivedAtOnly)
	markChatAssignableTestAgentArchivedAt(t, db, archivedAtOnly.ID)

	full, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List full agents: %v", err)
	}
	fullContext := service.BuildAgentDefinitionContextString(chatAssignableSummariesFromAgents(full))

	counter.Reset()
	counter.SetEnabled(true)
	compact, err := repo.ListChatAssignableDefinitions(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("ListChatAssignableDefinitions: %v", err)
	}
	compactContext := service.BuildAgentDefinitionContextString(compact)
	if compactContext != fullContext {
		t.Fatalf("compact context differs from full context\nfull:\n%s\ncompact:\n%s", fullContext, compactContext)
	}
	if !strings.Contains(compactContext, `Name: "Alpha Agent"; key: alpha; description: `) || !strings.Contains(compactContext, `Name: "Bravo Agent"`) {
		t.Fatalf("expected normal selectable agents in context, got:\n%s", compactContext)
	}
	for _, hidden := range []string{"Duplicate", "duplicate", "Protected", "Disabled", "Non Primary", "Archived", "Archived At Only"} {
		if strings.Contains(compactContext, hidden) {
			t.Fatalf("agent %q must not be advertised in chat context:\n%s", hidden, compactContext)
		}
	}
	if !strings.Contains(compactContext, "...") {
		t.Fatalf("expected long description to remain truncated, got:\n%s", compactContext)
	}

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact query", statements)
	}
	projection := strings.ToLower(strings.Split(statements[0], "from agents")[0])
	for _, forbidden := range []string{"system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json", "created_by", "absorbed_into", "project_id"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("chat assignable query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
	if !strings.Contains(strings.ToLower(statements[0]), "order by name asc") {
		t.Fatalf("chat assignable query must keep name ASC ordering: %s", statements[0])
	}

	stored, err := repo.GetByID(ctx, alpha.ID)
	if err != nil {
		t.Fatalf("GetByID full agent: %v", err)
	}
	if stored == nil || stored.SystemPrompt == "" || len(stored.Tools) == 0 || len(stored.Plugins) == 0 || len(stored.MCPServers) == 0 || len(stored.Skills) == 0 || !stored.PermissionDefaults.ReadAgents || stored.ModelDefaults.Model == "" || len(stored.SourceRefs) == 0 {
		t.Fatalf("full detail path lost hydrated fields: %#v", stored)
	}
}

func BenchmarkAgentRepoListChatAssignableDefinitionsProjection(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := repository.NewAgentRepo(db)
	ctx := context.Background()
	clearAgentsForChatAssignableTest(b, db)

	for i := 0; i < 100; i++ {
		agent := chatAssignableTestAgent(fmt.Sprintf("Production Agent %03d", i), fmt.Sprintf("production_agent_%03d", i), "Production-shaped chat context fixture.")
		agent.SystemPrompt = strings.Repeat("large production prompt with examples, constraints, and instructions. ", 1024)
		agent.ToolConfig = models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: fmt.Sprintf("dir-%03d", i), Permissions: []string{"read", "write"}}}}
		agent.Tools = []string{"Read", "Write", "Edit", "Bash", "Glob", "Grep", models.AgentToolScopedFiles}
		agent.Plugins = []string{"github@marketplace", "playwright@claude-plugins-official"}
		agent.MCPServers = []models.MCPServerConfig{{Name: "playwright", Command: []string{"npx", "-y", "@playwright/mcp"}}, {Name: "filesystem", Command: []string{"npx", "server"}}}
		agent.Skills = []models.SkillConfig{{Name: "inspect", Description: "Inspect code", Content: strings.Repeat("skill instructions ", 512)}, {Name: "repair", Description: "Repair code", Content: strings.Repeat("repair instructions ", 512)}}
		agent.PermissionDefaults = models.AgentPermissionDefaults{ReadAgents: true, ReadSkills: true, ReadRepositoryFiles: true, WriteRepositoryFiles: true, UseShellOrTools: true}
		agent.ModelDefaults = models.AgentModelDefaults{Model: "gpt-5", Temperature: 0.2, MaxTokens: 8192}
		agent.SourceRefs = []string{fmt.Sprintf("agents/production-%03d/SKILLS.md", i), fmt.Sprintf("agents/production-%03d/AGENT.md", i)}
		createChatAssignableTestAgent(b, repo, agent)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		agents, err := repo.ListChatAssignableDefinitions(ctx)
		if err != nil {
			b.Fatal(err)
		}
		assignable := service.UniqueChatAssignableAgentDefinitions(agents)
		if len(assignable) != 100 {
			b.Fatalf("assignable len = %d, want 100", len(assignable))
		}
	}
}

func chatAssignableTestAgent(name, key, description string) *models.Agent {
	return &models.Agent{
		Name:                name,
		Key:                 key,
		Description:         description,
		SystemPrompt:        strings.Repeat("full prompt ", 256),
		Model:               "inherit",
		Tools:               []string{"Read", "Bash", models.AgentToolScopedFiles},
		ToolConfig:          models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: "src", Permissions: []string{"read"}}}},
		Plugins:             []string{"github@marketplace"},
		MCPServers:          []models.MCPServerConfig{{Name: "playwright", Command: []string{"npx", "-y", "@playwright/mcp"}}},
		Skills:              []models.SkillConfig{{Name: "review", Description: "Review code", Content: strings.Repeat("review instructions ", 64)}},
		Enabled:             true,
		SelectableAsPrimary: true,
		PermissionDefaults:  models.AgentPermissionDefaults{ReadAgents: true, ReadSkills: true, ReadRepositoryFiles: true},
		ModelDefaults:       models.AgentModelDefaults{Model: "gpt-5", Temperature: 0.3, MaxTokens: 4096},
		SourceRefs:          []string{"agents/source.md"},
	}
}

func chatAssignableSummariesFromAgents(agents []models.Agent) []models.ChatAssignableAgentDefinition {
	summaries := make([]models.ChatAssignableAgentDefinition, 0, len(agents))
	for _, agent := range agents {
		summaries = append(summaries, models.ChatAssignableAgentDefinition{
			ID:                  agent.ID,
			Name:                agent.Name,
			Description:         agent.Description,
			Key:                 agent.Key,
			SystemKind:          agent.SystemKind,
			SelectableAsPrimary: agent.SelectableAsPrimary,
			Enabled:             agent.Enabled,
			GeneratedStatus:     agent.GeneratedStatus,
			ArchivedAt:          agent.ArchivedAt,
		})
	}
	return summaries
}

func createChatAssignableTestAgent(tb testing.TB, repo *repository.AgentRepo, agent *models.Agent) {
	tb.Helper()
	if err := repo.Create(context.Background(), agent); err != nil {
		tb.Fatalf("create agent %q: %v", agent.Name, err)
	}
}

func insertLegacyChatAssignableDuplicate(tb testing.TB, db *sql.DB, name, key string) {
	tb.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO agents (id, name, key, description, system_prompt, model, enabled, selectable_as_primary, generated_status)
		VALUES (lower(hex(randomblob(16))), ?, ?, 'Legacy duplicate', 'large prompt', 'inherit', 1, 1, 'user_edited')`, name, key)
	if err != nil {
		tb.Fatalf("insert duplicate agent %q: %v", name, err)
	}
}

func markChatAssignableTestAgentArchivedAt(tb testing.TB, db *sql.DB, id string) {
	tb.Helper()
	if _, err := db.ExecContext(context.Background(), `UPDATE agents SET archived_at = datetime('now') WHERE id = ?`, id); err != nil {
		tb.Fatalf("mark agent archived_at: %v", err)
	}
}

func clearAgentsForChatAssignableTest(tb testing.TB, db *sql.DB) {
	tb.Helper()
	if _, err := db.ExecContext(context.Background(), `DELETE FROM agents WHERE id IS NOT NULL`); err != nil {
		tb.Fatalf("clear agents: %v", err)
	}
}

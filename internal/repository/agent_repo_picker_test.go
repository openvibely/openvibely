package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAgentRepoListPickerOptionsUsesCompactProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewAgentRepo(db)
	ctx := context.Background()
	clearAgentsForRuntimeSummaryTest(t, db)

	zulu := createPickerAgent(t, repo, "Zulu Agent")
	alpha := createPickerAgent(t, repo, "Alpha Agent")
	archived := createPickerAgent(t, repo, "Archived Agent")
	archived.GeneratedStatus = models.AgentStatusArchived
	if err := repo.Update(ctx, archived); err != nil {
		t.Fatalf("archive picker agent: %v", err)
	}

	counter.Reset()
	counter.SetEnabled(true)
	options, err := repo.ListPickerOptions(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("ListPickerOptions: %v", err)
	}

	want := []AgentPickerOption{{ID: alpha.ID, Name: "Alpha Agent"}, {ID: zulu.ID, Name: "Zulu Agent"}}
	if len(options) != len(want) {
		t.Fatalf("options len = %d, want %d: %#v", len(options), len(want), options)
	}
	for i := range want {
		if options[i] != want[i] {
			t.Fatalf("options[%d] = %#v, want %#v; all=%#v", i, options[i], want[i], options)
		}
	}

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact query", statements)
	}
	stmt := strings.ToLower(statements[0])
	projection := strings.Split(stmt, "from agents")[0]
	for _, required := range []string{"select id, name"} {
		if !strings.Contains(projection, required) {
			t.Fatalf("picker query projection = %q, want %q in %s", projection, required, statements[0])
		}
	}
	for _, forbidden := range []string{"description", "system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json", "created_at", "updated_at"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("picker query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
	if !strings.Contains(stmt, "coalesce(generated_status, 'user_edited') <> 'archived'") {
		t.Fatalf("picker query must exclude archived agents: %s", statements[0])
	}
	if !strings.Contains(stmt, "order by name asc") {
		t.Fatalf("picker query must keep name ASC ordering: %s", statements[0])
	}

	full, err := repo.GetByID(ctx, alpha.ID)
	if err != nil {
		t.Fatalf("GetByID full agent: %v", err)
	}
	if full == nil || full.SystemPrompt == "" || len(full.Tools) == 0 || len(full.ToolConfig.ScopedFiles) == 0 || len(full.Plugins) == 0 || len(full.MCPServers) == 0 || len(full.Skills) == 0 || len(full.SourceRefs) == 0 || !full.PermissionDefaults.ReadAgents || full.ModelDefaults.Model != "gpt-5" {
		t.Fatalf("full detail path lost hydrated fields: %#v", full)
	}
}

func TestAgentRepoListPickerOptionsMatchesFullListJSONWithLowerAllocations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping production-shaped allocation fixture in short mode")
	}

	db := testutil.NewTestDB(t)
	repo := NewAgentRepo(db)
	ctx := context.Background()
	clearAgentsForRuntimeSummaryTest(t, db)

	for i := 0; i < 1000; i++ {
		createPickerAgent(t, repo, fmt.Sprintf("Agent %04d", i))
	}
	archived := createPickerAgent(t, repo, "Agent Archived")
	archived.GeneratedStatus = models.AgentStatusArchived
	if err := repo.Update(ctx, archived); err != nil {
		t.Fatalf("archive picker agent: %v", err)
	}

	fullAgents, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List full agents: %v", err)
	}
	compactOptions, err := repo.ListPickerOptions(ctx)
	if err != nil {
		t.Fatalf("ListPickerOptions: %v", err)
	}
	if len(fullAgents) != 1000 || len(compactOptions) != 1000 {
		t.Fatalf("fixture result sizes full=%d compact=%d, want 1000", len(fullAgents), len(compactOptions))
	}

	fullJSON := marshalPickerJSONFromAgents(t, fullAgents)
	compactJSON := marshalPickerJSONFromOptions(t, compactOptions)
	if string(compactJSON) != string(fullJSON) {
		t.Fatalf("compact picker JSON changed\nfull: %s\ncompact: %s", fullJSON, compactJSON)
	}

	fullAllocs := testing.AllocsPerRun(3, func() {
		agents, err := repo.List(ctx)
		if err != nil {
			t.Fatalf("List full agents during alloc check: %v", err)
		}
		_ = marshalPickerJSONFromAgents(t, agents)
	})
	compactAllocs := testing.AllocsPerRun(3, func() {
		options, err := repo.ListPickerOptions(ctx)
		if err != nil {
			t.Fatalf("ListPickerOptions during alloc check: %v", err)
		}
		_ = marshalPickerJSONFromOptions(t, options)
	})
	if compactAllocs > fullAllocs*0.10 {
		t.Fatalf("compact picker allocations = %.0f, full hydration allocations = %.0f; want at least 90%% reduction", compactAllocs, fullAllocs)
	}
	t.Logf("compact picker allocations %.0f vs full hydration %.0f (%.1f%% reduction)", compactAllocs, fullAllocs, (1-compactAllocs/fullAllocs)*100)
}

func BenchmarkAgentPickerOptionsProjectionVsFullHydration(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewAgentRepo(db)
	ctx := context.Background()
	clearAgentsForRuntimeSummaryTest(b, db)
	for i := 0; i < 1000; i++ {
		createPickerAgent(b, repo, fmt.Sprintf("Agent %04d", i))
	}

	b.Run("full_hydration", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			agents, err := repo.List(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(agents) != 1000 {
				b.Fatalf("agents len = %d, want 1000", len(agents))
			}
			if i == 0 {
				b.ReportMetric(float64(len(marshalPickerJSONFromAgents(b, agents))), "json_bytes")
			}
		}
	})

	b.Run("compact_projection", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			options, err := repo.ListPickerOptions(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(options) != 1000 {
				b.Fatalf("options len = %d, want 1000", len(options))
			}
			if i == 0 {
				b.ReportMetric(float64(len(marshalPickerJSONFromOptions(b, options))), "json_bytes")
			}
		}
	})
}

func createPickerAgent(tb testing.TB, repo *AgentRepo, name string) *models.Agent {
	tb.Helper()
	agent := &models.Agent{
		Name:         name,
		Description:  "production-shaped picker agent",
		SystemPrompt: strings.Repeat("large webhook picker prompt with instructions and examples. ", 320),
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
		PermissionDefaults:  models.AgentPermissionDefaults{ReadAgents: true, ReadSkills: true, ReadRepositoryFiles: true, UseShellOrTools: true},
		ModelDefaults:       models.AgentModelDefaults{Model: "gpt-5", Temperature: 0.3, MaxTokens: 8192},
		SourceRefs:          []string{"agents/picker/SKILLS.md", strings.Repeat("ref", 128)},
		Enabled:             true,
		SelectableAsPrimary: true,
	}
	if err := repo.Create(context.Background(), agent); err != nil {
		tb.Fatalf("create picker agent %q: %v", name, err)
	}
	return agent
}

func marshalPickerJSONFromAgents(tb testing.TB, agents []models.Agent) []byte {
	tb.Helper()
	options := make([]AgentPickerOption, len(agents))
	for i, agent := range agents {
		options[i] = AgentPickerOption{ID: agent.ID, Name: agent.Name}
	}
	return marshalPickerJSONFromOptions(tb, options)
}

func marshalPickerJSONFromOptions(tb testing.TB, options []AgentPickerOption) []byte {
	tb.Helper()
	type agentData struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	data := make([]agentData, len(options))
	for i, option := range options {
		data[i] = agentData{ID: option.ID, Name: option.Name}
	}
	b, err := json.Marshal(data)
	if err != nil {
		tb.Fatalf("marshal picker JSON: %v", err)
	}
	return b
}

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestAgentRepoListPickerOptionsForProjectExcludesForeignProjectAgents(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewAgentRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()
	clearAgentsForRuntimeSummaryTest(t, db)

	projectA := &models.Project{Name: "Picker Project A"}
	if err := projectRepo.Create(ctx, projectA); err != nil {
		t.Fatalf("create project A: %v", err)
	}
	projectB := &models.Project{Name: "Picker Project B"}
	if err := projectRepo.Create(ctx, projectB); err != nil {
		t.Fatalf("create project B: %v", err)
	}

	global := createPickerAgent(t, repo, "Global Picker Agent")
	projectAgentA := createPickerAgent(t, repo, "Project A Picker Agent")
	projectAgentA.Scope = models.AgentScopeProject
	projectAgentA.ProjectID = projectA.ID
	if err := repo.Update(ctx, projectAgentA); err != nil {
		t.Fatalf("update project A agent: %v", err)
	}
	projectAgentB := createPickerAgent(t, repo, "Project B Picker Agent")
	projectAgentB.Scope = models.AgentScopeProject
	projectAgentB.ProjectID = projectB.ID
	if err := repo.Update(ctx, projectAgentB); err != nil {
		t.Fatalf("update project B agent: %v", err)
	}

	counter.Reset()
	counter.SetEnabled(true)
	options, err := repo.ListPickerOptionsForProject(ctx, projectA.ID)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("ListPickerOptionsForProject: %v", err)
	}
	gotIDs := make([]string, 0, len(options))
	for _, option := range options {
		gotIDs = append(gotIDs, option.ID)
	}
	if len(gotIDs) != 2 || !containsString(gotIDs, global.ID) || !containsString(gotIDs, projectAgentA.ID) || containsString(gotIDs, projectAgentB.ID) {
		t.Fatalf("project A picker IDs = %#v, want global=%s and project A=%s without project B=%s", gotIDs, global.ID, projectAgentA.ID, projectAgentB.ID)
	}

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact query", statements)
	}
	stmt := strings.ToLower(statements[0])
	projection := strings.Split(stmt, "from agents")[0]
	if !strings.Contains(projection, "select id, name") {
		t.Fatalf("project picker projection = %q, want only identity columns: %s", projection, statements[0])
	}
	if !strings.Contains(stmt, "coalesce(scope, 'global')") || !strings.Contains(stmt, "project_id = ?") {
		t.Fatalf("project picker query must enforce project availability: %s", statements[0])
	}
	for _, forbidden := range []string{"description", "system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json", "created_at", "updated_at"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("project picker query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestAgentRepoGetTaskDetailAgentLabelUsesCompactProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewAgentRepo(db)
	ctx := context.Background()
	clearAgentsForRuntimeSummaryTest(t, db)

	global := createPickerAgent(t, repo, "Global Status Agent")
	projectAgent := &models.Agent{
		Name:                "Project Status Agent",
		SystemPrompt:        "Project agent details stay out of the badge query.",
		Model:               "inherit",
		Scope:               models.AgentScopeProject,
		ProjectID:           "default",
		Enabled:             true,
		SelectableAsPrimary: true,
	}
	if err := repo.Create(ctx, projectAgent); err != nil {
		t.Fatalf("create project agent: %v", err)
	}
	archived := createPickerAgent(t, repo, "Archived Status Agent")
	archived.GeneratedStatus = models.AgentStatusArchived
	if err := repo.Update(ctx, archived); err != nil {
		t.Fatalf("archive status agent: %v", err)
	}
	disabled := createPickerAgent(t, repo, "Disabled Status Agent")
	disabled.Enabled = false
	disabled.SelectableAsPrimary = false
	if err := repo.Update(ctx, disabled); err != nil {
		t.Fatalf("disable status agent: %v", err)
	}

	counter.Reset()
	counter.SetEnabled(true)
	label, err := repo.GetTaskDetailAgentLabel(ctx, "other-project", global.ID)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("get global task detail label: %v", err)
	}
	if label == nil || *label != (AgentPickerOption{ID: global.ID, Name: global.Name}) {
		t.Fatalf("global label = %#v, want id/name for %q", label, global.Name)
	}
	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one label query", statements)
	}
	stmt := strings.ToLower(statements[0])
	projection := strings.Split(stmt, "from agents")[0]
	if !strings.Contains(projection, "select id, name") {
		t.Fatalf("task detail label projection = %q, want only identity columns: %s", projection, statements[0])
	}
	for _, forbidden := range []string{"description", "system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json", "created_at", "updated_at"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("task detail label query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
	if !strings.Contains(stmt, "coalesce(scope, 'global')") || !strings.Contains(stmt, "project_id = ?") {
		t.Fatalf("task detail label query must enforce project availability: %s", statements[0])
	}

	projectLabel, err := repo.GetTaskDetailAgentLabel(ctx, "default", projectAgent.ID)
	if err != nil {
		t.Fatalf("get available project label: %v", err)
	}
	if projectLabel == nil || projectLabel.Name != projectAgent.Name {
		t.Fatalf("available project label = %#v, want %q", projectLabel, projectAgent.Name)
	}
	unavailableProjectLabel, err := repo.GetTaskDetailAgentLabel(ctx, "other-project", projectAgent.ID)
	if err != nil {
		t.Fatalf("get unavailable project label: %v", err)
	}
	if unavailableProjectLabel != nil {
		t.Fatalf("unavailable project label = %#v, want nil", unavailableProjectLabel)
	}
	archivedLabel, err := repo.GetTaskDetailAgentLabel(ctx, "default", archived.ID)
	if err != nil {
		t.Fatalf("get archived label: %v", err)
	}
	if archivedLabel != nil {
		t.Fatalf("archived label = %#v, want nil", archivedLabel)
	}
	disabledLabel, err := repo.GetTaskDetailAgentLabel(ctx, "other-project", disabled.ID)
	if err != nil {
		t.Fatalf("get disabled label: %v", err)
	}
	if disabledLabel == nil || disabledLabel.Name != disabled.Name {
		t.Fatalf("disabled assigned label = %#v, want %q", disabledLabel, disabled.Name)
	}

	counter.Reset()
	counter.SetEnabled(true)
	missing, err := repo.GetTaskDetailAgentLabel(ctx, "default", "missing-agent-id")
	_, emptyErr := repo.GetTaskDetailAgentLabel(ctx, "default", "")
	counter.SetEnabled(false)
	if err != nil || emptyErr != nil {
		t.Fatalf("missing/empty labels returned errors: missing=%v empty=%v", err, emptyErr)
	}
	if missing != nil {
		t.Fatalf("missing label = %#v, want nil", missing)
	}
	if len(counter.Statements()) != 1 {
		t.Fatalf("empty Agent ID should skip lookup; statements = %#v", counter.Statements())
	}

	full, err := repo.GetByID(ctx, global.ID)
	if err != nil {
		t.Fatalf("get full status agent: %v", err)
	}
	if full == nil || full.SystemPrompt == "" || len(full.Tools) == 0 || len(full.ToolConfig.ScopedFiles) == 0 || len(full.Plugins) == 0 || len(full.MCPServers) == 0 || len(full.Skills) == 0 || len(full.SourceRefs) == 0 || !full.PermissionDefaults.ReadAgents || full.ModelDefaults.Model != "gpt-5" {
		t.Fatalf("full detail path lost hydrated fields: %#v", full)
	}
}

func BenchmarkAgentTaskDetailLabelProjectionVsFullHydration(b *testing.B) {
	db, counter := testutil.NewStatementCountingTestDB(b)
	repo := NewAgentRepo(db)
	clearAgentsForRuntimeSummaryTest(b, db)

	var targetID string
	for i := 0; i < 1000; i++ {
		agent := createPickerAgent(b, repo, fmt.Sprintf("Agent %04d", i))
		if i == 999 {
			targetID = agent.ID
		}
	}

	for _, tc := range []struct {
		name   string
		lookup func(context.Context) (string, error)
	}{
		{
			name: "full_hydration",
			lookup: func(ctx context.Context) (string, error) {
				agents, err := repo.List(ctx)
				if err != nil {
					return "", err
				}
				for _, agent := range agents {
					if agent.ID == targetID {
						return agent.Name, nil
					}
				}
				return "", fmt.Errorf("target agent %q not found", targetID)
			},
		},
		{
			name: "task_detail_label",
			lookup: func(ctx context.Context) (string, error) {
				label, err := repo.GetTaskDetailAgentLabel(ctx, "benchmark-project", targetID)
				if err != nil {
					return "", err
				}
				if label == nil {
					return "", fmt.Errorf("target agent %q not found", targetID)
				}
				return label.Name, nil
			},
		},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			var totalLightweightWait time.Duration
			for i := 0; i < b.N; i++ {
				queryStarted := make(chan struct{})
				var once sync.Once
				counter.SetObserver(func(_ context.Context, query string) {
					if strings.Contains(strings.ToLower(query), "from agents") {
						once.Do(func() { close(queryStarted) })
					}
				})

				type lookupResult struct {
					name string
					err  error
				}
				resultCh := make(chan lookupResult, 1)
				go func() {
					name, err := tc.lookup(context.Background())
					resultCh <- lookupResult{name: name, err: err}
				}()
				var result lookupResult
				lookupComplete := false
				select {
				case <-queryStarted:
				case result = <-resultCh:
					lookupComplete = true
				case <-time.After(2 * time.Second):
					b.Fatalf("Agent lookup query did not start")
				}

				lightweightStart := time.Now()
				var projectID string
				if err := db.QueryRowContext(context.Background(), `SELECT id FROM projects ORDER BY id LIMIT 1`).Scan(&projectID); err != nil {
					b.Fatalf("lightweight project lookup: %v", err)
				}
				totalLightweightWait += time.Since(lightweightStart)

				if !lookupComplete {
					result = <-resultCh
				}
				counter.SetObserver(nil)
				if result.err != nil {
					b.Fatal(result.err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(totalLightweightWait.Nanoseconds())/float64(b.N), "lightweight_db_wait_ns/op")
		})
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

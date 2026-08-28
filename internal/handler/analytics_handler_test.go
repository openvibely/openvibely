package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

// --- Analytics page ---

func TestAnalytics_NoProjects(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/analytics").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestAnalytics_WithProject(t *testing.T) {
	tc := NewTestContext(t)
	tc.CreateProject().WithName("My Project").Build()
	rec := tc.HTTP().Get("/analytics").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK).Contains("My Project")
}

func TestAnalytics_WithProjectID(t *testing.T) {
	tc := NewTestContext(t)
	p := tc.CreateProject().WithName("Selected").Build()
	rec := tc.HTTP().Get("/analytics?project_id=" + p.ID).Execute()
	tc.Assert(rec).StatusCode(http.StatusOK).Contains("Selected")
}

func TestAnalytics_HTMX(t *testing.T) {
	tc := NewTestContext(t)
	tc.CreateProject().WithName("HTMX Project").Build()
	rec := tc.HTMX().Get("/analytics").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

// --- API endpoints backed by execRepo (wired in NewTestContext) ---

func TestGetAnalyticsUsage_Default(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/analytics/usage").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestGetAnalyticsUsage_AccountLimitsAreOrderedAndPrivate(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	cfg := &models.LLMConfig{
		Name:              "OpenAI OAuth",
		Provider:          models.ProviderOpenAI,
		Model:             "gpt-5.3-codex",
		AuthMethod:        models.AuthMethodOAuth,
		OAuthAccessToken:  "oauth-secret-token",
		OAuthRefreshToken: "refresh-secret-token",
		OAuthAccountID:    "account-secret-id",
	}
	if err := tc.llmConfigRepo.Create(ctx, cfg); err != nil {
		t.Fatalf("create oauth config: %v", err)
	}
	weeklyPercent := 8.0
	fiveHourPercent := 4.0
	weeklyMinutes := 10080
	fiveHourMinutes := 300
	if err := tc.usageRepo.CreateAccountUsageSnapshot(ctx, &models.AccountUsageSnapshot{
		Provider:               "openai",
		AccountID:              cfg.OAuthAccountID,
		AgentConfigID:          cfg.ID,
		PlanType:               "ChatGPT Pro",
		PrimaryLabel:           "5-hour session",
		PrimaryUsedPercent:     &weeklyPercent,
		PrimaryWindowMinutes:   &weeklyMinutes,
		SecondaryLabel:         "weekly limit",
		SecondaryUsedPercent:   &fiveHourPercent,
		SecondaryWindowMinutes: &fiveHourMinutes,
		RawJSON:                `{"account_id":"raw-provider-account","authorization":"Bearer raw-secret"}`,
		FetchedAt:              time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create account snapshot: %v", err)
	}

	rec := tc.HTTP().Get("/api/analytics/usage?provider=openai&range=all").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
	var view models.AnalyticsUsageViewModel
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode usage API: %v", err)
	}
	if len(view.AccountLimits) != 1 || len(view.AccountLimits[0].Limits) != 2 {
		t.Fatalf("account limits = %+v", view.AccountLimits)
	}
	limits := view.AccountLimits[0].Limits
	if limits[0].LimitKey != "five_hour" || limits[0].Label != "5-hour session" || limits[1].LimitKey != "weekly" || limits[1].Label != "weekly limit" {
		t.Fatalf("public limits are not stable/canonical: %+v", limits)
	}
	body := rec.Body.String()
	for _, secret := range []string{cfg.ID, cfg.OAuthAccountID, cfg.OAuthAccessToken, cfg.OAuthRefreshToken, "raw-provider-account", "Bearer raw-secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("usage API exposed private account/config data %q: %s", secret, body)
		}
	}
}

func TestGetAnalyticsUsage_Range7d(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/analytics/usage?range=7d").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestGetAnalyticsUsage_RangeAll(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/analytics/usage?range=all").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestGetAnalyticsUsage_RangeMonth(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/analytics/usage?range=month&group_by=week").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestGetAnalyticsUsage_WithProjectFilter(t *testing.T) {
	tc := NewTestContext(t)
	p := tc.CreateProject().Build()
	rec := tc.HTTP().Get("/api/analytics/usage?project_id=" + p.ID).Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestGetAnalyticsUsage_WithDateRange(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/analytics/usage?date_from=2024-01-01T00:00:00Z&date_to=2024-12-31T23:59:59Z").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestGetSuccessFailureRates(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/analytics/success-failure-rates").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestGetSuccessFailureRates_GroupByWeek(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/analytics/success-failure-rates?group_by=week").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestGetAvgExecutionTimeByTask(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/analytics/avg-execution-time-by-task").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestGetAvgExecutionTimeByTask_WithLimit(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/analytics/avg-execution-time-by-task?limit=5").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestGetAvgExecutionTimeByAgent(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/analytics/avg-execution-time-by-agent").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestGetExecutionTrendsByHour(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/analytics/execution-trends-by-hour").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestGetAgentUsageByProject(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/analytics/agent-usage-by-project").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestGetMostFrequentTasks(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/analytics/most-frequent-tasks").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestGetMostFrequentTasks_WithLimit(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/analytics/most-frequent-tasks?limit=3").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestGetFailedTaskPatterns(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/api/analytics/failed-task-patterns").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

// --- Pure function unit tests for parseAnalyticsTime ---

func TestParseAnalyticsTime_Empty(t *testing.T) {
	if got := parseAnalyticsTime(""); !got.IsZero() {
		t.Errorf("expected zero time, got %v", got)
	}
}

func TestParseAnalyticsTime_RFC3339(t *testing.T) {
	got := parseAnalyticsTime("2024-01-15T10:00:00Z")
	if got.IsZero() {
		t.Fatal("expected non-zero time")
	}
	if got.Year() != 2024 || got.Month() != 1 || got.Day() != 15 {
		t.Errorf("unexpected date: %v", got)
	}
}

func TestParseAnalyticsTime_DateOnly(t *testing.T) {
	got := parseAnalyticsTime("2024-06-01")
	if got.IsZero() {
		t.Fatal("expected non-zero time")
	}
	if got.Year() != 2024 || got.Month() != 6 {
		t.Errorf("unexpected date: %v", got)
	}
}

func TestParseAnalyticsTime_Invalid(t *testing.T) {
	if got := parseAnalyticsTime("not-a-date"); !got.IsZero() {
		t.Errorf("expected zero time for invalid input, got %v", got)
	}
}

// --- Pure function unit tests for parseUsageFilter ---

func echoContext(rawURL string) echo.Context {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, rawURL, nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec)
}

func TestParseUsageFilter_Defaults(t *testing.T) {
	filter := parseUsageFilter(echoContext("/api/analytics/usage"))
	if filter.GroupBy != "day" {
		t.Errorf("expected default group_by=day, got %q", filter.GroupBy)
	}
	if filter.DateFrom.IsZero() {
		t.Error("expected DateFrom to be set for default 30d range")
	}
}

func TestParseUsageFilter_ExplicitDates(t *testing.T) {
	filter := parseUsageFilter(echoContext("/api/analytics/usage?date_from=2024-01-01&date_to=2024-01-31"))
	if filter.DateFrom.IsZero() {
		t.Error("expected DateFrom to be set")
	}
	if filter.DateTo.IsZero() {
		t.Error("expected DateTo to be set")
	}
}

func TestParseUsageFilter_Refresh(t *testing.T) {
	filter := parseUsageFilter(echoContext("/api/analytics/usage?refresh=true"))
	if !filter.Refresh {
		t.Error("expected Refresh=true")
	}
}

func TestParseUsageFilter_RefreshNumeric(t *testing.T) {
	filter := parseUsageFilter(echoContext("/api/analytics/usage?refresh=1"))
	if !filter.Refresh {
		t.Error("expected Refresh=true for refresh=1")
	}
}

func TestParseUsageFilter_GroupByHour(t *testing.T) {
	filter := parseUsageFilter(echoContext("/api/analytics/usage?group_by=hour&range=7d"))
	if filter.GroupBy != "hour" {
		t.Errorf("expected group_by=hour, got %q", filter.GroupBy)
	}
	diff := filter.DateTo.Sub(filter.DateFrom)
	if diff < 6*24*time.Hour || diff > 8*24*time.Hour {
		t.Errorf("expected ~7d date range, got %v", diff)
	}
}

func TestParseUsageFilter_MonthRange(t *testing.T) {
	filter := parseUsageFilter(echoContext("/api/analytics/usage?range=month"))
	if filter.DateFrom.Day() != 1 {
		t.Errorf("expected DateFrom day=1 for month range, got day=%d", filter.DateFrom.Day())
	}
}

func TestParseUsageFilter_YearRange(t *testing.T) {
	filter := parseUsageFilter(echoContext("/api/analytics/usage?range=365d"))
	diff := filter.DateTo.Sub(filter.DateFrom)
	if diff < 364*24*time.Hour || diff > 366*24*time.Hour {
		t.Errorf("expected ~365d date range, got %v", diff)
	}
}

func TestParseSkillAnalyticsFilter_YearRange(t *testing.T) {
	filter := parseSkillAnalyticsFilter(echoContext("/api/analytics/skills?range=365d&group_by=week"))
	diff := filter.DateTo.Sub(filter.DateFrom)
	if diff < 364*24*time.Hour || diff > 366*24*time.Hour {
		t.Errorf("expected ~365d date range, got %v", diff)
	}
	if filter.GroupBy != "week" {
		t.Errorf("expected group_by=week, got %q", filter.GroupBy)
	}
}

func TestParseSkillAnalyticsFilter_WhitespaceRangeFallsBackToDefault(t *testing.T) {
	filter := parseSkillAnalyticsFilter(echoContext("/api/analytics/skills?range=%207d%20"))
	diff := filter.DateTo.Sub(filter.DateFrom)
	if diff < 29*24*time.Hour || diff > 31*24*time.Hour {
		t.Errorf("expected whitespace-padded range to retain Skill Analytics default 30d behavior, got %v", diff)
	}
}

func TestGetSkillAnalyticsUsesCompactAgentCatalogProjectionAndPreservesEnabledSkills(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM agents WHERE id IS NOT NULL`); err != nil {
		t.Fatalf("clear agents: %v", err)
	}

	projectRepo := repository.NewProjectRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	skillAnalyticsRepo := repository.NewSkillAnalyticsRepo(db)
	globalRoot := filepath.Join(t.TempDir(), "global")
	projectARepo := filepath.Join(t.TempDir(), "project-a")
	projectBRepo := filepath.Join(t.TempDir(), "project-b")
	projectA := &models.Project{Name: "Project A", RepoPath: projectARepo}
	projectB := &models.Project{Name: "Project B", RepoPath: projectBRepo}
	if err := projectRepo.Create(ctx, projectA); err != nil {
		t.Fatalf("create project A: %v", err)
	}
	if err := projectRepo.Create(ctx, projectB); err != nil {
		t.Fatalf("create project B: %v", err)
	}
	projectARoot := filepath.Join(projectARepo, ".openvibely")
	projectBRoot := filepath.Join(projectBRepo, ".openvibely")

	writeStandaloneSkillForAnalyticsTest(t, globalRoot, "global_only", true, false)
	writeStandaloneSkillForAnalyticsTest(t, projectARoot, "project_only", true, true)
	writeAgentSkillForAnalyticsTest(t, globalRoot, "global_agent", "global_agent_skill", true)
	writeAgentSkillForAnalyticsTest(t, projectARoot, "project_agent", "project_agent_skill", true)
	writeAgentSkillForAnalyticsTest(t, projectARoot, "duplicate_a", "shared_agent_skill", true)
	writeAgentSkillForAnalyticsTest(t, projectARoot, "duplicate_b", "shared_agent_skill", true)
	writeAgentSkillForAnalyticsTest(t, projectARoot, "disabled_agent", "disabled_agent_skill", false)
	writeAgentSkillForAnalyticsTest(t, projectBRoot, "other_agent", "other_project_agent_skill", true)
	writeAgentSkillForAnalyticsTest(t, projectARoot, "archived_agent", "archived_agent_skill", true)
	writeAgentSkillForAnalyticsTest(t, projectARoot, "bad/key", "invalid_key_skill", true)

	createAnalyticsAgent(t, agentRepo, "Global Agent", "global_agent", "")
	createAnalyticsAgent(t, agentRepo, "Project Agent", "project_agent", projectA.ID)
	createAnalyticsAgent(t, agentRepo, "Duplicate A", "duplicate_a", projectA.ID)
	createAnalyticsAgent(t, agentRepo, "Duplicate B", "duplicate_b", projectA.ID)
	createAnalyticsAgent(t, agentRepo, "Disabled Agent", "disabled_agent", projectA.ID)
	createAnalyticsAgent(t, agentRepo, "Blank Key", "", projectA.ID)
	createAnalyticsAgent(t, agentRepo, "Invalid Key", "bad/key", projectA.ID)
	createAnalyticsAgent(t, agentRepo, "Other Project Agent", "other_agent", projectB.ID)
	archived := createAnalyticsAgent(t, agentRepo, "Archived Agent", "archived_agent", projectA.ID)
	archived.GeneratedStatus = models.AgentStatusArchived
	if err := agentRepo.Update(ctx, archived); err != nil {
		t.Fatalf("archive agent: %v", err)
	}

	h := &Handler{
		projectRepo:        projectRepo,
		agentRepo:          agentRepo,
		skillAnalyticsRepo: skillAnalyticsRepo,
		agentSkillRoot:     globalRoot,
	}
	e := echo.New()
	e.GET("/api/analytics/skills", h.GetSkillAnalytics)

	counter.Reset()
	counter.SetEnabled(true)
	req := httptest.NewRequest(http.MethodGet, "/api/analytics/skills?project_id="+projectA.ID+"&range=all", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	counter.SetEnabled(false)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetSkillAnalytics status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var dashboard models.SkillAnalyticsDashboard
	if err := json.Unmarshal(rec.Body.Bytes(), &dashboard); err != nil {
		t.Fatalf("decode skill analytics dashboard: %v", err)
	}
	underusedByHandle := map[string][]models.UnderusedSkillMetric{}
	for _, metric := range dashboard.Underused {
		underusedByHandle[metric.SkillHandle] = append(underusedByHandle[metric.SkillHandle], metric)
	}
	for _, handle := range []string{"global_only", "project_only", "global_agent_skill", "project_agent_skill", "shared_agent_skill"} {
		if len(underusedByHandle[handle]) == 0 {
			t.Fatalf("underused output missing enabled skill %q; got handles %v", handle, sortedSkillAnalyticsHandles(underusedByHandle))
		}
	}
	if got := len(underusedByHandle["shared_agent_skill"]); got != 1 {
		t.Fatalf("duplicate agent-owned skill handle rows = %d, want 1", got)
	}
	projectOnly := underusedByHandle["project_only"][0]
	if projectOnly.SkillScope != models.SkillScopeProject || !projectOnly.AlwaysUse || !projectOnly.Enabled {
		t.Fatalf("project skill metric = %+v, want project scope, always use, enabled", projectOnly)
	}
	for _, handle := range []string{"disabled_agent_skill", "other_project_agent_skill", "archived_agent_skill", "invalid_key_skill"} {
		if len(underusedByHandle[handle]) != 0 {
			t.Fatalf("underused output included excluded skill %q: %+v", handle, underusedByHandle[handle])
		}
	}

	var agentStatements []string
	for _, stmt := range counter.Statements() {
		if strings.Contains(strings.ToLower(stmt), "from agents") {
			agentStatements = append(agentStatements, stmt)
		}
	}
	if len(agentStatements) != 1 {
		t.Fatalf("agent statements = %#v, want exactly one compact enabled-skill lookup", agentStatements)
	}
	stmt := strings.ToLower(agentStatements[0])
	projection := strings.Split(stmt, "from agents")[0]
	for _, required := range []string{"select id", "coalesce(key, '')", "project_id"} {
		if !strings.Contains(projection, required) {
			t.Fatalf("analytics agent projection = %q, want %q in %s", projection, required, agentStatements[0])
		}
	}
	for _, forbidden := range []string{"system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("analytics enabled-skill lookup selected forbidden column %q: %s", forbidden, agentStatements[0])
		}
	}
}

func writeStandaloneSkillForAnalyticsTest(t *testing.T, root, handle string, enabled, alwaysUse bool) {
	t.Helper()
	indexPath := filepath.Join(root, "skills", "SKILLS.md")
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		t.Fatalf("create standalone skill index dir: %v", err)
	}
	index := ""
	if alwaysUse {
		index = "---\nalways_use:\n  - " + handle + "\n---\n\n"
	}
	index += "## " + handle + "\n"
	if err := os.WriteFile(indexPath, []byte(index), 0o644); err != nil {
		t.Fatalf("write standalone skill index: %v", err)
	}
	skillDir := filepath.Join(root, "skills", handle)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("create standalone skill dir: %v", err)
	}
	body := analyticsSkillBody(handle, enabled)
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write standalone skill body: %v", err)
	}
}

func writeAgentSkillForAnalyticsTest(t *testing.T, root, agentKey, handle string, enabled bool) {
	t.Helper()
	indexPath := filepath.Join(root, "agents", agentKey, "SKILLS.md")
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		t.Fatalf("create agent skill index dir: %v", err)
	}
	if err := os.WriteFile(indexPath, []byte("## "+agentKey+"/"+handle+"\n"), 0o644); err != nil {
		t.Fatalf("write agent skill index: %v", err)
	}
	skillDir := filepath.Join(root, "agents", agentKey, "skills", handle)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("create agent skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(analyticsSkillBody(handle, enabled)), 0o644); err != nil {
		t.Fatalf("write agent skill body: %v", err)
	}
}

func analyticsSkillBody(handle string, enabled bool) string {
	return "---\nskill:\n  key: " + handle + "\n  enabled: " + map[bool]string{true: "true", false: "false"}[enabled] + "\n---\n\n# " + handle + "\n"
}

func createAnalyticsAgent(t *testing.T, repo *repository.AgentRepo, name, key, projectID string) *models.Agent {
	t.Helper()
	agent := &models.Agent{
		Name:         name,
		Description:  "analytics skill catalog fixture",
		SystemPrompt: strings.Repeat("large analytics prompt ", 256),
		Model:        "inherit",
		Tools:        []string{"Read", models.AgentToolScopedFiles},
		ToolConfig: models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{
			Directory:   "src",
			Permissions: []string{"read", "write"},
		}}},
		Plugins: []string{"github@marketplace"},
		MCPServers: []models.MCPServerConfig{{
			Name:    "playwright",
			Command: []string{"npx", "server"},
		}},
		Skills: []models.SkillConfig{{
			Name:    "legacy",
			Content: strings.Repeat("legacy body ", 128),
		}},
		Key:                 key,
		ProjectID:           projectID,
		PermissionDefaults:  models.AgentPermissionDefaults{ReadAgents: true, ReadSkills: true},
		ModelDefaults:       models.AgentModelDefaults{Model: "gpt-5"},
		SourceRefs:          []string{"agents/fixture/SKILLS.md"},
		Enabled:             true,
		SelectableAsPrimary: true,
	}
	if projectID != "" {
		agent.Scope = models.AgentScopeProject
	}
	if err := repo.Create(context.Background(), agent); err != nil {
		t.Fatalf("create analytics agent %q: %v", name, err)
	}
	return agent
}

func sortedSkillAnalyticsHandles(metrics map[string][]models.UnderusedSkillMetric) []string {
	handles := make([]string, 0, len(metrics))
	for handle := range metrics {
		handles = append(handles, handle)
	}
	sort.Strings(handles)
	return handles
}

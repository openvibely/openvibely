package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/database"
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

func TestAnalyticsSupportingEvidenceRequiresProject(t *testing.T) {
	tc := NewTestContext(t)
	usage := tc.HTTP().Get("/api/analytics/usage?usage_period=2026-01-10").Execute()
	tc.Assert(usage).StatusCode(http.StatusBadRequest)
	skill := tc.HTTP().Get("/api/analytics/skills?skill_period=2026-01-10&skill_event=created").Execute()
	tc.Assert(skill).StatusCode(http.StatusBadRequest)
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

func TestGetAnalyticsDashboardYieldsSoleReaderForConcurrentRequest(t *testing.T) {
	connections, err := database.NewReadWrite(filepath.Join(t.TempDir(), "analytics-contention.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connections.Close()
	if got := connections.Reader.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("reader max connections = %d, want production topology of 1", got)
	}
	if got := connections.Writer.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("writer max connections = %d, want production topology of 1", got)
	}
	if _, err := connections.Writer.Exec(`
		INSERT INTO projects(id,name) VALUES ('analytics-project','Analytics project');
		INSERT INTO tasks(id,project_id,title,category,status,created_at)
			VALUES ('analytics-task','analytics-project','Analytics task','backlog','completed',CURRENT_TIMESTAMP);
		WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<250000)
		INSERT INTO executions(id,task_id,status,started_at,completed_at,is_followup,history_order)
		SELECT printf('analytics-exec-%06d',n),'analytics-task',
			CASE WHEN n%3=0 THEN 'failed' ELSE 'completed' END,
			CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CASE WHEN n=1 THEN 0 ELSE 1 END,n
		FROM seq;
	`); err != nil {
		t.Fatal(err)
	}

	h := &Handler{execRepo: repository.NewExecutionRepo(connections.Reader)}
	e := echo.New()
	e.GET("/api/analytics/dashboard", h.GetAnalyticsDashboard)
	e.GET("/probe", func(c echo.Context) error {
		var count int
		if err := connections.Reader.QueryRowContext(c.Request().Context(), `SELECT COUNT(*) FROM projects`).Scan(&count); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]int{"projects": count})
	})
	server := httptest.NewServer(e)
	defer server.Close()

	analyticsCtx, cleanupAnalyticsRequest := context.WithCancel(context.Background())
	defer cleanupAnalyticsRequest()
	analyticsReq, err := http.NewRequestWithContext(analyticsCtx, http.MethodGet, server.URL+"/api/analytics/dashboard?project_id=analytics-project&view=overview&range=all", nil)
	if err != nil {
		t.Fatal(err)
	}
	analyticsDone := make(chan error, 1)
	go func() {
		response, requestErr := server.Client().Do(analyticsReq)
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if requestErr == nil && response.StatusCode != http.StatusOK {
				requestErr = fmt.Errorf("Analytics status = %d", response.StatusCode)
			}
		}
		analyticsDone <- requestErr
	}()

	deadline := time.Now().Add(2 * time.Second)
	for connections.Reader.Stats().InUse != 1 {
		select {
		case requestErr := <-analyticsDone:
			t.Fatalf("Analytics request completed before occupying the sole reader: %v", requestErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("Analytics request did not occupy the sole reader")
		}
		time.Sleep(time.Millisecond)
	}

	probeDone := make(chan error, 1)
	probeStarted := time.Now()
	waitCountBeforeProbe := connections.Reader.Stats().WaitCount
	go func() {
		response, requestErr := server.Client().Get(server.URL + "/probe")
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if requestErr == nil && response.StatusCode != http.StatusOK {
				requestErr = fmt.Errorf("probe status = %d", response.StatusCode)
			}
		}
		probeDone <- requestErr
	}()

	deadline = time.Now().Add(time.Second)
	for connections.Reader.Stats().WaitCount <= waitCountBeforeProbe {
		select {
		case err := <-probeDone:
			t.Fatalf("concurrent request completed before contending for the Analytics reader: %v", err)
		case err := <-analyticsDone:
			t.Fatalf("Analytics request completed before cancellation: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("concurrent request did not queue behind the Analytics reader")
		}
		time.Sleep(time.Millisecond)
	}

	select {
	case err := <-probeDone:
		if err != nil {
			t.Fatalf("concurrent request while Analytics remained active: %v", err)
		}
		if elapsed := time.Since(probeStarted); elapsed > time.Second {
			t.Fatalf("concurrent request waited %s while Analytics remained active; want <= 1s", elapsed)
		}
		if err := analyticsCtx.Err(); err != nil {
			t.Fatalf("Analytics context was cancelled while the concurrent request completed: %v", err)
		}
		select {
		case err := <-analyticsDone:
			t.Fatalf("Analytics completed before the concurrent request could be observed: %v", err)
		default:
		}
	case err := <-analyticsDone:
		t.Fatalf("Analytics completed before the contending request: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent request remained blocked while Analytics was active")
	}
	select {
	case err := <-analyticsDone:
		if err != nil {
			t.Fatalf("Analytics did not resume after yielding the reader: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Analytics did not complete after yielding the reader")
	}
}

func TestGetAnalyticsDashboardRequiresProjectAndReturnsDefinitions(t *testing.T) {
	tc := NewTestContext(t)
	missing := tc.HTTP().Get("/api/analytics/dashboard?range=30d").Execute()
	tc.Assert(missing).StatusCode(http.StatusBadRequest)

	project := tc.CreateProject().Build()
	rec := tc.HTTP().Get("/api/analytics/dashboard?project_id=" + project.ID + "&range=all&compare=1&evidence_limit=1&evidence_offset=2").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
	var dashboard models.AnalyticsDashboard
	if err := json.Unmarshal(rec.Body.Bytes(), &dashboard); err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}
	if len(dashboard.Definitions) < 6 {
		t.Fatalf("definitions = %d, want centralized metric definitions", len(dashboard.Definitions))
	}
	if dashboard.Previous != nil {
		t.Fatalf("all-time dashboard must omit nonsensical previous comparison: %+v", dashboard.Previous)
	}
	if dashboard.Agents == nil || dashboard.Workflows == nil || dashboard.RecentOutcomes == nil || dashboard.AgentSkillOutcomes == nil {
		t.Fatalf("empty dashboard collections must encode as arrays: %+v", dashboard)
	}
	if dashboard.EvidenceLimit != 1 || dashboard.EvidenceOffset != 2 {
		t.Fatalf("evidence pagination was not parsed: limit=%d offset=%d", dashboard.EvidenceLimit, dashboard.EvidenceOffset)
	}
}

func TestGetAnalyticsDashboardFiltersScopedSkillOutcomeEvidence(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().Build()
	config := tc.CreateLLMConfig().WithProvider(models.ProviderTest).WithModel("test-model").Build()
	target := tc.CreateTask(project.ID).WithTitle("Project skill outcome").Build()
	other := tc.CreateTask(project.ID).WithTitle("Global skill outcome").Build()

	for _, task := range []*models.Task{target, other} {
		execution := &models.Execution{TaskID: task.ID, AgentConfigID: config.ID, Status: models.ExecRunning, PromptSent: "analytics evidence"}
		if err := tc.execRepo.Create(ctx, execution); err != nil {
			t.Fatalf("create execution for %s: %v", task.ID, err)
		}
		if err := tc.execRepo.Complete(ctx, execution.ID, models.ExecCompleted, "done", "", 1, 1); err != nil {
			t.Fatalf("complete execution for %s: %v", task.ID, err)
		}
	}

	skillRepo := repository.NewSkillAnalyticsRepo(tc.db)
	for _, event := range []*models.SkillAnalyticsEvent{
		{ProjectID: project.ID, TaskID: target.ID, SkillHandle: "shared:evaluator", SkillScope: models.SkillScopeProject, EventType: models.SkillEventSelected},
		{ProjectID: project.ID, TaskID: other.ID, SkillHandle: "shared:evaluator", SkillScope: models.SkillScopeGlobal, EventType: models.SkillEventSelected},
	} {
		if err := skillRepo.RecordEvent(ctx, event); err != nil {
			t.Fatalf("record skill event: %v", err)
		}
	}

	rec := tc.HTTP().Get("/api/analytics/dashboard?project_id=" + project.ID + "&range=all&evidence_skill_handle=shared:evaluator&evidence_skill_scope=project").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
	var dashboard models.AnalyticsDashboard
	if err := json.Unmarshal(rec.Body.Bytes(), &dashboard); err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}
	if dashboard.EvidenceTotal != 1 || len(dashboard.RecentOutcomes) != 1 || dashboard.RecentOutcomes[0].TaskID != target.ID {
		t.Fatalf("scoped skill outcome evidence = total %d rows %+v, want only %s", dashboard.EvidenceTotal, dashboard.RecentOutcomes, target.ID)
	}
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

func TestGetMostFrequentTasks_StableTieBreakAndFullHistory(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Analytics project").Build()
	agent := &models.LLMConfig{
		Name:     "Analytics agent",
		Provider: models.ProviderAnthropic,
		Model:    "claude-3-5-sonnet-20241022",
	}
	if err := tc.llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	createTask := func(title string) *models.Task {
		t.Helper()
		task := &models.Task{
			ProjectID: project.ID,
			Title:     title,
			Category:  models.CategoryActive,
			Status:    models.StatusPending,
			Prompt:    "analytics test",
		}
		if err := tc.taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create task %q: %v", title, err)
		}
		return task
	}
	createExecutions := func(taskID string, count int) {
		t.Helper()
		for i := 0; i < count; i++ {
			if err := tc.execRepo.Create(ctx, &models.Execution{
				TaskID:        taskID,
				AgentConfigID: agent.ID,
				Status:        models.ExecCompleted,
				PromptSent:    "analytics test",
			}); err != nil {
				t.Fatalf("create execution for %s: %v", taskID, err)
			}
		}
	}

	mostFrequent := createTask("Most frequent")
	tieA := createTask("Tie A")
	tieB := createTask("Tie B")
	createExecutions(mostFrequent.ID, 3)
	createExecutions(tieA.ID, 1)
	createExecutions(tieB.ID, 1)

	firstTieID, secondTieID := tieA.ID, tieB.ID
	if firstTieID > secondTieID {
		firstTieID, secondTieID = secondTieID, firstTieID
	}

	bounded := tc.HTTP().Get("/api/analytics/most-frequent-tasks?project_id=" + project.ID + "&limit=2").Execute()
	tc.Assert(bounded).StatusCode(http.StatusOK)
	var boundedRows []repository.TaskFrequency
	if err := json.Unmarshal(bounded.Body.Bytes(), &boundedRows); err != nil {
		t.Fatalf("decode bounded response: %v", err)
	}
	if len(boundedRows) != 2 || boundedRows[0].TaskID != mostFrequent.ID || boundedRows[1].TaskID != firstTieID {
		t.Fatalf("bounded rows = %+v, want most frequent then stable tie %s", boundedRows, firstTieID)
	}

	full := tc.HTTP().Get("/api/analytics/most-frequent-tasks?project_id=" + project.ID + "&limit=0").Execute()
	tc.Assert(full).StatusCode(http.StatusOK)
	var fullRows []repository.TaskFrequency
	if err := json.Unmarshal(full.Body.Bytes(), &fullRows); err != nil {
		t.Fatalf("decode full-history response: %v", err)
	}
	if len(fullRows) != 3 || fullRows[1].TaskID != firstTieID || fullRows[2].TaskID != secondTieID {
		t.Fatalf("full-history rows = %+v, want stable ties [%s, %s]", fullRows, firstTieID, secondTieID)
	}
}

func TestGetMostFrequentTasks_RealFiveThousandRecordResponseBoundary(t *testing.T) {
	const fixtureSize = 5000
	ctx := context.Background()
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Large analytics project").Build()
	agent := &models.LLMConfig{
		Name:     "Analytics evidence agent",
		Provider: models.ProviderAnthropic,
		Model:    "claude-3-5-sonnet-20241022",
	}
	if err := tc.llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	tx, err := tc.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin fixture transaction: %v", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()
	for i := 0; i < fixtureSize; i++ {
		taskID := fmt.Sprintf("analytics-large-%05d", i)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tasks (id, project_id, title, prompt) VALUES (?, ?, ?, '')`,
			taskID, project.ID, fmt.Sprintf("task title %05d", i)); err != nil {
			t.Fatalf("insert task %d: %v", i, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO executions (id, task_id, agent_config_id, status, prompt_sent, started_at, completed_at) VALUES (?, ?, ?, ?, '', ?, ?)`,
			fmt.Sprintf("execution-large-%05d", i), taskID, agent.ID, models.ExecCompleted, "2026-09-13 12:34:56", "2026-09-13 12:34:56"); err != nil {
			t.Fatalf("insert execution %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}
	rollback = false

	request := func(limit int) ([]repository.TaskFrequency, int) {
		t.Helper()
		path := fmt.Sprintf("/api/analytics/most-frequent-tasks?project_id=%s&limit=%d", project.ID, limit)
		rec := tc.HTTP().Get(path).Execute()
		tc.Assert(rec).StatusCode(http.StatusOK)
		var rows []repository.TaskFrequency
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatalf("decode limit=%d response: %v", limit, err)
		}
		return rows, len(rec.Body.Bytes())
	}

	boundedRows, boundedBytes := request(12)
	fullRows, fullBytes := request(0)
	if len(boundedRows) != 12 {
		t.Fatalf("bounded real-backend rows = %d, want 12", len(boundedRows))
	}
	if len(fullRows) != fixtureSize {
		t.Fatalf("full real-backend rows = %d, want %d", len(fullRows), fixtureSize)
	}
	for i, row := range boundedRows {
		wantID := fmt.Sprintf("analytics-large-%05d", i)
		if row.TaskID != wantID || row.ExecutionCount != 1 {
			t.Fatalf("bounded row %d = %+v, want task %s with count 1", i, row, wantID)
		}
	}
	if boundedBytes >= fullBytes {
		t.Fatalf("bounded real-backend response bytes = %d, full = %d", boundedBytes, fullBytes)
	}
	t.Logf("real_frequent_analytics_boundary fixture=%d bounded_limit=12 bounded_response_bytes=%d bounded_rows=%d full_limit=0 full_response_bytes=%d full_rows=%d", fixtureSize, boundedBytes, len(boundedRows), fullBytes, len(fullRows))
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

func TestParseUsageAndSkillFiltersIncludeAnalyticsDimensions(t *testing.T) {
	usage := parseUsageFilter(echoContext("/api/analytics/usage?agent=agent-1&workflow=workflow-1"))
	if usage.AgentID != "agent-1" || usage.WorkflowID != "workflow-1" {
		t.Fatalf("usage dimensions = %+v", usage)
	}
	skill := parseSkillAnalyticsFilter(echoContext("/api/analytics/skills?agent=agent-1&workflow=workflow-1"))
	if skill.AgentID != "agent-1" || skill.WorkflowID != "workflow-1" {
		t.Fatalf("skill dimensions = %+v", skill)
	}
}

func TestParseAnalyticsSupportingEvidenceFilters(t *testing.T) {
	usage := parseUsageFilter(echoContext("/api/analytics/usage?usage_period=2026-01-10&usage_provider=openai&usage_model_name=gpt-a"))
	if usage.EvidencePeriod != "2026-01-10" || usage.EvidenceProvider != "openai" || usage.EvidenceModel != "gpt-a" || usage.EvidenceLimit != 50 {
		t.Fatalf("usage evidence filters = %+v", usage)
	}
	skill := parseSkillAnalyticsFilter(echoContext("/api/analytics/skills?skill_period=2026-01-10&skill_event=created&skill_agent=agent-1&skill_handle=project%3Areview&skill_evidence_scope=project"))
	if skill.EvidencePeriod != "2026-01-10" || skill.EvidenceEvent != "created" || skill.EvidenceAgentID != "agent-1" || skill.EvidenceSkillHandle != "project:review" || skill.EvidenceSkillScope != "project" || skill.EvidenceLimit != 50 {
		t.Fatalf("skill evidence filters = %+v", skill)
	}
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

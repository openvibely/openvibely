package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

func setupSuggestionsTestHandler(t *testing.T) (*Handler, *echo.Echo, *repository.ProjectRepo, *repository.TaskRepo) {
	t.Helper()
	db := testutil.NewTestDB(t)

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerRepo := repository.NewWorkerRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	alertRepo := repository.NewAlertRepo(db)
	upcomingRepo := repository.NewUpcomingRepo(db)
	insightsRepo := repository.NewInsightsRepo(db)
	backlogRepo := repository.NewBacklogRepo(db)

	projectSvc := service.NewProjectService(projectRepo)
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	mockCaller := testutil.NewMockLLMCaller()
	mockCaller.Response = `{"grade":"B","next_grade":"B+","summary":"Good idea quality","strengths":"Solid execution","improvements":"Improve prioritization","assessment":"Overall healthy","how_to_improve":"Keep shipping","how_to_next_grade":"Tighten acceptance criteria","clarity_score":82,"ambition_score":78,"follow_through":70,"diversity_score":65,"strategy_score":74}`
	llmSvc.SetLLMCaller(mockCaller)

	defaultAgent := &models.LLMConfig{
		Name:      "Suggestions Test Agent",
		Provider:  models.ProviderTest,
		Model:     "test-model",
		MaxTokens: 4096,
		IsDefault: true,
	}
	if err := llmConfigRepo.Create(context.Background(), defaultAgent); err != nil {
		t.Fatalf("failed to create default test agent: %v", err)
	}
	workerSvc := service.NewWorkerService(llmSvc, 0, nil)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	alertSvc := service.NewAlertService(alertRepo, nil)
	upcomingSvc := service.NewUpcomingService(upcomingRepo)
	insightsSvc := service.NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)
	insightsSvc.SetLLMService(llmSvc)
	backlogSvc := service.NewBacklogService(backlogRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

	h := New(projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, alertSvc, upcomingSvc, nil, nil, insightsSvc, nil, backlogSvc, nil, nil, nil, nil, llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, nil, nil, nil, nil)

	e := echo.New()
	h.RegisterRoutes(e)

	return h, e, projectRepo, taskRepo
}

func TestUnifiedSuggestions_FullPage(t *testing.T) {
	_, e, projectRepo, _ := setupSuggestionsTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := models.Project{Name: "Test Project", RepoPath: "/tmp/test"}
	err := projectRepo.Create(ctx, &project)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Full page load
	req := httptest.NewRequest(http.MethodGet, "/suggestions?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if body == "" {
		t.Error("response body is empty")
	}

	// Check it contains both sections
	if !containsStr(body, "Suggestions") {
		t.Error("expected page title 'Suggestions'")
	}
	if !containsStr(body, "Codebase Insights") {
		t.Error("expected 'Codebase Insights' section")
	}
	if !containsStr(body, "Backlog Suggestions") {
		t.Error("expected 'Backlog Suggestions' section")
	}
}

func TestUnifiedSuggestions_HTMXRequest(t *testing.T) {
	_, e, projectRepo, _ := setupSuggestionsTestHandler(t)
	ctx := context.Background()

	project := models.Project{Name: "Test Project", RepoPath: "/tmp/test"}
	err := projectRepo.Create(ctx, &project)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// HTMX request
	req := httptest.NewRequest(http.MethodGet, "/suggestions?project_id="+project.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !containsStr(body, "Codebase Insights") {
		t.Error("expected 'Codebase Insights' section in HTMX response")
	}
	if !containsStr(body, "Backlog Suggestions") {
		t.Error("expected 'Backlog Suggestions' section in HTMX response")
	}
}

func TestUnifiedSuggestions_NoProject(t *testing.T) {
	_, e, _, _ := setupSuggestionsTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/suggestions", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestUnifiedSuggestions_CombinedAnalysis(t *testing.T) {
	_, e, projectRepo, _ := setupSuggestionsTestHandler(t)
	ctx := context.Background()

	project := models.Project{Name: "Test Project", RepoPath: "/tmp/test"}
	err := projectRepo.Create(ctx, &project)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Test combined analysis endpoint (will fail without LLM but should return error page)
	req := httptest.NewRequest(http.MethodPost, "/suggestions/analyze?project_id="+project.ID+"&type=all", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestUnifiedSuggestions_InsightsOnlyAnalysis(t *testing.T) {
	_, e, projectRepo, _ := setupSuggestionsTestHandler(t)
	ctx := context.Background()

	project := models.Project{Name: "Test Project", RepoPath: "/tmp/test"}
	err := projectRepo.Create(ctx, &project)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/suggestions/analyze?project_id="+project.ID+"&type=insights", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestUnifiedSuggestions_BacklogOnlyAnalysis(t *testing.T) {
	_, e, projectRepo, _ := setupSuggestionsTestHandler(t)
	ctx := context.Background()

	project := models.Project{Name: "Test Project", RepoPath: "/tmp/test"}
	err := projectRepo.Create(ctx, &project)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/suggestions/analyze?project_id="+project.ID+"&type=backlog", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestUnifiedSuggestions_ExistingEndpointsStillWork(t *testing.T) {
	_, e, projectRepo, _ := setupSuggestionsTestHandler(t)
	ctx := context.Background()

	project := models.Project{Name: "Test Project", RepoPath: "/tmp/test"}
	err := projectRepo.Create(ctx, &project)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// /insights should still serve the standalone grades page
	req := httptest.NewRequest(http.MethodGet, "/insights?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected /insights to return 200, got %d", rec.Code)
	}
	insightsBody := rec.Body.String()
	assertInsightsGradeLabelsUpdated(t, insightsBody)
	if containsStr(insightsBody, "Run Analysis") {
		t.Error("expected /insights not to contain 'Run Analysis'")
	}
	if containsStr(insightsBody, "Extract Knowledge") {
		t.Error("expected /insights not to contain 'Extract Knowledge'")
	}
	if containsStr(insightsBody, "View Reports") {
		t.Error("expected /insights not to contain 'View Reports'")
	}
	if containsStr(insightsBody, "Bug Patterns") {
		t.Error("expected /insights not to contain 'Bug Patterns'")
	}
	if containsStr(insightsBody, "Knowledge Base") {
		t.Error("expected /insights not to contain 'Knowledge Base'")
	}

	// /suggestions should continue to contain analysis, filters, and knowledge/report controls
	req = httptest.NewRequest(http.MethodGet, "/suggestions?project_id="+project.ID, nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected /suggestions to return 200, got %d", rec.Code)
	}
	suggestionsBody := rec.Body.String()
	if !containsStr(suggestionsBody, "Run Codebase Analysis") {
		t.Error("expected /suggestions to contain 'Run Codebase Analysis'")
	}
	if !containsStr(suggestionsBody, "Extract Knowledge") {
		t.Error("expected /suggestions to contain 'Extract Knowledge'")
	}
	if !containsStr(suggestionsBody, "Bug Patterns") {
		t.Error("expected /suggestions to contain 'Bug Patterns'")
	}
	if !containsStr(suggestionsBody, "Knowledge Base") {
		t.Error("expected /suggestions to contain 'Knowledge Base'")
	}
	if !containsStr(suggestionsBody, "Insight Reports") {
		t.Error("expected /suggestions to contain 'Insight Reports'")
	}

	// /backlog should still serve the standalone backlog page
	req = httptest.NewRequest(http.MethodGet, "/backlog?project_id="+project.ID, nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected /backlog to return 200, got %d", rec.Code)
	}
}

func TestUnifiedSuggestions_InsightsLabels_InitialState(t *testing.T) {
	_, e, projectRepo, _ := setupSuggestionsTestHandler(t)
	ctx := context.Background()

	project := models.Project{Name: "Insights Initial State", RepoPath: "/tmp/test"}
	err := projectRepo.Create(ctx, &project)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/insights?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected /insights to return 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	assertInsightsGradeLabelsUpdated(t, body)
}

func TestUnifiedSuggestions_InsightsLabels_WithAnalysisState(t *testing.T) {
	_, e, projectRepo, taskRepo := setupSuggestionsTestHandler(t)
	ctx := context.Background()

	project := models.Project{Name: "Insights Analysis State", RepoPath: "/tmp/test"}
	err := projectRepo.Create(ctx, &project)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Analyze task",
		Category:  models.CategoryActive,
		Status:    models.StatusCompleted,
		Priority:  2,
		Prompt:    "Evaluate task quality",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/insights/health-check?project_id="+project.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected /insights/health-check to return 200, got %d", rec.Code)
	}
	if !containsStr(rec.Body.String(), "What You're Doing Well") {
		t.Fatal("expected health-check refresh to render analysis content")
	}

	req = httptest.NewRequest(http.MethodPost, "/history/grade-ideas?project_id="+project.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected /history/grade-ideas to return 200, got %d", rec.Code)
	}
	if !containsStr(rec.Body.String(), "Tasks Evaluated") {
		t.Fatal("expected idea-grade refresh to render analysis content")
	}

	req = httptest.NewRequest(http.MethodGet, "/insights?project_id="+project.ID, nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected /insights to return 200 after analysis, got %d", rec.Code)
	}

	body := rec.Body.String()
	assertInsightsGradeLabelsUpdated(t, body)
	if !containsStr(body, "Refresh Analysis") {
		t.Error("expected /insights to show 'Refresh Analysis' after analysis runs")
	}
}

func TestUnifiedSuggestions_TabNavigation(t *testing.T) {
	_, e, projectRepo, _ := setupSuggestionsTestHandler(t)
	ctx := context.Background()

	project := models.Project{Name: "Test Project", RepoPath: "/tmp/test"}
	err := projectRepo.Create(ctx, &project)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/suggestions?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Verify tab navigation structure exists
	if !containsStr(body, "suggestions-tabs") {
		t.Error("expected tab navigation container 'suggestions-tabs'")
	}

	// Verify all three tab buttons exist
	if !containsStr(body, `data-tab="insights"`) {
		t.Error("expected insights tab button")
	}
	if !containsStr(body, `data-tab="backlog"`) {
		t.Error("expected backlog tab button")
	}
	if !containsStr(body, `data-tab="knowledge"`) {
		t.Error("expected knowledge tab button")
	}

	// Verify tab panels exist
	if !containsStr(body, `id="tab-insights"`) {
		t.Error("expected insights tab panel")
	}
	if !containsStr(body, `id="tab-backlog"`) {
		t.Error("expected backlog tab panel")
	}
	if !containsStr(body, `id="tab-knowledge"`) {
		t.Error("expected knowledge tab panel")
	}

	// Verify tab switching function is included
	if !containsStr(body, "switchSuggestionsTab") {
		t.Error("expected switchSuggestionsTab function")
	}

	// Verify Knowledge & Reports tab label
	if !containsStr(body, "Knowledge") {
		t.Error("expected 'Knowledge' in tab labels")
	}
}

func assertInsightsGradeLabelsUpdated(t *testing.T, body string) {
	t.Helper()
	if !containsStr(body, `<h3 class="card-title text-xl">Project Management</h3>`) {
		t.Error("expected page to contain heading 'Project Management'")
	}
	if !containsStr(body, `<h3 class="card-title text-xl">Ideas</h3>`) {
		t.Error("expected page to contain heading 'Ideas'")
	}
	if containsStr(body, `<h3 class="card-title text-xl">Project Management Grade</h3>`) {
		t.Error("expected page not to contain heading 'Project Management Grade'")
	}
	if containsStr(body, `<h3 class="card-title text-xl">Idea Grade</h3>`) {
		t.Error("expected page not to contain heading 'Idea Grade'")
	}
	if containsStr(body, "How Am I Doing?") {
		t.Error("expected page not to contain 'How Am I Doing?'")
	}
	if containsStr(body, "Idea Quality Grade") {
		t.Error("expected page not to contain 'Idea Quality Grade'")
	}
}

func containsStr(s, sub string) bool {
	return strings.Contains(s, sub)
}

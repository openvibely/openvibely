package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

// seedPatternBuiltins creates built-in patterns for testing. The migration seeds
// these via INSERT...SELECT from projects, but in tests no projects exist at
// migration time so we need to seed manually.
func seedPatternBuiltins(t *testing.T, repo *repository.PatternRepo, projectID string) {
	t.Helper()
	ctx := context.Background()
	builtins := []struct {
		title    string
		category models.PatternCategory
	}{
		{"Debug Runtime Error", models.CategoryDebugging},
		{"Debug Performance Issue", models.CategoryDebugging},
		{"Write Unit Tests", models.CategoryTesting},
		{"Write Integration Tests", models.CategoryTesting},
		{"Extract Method", models.CategoryRefactoring},
		{"Simplify Logic", models.CategoryRefactoring},
		{"Generate API Docs", models.CategoryDocumentation},
		{"Code Review Checklist", models.CategoryCodeReview},
		{"Optimize Query", models.CategoryOptimization},
		{"Implement Feature", models.CategoryFeature},
	}
	for _, b := range builtins {
		p := &models.PromptPattern{
			ProjectID:    projectID,
			Title:        b.title,
			Description:  "Built-in: " + b.title,
			TemplateText: "Template for " + b.title + " {{var}}",
			Variables:    `["var"]`,
			Category:     b.category,
			IsBuiltin:    true,
			Tags:         "[]",
		}
		if err := repo.Create(ctx, p); err != nil {
			t.Fatalf("seed built-in pattern %q: %v", b.title, err)
		}
	}
}

func setupPatternTestHandler(t *testing.T) (*Handler, *echo.Echo, *models.Project) {
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
	patternRepo := repository.NewPatternRepo(db)

	projectSvc := service.NewProjectService(projectRepo)
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := service.NewWorkerService(llmSvc, 0, nil)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	alertSvc := service.NewAlertService(alertRepo, nil)
	upcomingSvc := service.NewUpcomingService(upcomingRepo)
	patternSvc := service.NewPatternService(patternRepo, taskRepo)

	h := New(projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, alertSvc, upcomingSvc, nil, nil, nil, nil, nil, nil, nil, nil, patternSvc, llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, nil, nil, nil, nil)

	e := echo.New()
	h.RegisterRoutes(e)

	// Get the default project (created by migration 001)
	ctx := context.Background()
	projects, err := projectSvc.List(ctx)
	if err != nil {
		t.Fatalf("failed to list projects: %v", err)
	}
	if len(projects) == 0 {
		t.Fatal("no default project found")
	}
	project := &projects[0]

	// Seed built-in patterns (migration INSERT...SELECT finds no projects at
	// migration time in test DBs, so no built-ins are seeded automatically)
	seedPatternBuiltins(t, patternRepo, project.ID)

	return h, e, project
}

func TestHandler_PatternsPage(t *testing.T) {
	_, e, project := setupPatternTestHandler(t)

	// Request patterns page
	req := httptest.NewRequest(http.MethodGet, "/patterns?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Should have built-in patterns in response
	body := rec.Body.String()
	if !strings.Contains(body, "pattern") {
		t.Error("expected patterns in response")
	}
}

func TestHandler_PatternsPage_HTMX(t *testing.T) {
	_, e, project := setupPatternTestHandler(t)

	// Request patterns page via HTMX
	req := httptest.NewRequest(http.MethodGet, "/patterns?project_id="+project.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestHandler_CreatePattern(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Create form data
	form := url.Values{}
	form.Set("project_id", project.ID)
	form.Set("title", "Handler Test Pattern")
	form.Set("description", "Test description")
	form.Set("template_text", "Fix {{issue}} in {{file}}")
	form.Set("category", string(models.CategoryDebugging))
	form.Set("tags", "test,handler")

	req := httptest.NewRequest(http.MethodPost, "/patterns", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify pattern was created
	patterns, err := h.patternSvc.Search(ctx, project.ID, "Handler Test Pattern")
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	if len(patterns) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(patterns))
	}
	if patterns[0].Title != "Handler Test Pattern" {
		t.Errorf("expected title='Handler Test Pattern', got %q", patterns[0].Title)
	}
}

func TestHandler_CreatePattern_MissingProjectID(t *testing.T) {
	_, e, _ := setupPatternTestHandler(t)

	// Create form without project_id
	form := url.Values{}
	form.Set("title", "Test")
	form.Set("description", "Test")
	form.Set("template_text", "Test")
	form.Set("category", string(models.CategoryCustom))

	req := httptest.NewRequest(http.MethodPost, "/patterns", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestHandler_GetPattern(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    project.ID,
		Title:        "Get Handler Test",
		Description:  "Test",
		TemplateText: "Test {{var}}",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	h.patternSvc.CreatePattern(ctx, pattern)

	// Get pattern
	req := httptest.NewRequest(http.MethodGet, "/patterns/"+pattern.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Should contain pattern data
	body := rec.Body.String()
	if !strings.Contains(body, "Get Handler Test") {
		t.Error("expected pattern title in response")
	}
}

func TestHandler_GetPattern_NotFound(t *testing.T) {
	_, e, _ := setupPatternTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/patterns/nonexistent", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}
}

func TestHandler_UpdatePattern(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    project.ID,
		Title:        "Update Handler Test",
		Description:  "Original",
		TemplateText: "Original {{var}}",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	h.patternSvc.CreatePattern(ctx, pattern)

	// Update pattern
	form := url.Values{}
	form.Set("project_id", project.ID)
	form.Set("title", "Updated Title")
	form.Set("description", "Updated description")
	form.Set("template_text", "Updated {{new_var}}")
	form.Set("category", string(models.CategoryRefactoring))
	form.Set("tags", "updated")

	req := httptest.NewRequest(http.MethodPut, "/patterns/"+pattern.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify update
	updated, _ := h.patternSvc.GetPattern(ctx, pattern.ID)
	if updated.Title != "Updated Title" {
		t.Errorf("expected title='Updated Title', got %q", updated.Title)
	}
}

func TestHandler_UpdatePattern_BuiltinProtection(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Get a built-in pattern
	dashboard, _ := h.patternSvc.GetDashboard(ctx, project.ID)
	var builtin *models.PromptPattern
	for i := range dashboard.AllPatterns {
		if dashboard.AllPatterns[i].IsBuiltin {
			builtin = &dashboard.AllPatterns[i]
			break
		}
	}
	if builtin == nil {
		t.Fatal("no built-in pattern found")
	}

	// Try to update built-in
	form := url.Values{}
	form.Set("project_id", project.ID)
	form.Set("title", "Modified Builtin")
	form.Set("description", "Modified")
	form.Set("template_text", "Modified")
	form.Set("category", string(models.CategoryCustom))

	req := httptest.NewRequest(http.MethodPut, "/patterns/"+builtin.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestHandler_DeletePattern(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    project.ID,
		Title:        "Delete Handler Test",
		Description:  "Test",
		TemplateText: "Test",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	h.patternSvc.CreatePattern(ctx, pattern)

	// Delete pattern
	req := httptest.NewRequest(http.MethodDelete, "/patterns/"+pattern.ID+"?project_id="+project.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Verify deleted
	deleted, _ := h.patternSvc.GetPattern(ctx, pattern.ID)
	if deleted != nil {
		t.Error("expected pattern to be deleted")
	}
}

func TestHandler_DeletePattern_BuiltinProtection(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Get a built-in pattern
	dashboard, _ := h.patternSvc.GetDashboard(ctx, project.ID)
	var builtin *models.PromptPattern
	for i := range dashboard.AllPatterns {
		if dashboard.AllPatterns[i].IsBuiltin {
			builtin = &dashboard.AllPatterns[i]
			break
		}
	}
	if builtin == nil {
		t.Fatal("no built-in pattern found")
	}

	// Try to delete built-in
	req := httptest.NewRequest(http.MethodDelete, "/patterns/"+builtin.ID+"?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}

	// Verify still exists
	still, _ := h.patternSvc.GetPattern(ctx, builtin.ID)
	if still == nil {
		t.Error("built-in pattern should not be deleted")
	}
}

func TestHandler_ApplyPatternForm(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    project.ID,
		Title:        "Apply Form Test",
		Description:  "Test",
		TemplateText: "Fix {{issue}} in {{file}}",
		Category:     models.CategoryDebugging,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	h.patternSvc.CreatePattern(ctx, pattern)

	// Get apply form
	req := httptest.NewRequest(http.MethodGet, "/patterns/"+pattern.ID+"/apply", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Should contain form fields for variables
	body := rec.Body.String()
	if !strings.Contains(body, "issue") || !strings.Contains(body, "file") {
		t.Error("expected variable input fields in form")
	}
}

func TestHandler_ApplyPattern(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    project.ID,
		Title:        "Apply Handler Test",
		Description:  "Test",
		TemplateText: "Fix {{issue}} in {{file}}",
		Category:     models.CategoryDebugging,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	h.patternSvc.CreatePattern(ctx, pattern)

	// Apply pattern
	form := url.Values{}
	form.Set("task_title", "Fix Bug Task")
	form.Set("var_issue", "null pointer")
	form.Set("var_file", "handler.go")
	form.Set("category", string(models.CategoryBacklog))

	req := httptest.NewRequest(http.MethodPost, "/patterns/"+pattern.ID+"/apply", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify task was created
	tasks, _ := h.taskSvc.ListByProject(ctx, project.ID, "")
	found := false
	for _, task := range tasks {
		if task.Title == "Fix Bug Task" {
			found = true
			expectedPrompt := "Fix null pointer in handler.go"
			if task.Prompt != expectedPrompt {
				t.Errorf("expected prompt=%q, got %q", expectedPrompt, task.Prompt)
			}
		}
	}
	if !found {
		t.Error("expected task to be created")
	}

	// Verify usage was incremented
	updated, _ := h.patternSvc.GetPattern(ctx, pattern.ID)
	if updated.UsageCount != 1 {
		t.Errorf("expected usage_count=1, got %d", updated.UsageCount)
	}
}

func TestHandler_ApplyPattern_MissingVariable(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    project.ID,
		Title:        "Missing Var Test",
		Description:  "Test",
		TemplateText: "Fix {{issue}} in {{file}}",
		Category:     models.CategoryDebugging,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	h.patternSvc.CreatePattern(ctx, pattern)

	// Apply pattern with missing variable
	form := url.Values{}
	form.Set("task_title", "Test Task")
	form.Set("var_issue", "null pointer")
	// Missing var_file
	form.Set("category", string(models.CategoryBacklog))

	req := httptest.NewRequest(http.MethodPost, "/patterns/"+pattern.ID+"/apply", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Should succeed (service will return error in result component)
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestHandler_DuplicatePattern(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    project.ID,
		Title:        "Duplicate Handler Test",
		Description:  "Test",
		TemplateText: "Test {{var}}",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	h.patternSvc.CreatePattern(ctx, pattern)

	// Duplicate pattern
	req := httptest.NewRequest(http.MethodPost, "/patterns/"+pattern.ID+"/duplicate?project_id="+project.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify duplicate was created
	patterns, _ := h.patternSvc.Search(ctx, project.ID, "Duplicate Handler Test (Copy)")
	if len(patterns) != 1 {
		t.Fatalf("expected 1 duplicate pattern, got %d", len(patterns))
	}
}

func TestHandler_SearchPatterns(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    project.ID,
		Title:        "Searchable Handler Pattern",
		Description:  "Test",
		TemplateText: "Test",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         `["searchable"]`,
	}
	h.patternSvc.CreatePattern(ctx, pattern)

	// Search patterns
	req := httptest.NewRequest(http.MethodGet, "/patterns/search?project_id="+project.ID+"&q=Searchable", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Should contain searched pattern
	body := rec.Body.String()
	if !strings.Contains(body, "Searchable Handler Pattern") {
		t.Error("expected search result in response")
	}
}

func TestHandler_SearchPatterns_ByCategory(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    project.ID,
		Title:        "Category Search Test",
		Description:  "Test",
		TemplateText: "Test",
		Category:     models.CategoryRefactoring,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	h.patternSvc.CreatePattern(ctx, pattern)

	// Search by category
	req := httptest.NewRequest(http.MethodGet, "/patterns/search?project_id="+project.ID+"&category="+string(models.CategoryRefactoring), nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Category Search Test") {
		t.Error("expected pattern in category search results")
	}
}

func TestHandler_ExportPatterns(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Create custom pattern
	pattern := &models.PromptPattern{
		ProjectID:    project.ID,
		Title:        "Export Handler Test",
		Description:  "Export test",
		TemplateText: "Test {{var}}",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         `["export"]`,
	}
	h.patternSvc.CreatePattern(ctx, pattern)

	// Export patterns
	req := httptest.NewRequest(http.MethodGet, "/patterns/export?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Verify JSON response
	var exported []models.PromptPattern
	if err := json.Unmarshal(rec.Body.Bytes(), &exported); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	// Should contain custom pattern
	found := false
	for _, p := range exported {
		if p.Title == "Export Handler Test" {
			found = true
			if p.IsBuiltin {
				t.Error("exported pattern should not be built-in")
			}
		}
	}
	if !found {
		t.Error("expected custom pattern in export")
	}

	// Verify Content-Disposition header
	contentDisp := rec.Header().Get("Content-Disposition")
	if !strings.Contains(contentDisp, "patterns.json") {
		t.Errorf("expected Content-Disposition with patterns.json, got %q", contentDisp)
	}
}

func TestHandler_ImportPatterns(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Create JSON file to import
	patternsJSON := []models.PromptPattern{
		{
			Title:        "Import Handler Test 1",
			Description:  "First import",
			TemplateText: "Test {{var1}}",
			Category:     models.CategoryCustom,
			Tags:         `["import"]`,
		},
		{
			Title:        "Import Handler Test 2",
			Description:  "Second import",
			TemplateText: "Test {{var2}}",
			Category:     models.CategoryDebugging,
			Tags:         "[]",
		},
	}
	jsonData, _ := json.Marshal(patternsJSON)

	// Create multipart form
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	writer.WriteField("project_id", project.ID)

	part, _ := writer.CreateFormFile("file", "patterns.json")
	part.Write(jsonData)
	writer.Close()

	// Import patterns
	req := httptest.NewRequest(http.MethodPost, "/patterns/import", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify patterns were imported
	patterns, _ := h.patternSvc.Search(ctx, project.ID, "Import Handler Test")
	if len(patterns) < 2 {
		t.Errorf("expected at least 2 imported patterns, got %d", len(patterns))
	}
}

func TestHandler_ImportPatterns_NoFile(t *testing.T) {
	_, e, project := setupPatternTestHandler(t)

	// Create form without file
	form := url.Values{}
	form.Set("project_id", project.ID)

	req := httptest.NewRequest(http.MethodPost, "/patterns/import", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestHandler_ListPatternsByCategory(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Create pattern in specific category
	pattern := &models.PromptPattern{
		ProjectID:    project.ID,
		Title:        "Category List Test",
		Description:  "Test",
		TemplateText: "Test",
		Category:     models.CategoryOptimization,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	h.patternSvc.CreatePattern(ctx, pattern)

	// List patterns by category
	req := httptest.NewRequest(http.MethodGet, "/patterns/category/"+string(models.CategoryOptimization)+"?project_id="+project.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Category List Test") {
		t.Error("expected pattern in category list")
	}
}

func TestHandler_CreatePattern_EmptyTags(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Create pattern without tags
	form := url.Values{}
	form.Set("project_id", project.ID)
	form.Set("title", "No Tags Pattern")
	form.Set("description", "Test")
	form.Set("template_text", "Test")
	form.Set("category", string(models.CategoryCustom))
	// No tags field

	req := httptest.NewRequest(http.MethodPost, "/patterns", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Verify pattern was created with empty tags
	patterns, _ := h.patternSvc.Search(ctx, project.ID, "No Tags Pattern")
	if len(patterns) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(patterns))
	}
	if patterns[0].Tags != "[]" {
		t.Errorf("expected empty tags array, got %q", patterns[0].Tags)
	}
}

func TestHandler_UpdatePattern_WithTags(t *testing.T) {
	h, e, project := setupPatternTestHandler(t)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    project.ID,
		Title:        "Tags Update Test",
		Description:  "Test",
		TemplateText: "Test",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	h.patternSvc.CreatePattern(ctx, pattern)

	// Update with tags
	form := url.Values{}
	form.Set("project_id", project.ID)
	form.Set("title", "Tags Update Test")
	form.Set("description", "Test")
	form.Set("template_text", "Test")
	form.Set("category", string(models.CategoryCustom))
	form.Set("tags", "tag1, tag2, tag3")

	req := httptest.NewRequest(http.MethodPut, "/patterns/"+pattern.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Verify tags were updated
	updated, _ := h.patternSvc.GetPattern(ctx, pattern.ID)
	tags, _ := updated.ParseTags()
	if len(tags) != 3 {
		t.Errorf("expected 3 tags, got %d", len(tags))
	}
}

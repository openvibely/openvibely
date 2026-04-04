package handler

import (
	"context"
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

func setupTemplateTestHandler(t *testing.T) (*Handler, *echo.Echo, *repository.TemplateRepo, *repository.TaskRepo, *repository.ProjectRepo, *repository.LLMConfigRepo) {
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
	templateRepo := repository.NewTemplateRepo(db)

	alertRepo := repository.NewAlertRepo(db)
	upcomingRepo := repository.NewUpcomingRepo(db)

	projectSvc := service.NewProjectService(projectRepo)
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := service.NewWorkerService(llmSvc, 0, nil)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	alertSvc := service.NewAlertService(alertRepo, nil)
	upcomingSvc := service.NewUpcomingService(upcomingRepo)
	templateSvc := service.NewTemplateService(templateRepo, taskRepo, projectRepo)

	h := New(
		projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, alertSvc, upcomingSvc,
		nil, nil, nil, nil, nil, nil, nil, templateSvc, nil,
		llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, nil, nil, nil, nil,
	)

	e := echo.New()
	h.RegisterRoutes(e)

	return h, e, templateRepo, taskRepo, projectRepo, llmConfigRepo
}

func TestHandler_Templates(t *testing.T) {
	h, e, templateRepo, _, projectRepo, _ := setupTemplateTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project"}
	projectRepo.Create(ctx, project)

	// Create templates
	templateRepo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &project.ID,
		Name:           "Custom Template",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		CreatedBy:      "user",
	})

	// Request templates page
	req := httptest.NewRequest(http.MethodGet, "/templates?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Templates(c); err != nil {
		t.Fatalf("Templates: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Custom Template") {
		t.Error("expected template name in response")
	}
}

func TestHandler_Templates_HTMXRequest(t *testing.T) {
	h, e, _, _, projectRepo, _ := setupTemplateTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project"}
	projectRepo.Create(ctx, project)

	// Make HTMX request
	req := httptest.NewRequest(http.MethodGet, "/templates?project_id="+project.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Templates(c); err != nil {
		t.Fatalf("Templates: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestHandler_Templates_NoProject(t *testing.T) {
	h, e, _, _, _, _ := setupTemplateTestHandler(t)

	// Request without project_id
	req := httptest.NewRequest(http.MethodGet, "/templates", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Templates(c); err != nil {
		t.Fatalf("Templates: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestHandler_CreateTemplate(t *testing.T) {
	h, e, templateRepo, _, projectRepo, _ := setupTemplateTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project"}
	projectRepo.Create(ctx, project)

	// Create template via handler
	form := url.Values{}
	form.Set("name", "New Template")
	form.Set("description", "A new template")
	form.Set("default_prompt", "Do something")
	form.Set("category", string(models.CategoryActive))
	form.Set("category_filter", string(models.TaskTemplateCategoryFeature))
	form.Set("priority", "3")
	form.Set("tag", string(models.TagFeature))

	req := httptest.NewRequest(http.MethodPost, "/templates?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateTemplate(c); err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Verify template was created
	templates, _ := templateRepo.ListByProject(ctx, project.ID)
	found := false
	for _, tmpl := range templates {
		if tmpl.Name == "New Template" {
			found = true
			if tmpl.Description != "A new template" {
				t.Errorf("expected description 'A new template', got %q", tmpl.Description)
			}
			if tmpl.Priority != 3 {
				t.Errorf("expected priority 3, got %d", tmpl.Priority)
			}
			break
		}
	}
	if !found {
		t.Error("template should be created")
	}
}

func TestHandler_CreateTemplate_WithAgentID(t *testing.T) {
	h, e, templateRepo, _, projectRepo, llmConfigRepo := setupTemplateTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project"}
	projectRepo.Create(ctx, project)

	// Create an agent in agent_configs to satisfy FK constraint
	agent := &models.LLMConfig{Name: "Test Agent", Provider: "anthropic", Model: "claude-sonnet-4-5-20250929"}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("Create agent: %v", err)
	}
	agentID := agent.ID

	// Create template with agent_id
	form := url.Values{}
	form.Set("name", "Template with Agent")
	form.Set("description", "desc")
	form.Set("default_prompt", "prompt")
	form.Set("category", string(models.CategoryActive))
	form.Set("category_filter", string(models.TaskTemplateCategoryFeature))
	form.Set("priority", "2")
	form.Set("tag", string(models.TagFeature))
	form.Set("agent_id", agentID)

	req := httptest.NewRequest(http.MethodPost, "/templates?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateTemplate(c); err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}

	// Verify agent_id was set
	templates, _ := templateRepo.ListByProject(ctx, project.ID)
	found := false
	for _, tmpl := range templates {
		if tmpl.Name == "Template with Agent" {
			found = true
			if tmpl.SuggestedAgentID == nil || *tmpl.SuggestedAgentID != agentID {
				t.Errorf("expected agent_id %q, got %v", agentID, tmpl.SuggestedAgentID)
			}
			break
		}
	}
	if !found {
		t.Error("template should be created")
	}
}

func TestHandler_CreateTemplate_MissingProjectID(t *testing.T) {
	h, e, _, _, _, _ := setupTemplateTestHandler(t)

	form := url.Values{}
	form.Set("name", "Template")

	req := httptest.NewRequest(http.MethodPost, "/templates", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.CreateTemplate(c)
	if err == nil {
		t.Error("expected error for missing project_id")
	}

	httpErr, ok := err.(*echo.HTTPError)
	if !ok {
		t.Error("expected echo.HTTPError")
	}
	if httpErr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", httpErr.Code)
	}
}

func TestHandler_CreateTaskFromTemplate(t *testing.T) {
	h, e, templateRepo, taskRepo, projectRepo, _ := setupTemplateTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project"}
	projectRepo.Create(ctx, project)

	// Create a template
	tmpl := &models.TaskTemplate{
		ProjectID:      &project.ID,
		Name:           "Bug Fix Template",
		Description:    "desc",
		DefaultPrompt:  "Fix the bug",
		Category:       models.CategoryActive,
		Priority:       4,
		Tag:            models.TagBug,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryBugFix),
		CreatedBy:      "user",
	}
	templateRepo.Create(ctx, tmpl)

	// Create task from template
	form := url.Values{}
	// No prompt override

	req := httptest.NewRequest(http.MethodPost, "/templates/"+tmpl.ID+"/create?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/templates/:id/create")
	c.SetParamNames("id")
	c.SetParamValues(tmpl.ID)

	if err := h.CreateTaskFromTemplate(c); err != nil {
		t.Fatalf("CreateTaskFromTemplate: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Verify HX-Redirect header
	redirect := rec.Header().Get("HX-Redirect")
	if !strings.Contains(redirect, "/tasks") {
		t.Error("expected HX-Redirect to /tasks")
	}

	// Verify task was created
	tasks, _ := taskRepo.ListByProject(ctx, project.ID, "")
	if len(tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Title != "Bug Fix Template" {
		t.Errorf("expected task title 'Bug Fix Template', got %q", tasks[0].Title)
	}
	if tasks[0].Prompt != "Fix the bug" {
		t.Errorf("expected prompt 'Fix the bug', got %q", tasks[0].Prompt)
	}
}

func TestHandler_CreateTaskFromTemplate_WithPromptOverride(t *testing.T) {
	h, e, templateRepo, taskRepo, projectRepo, _ := setupTemplateTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project"}
	projectRepo.Create(ctx, project)

	// Create a template
	tmpl := &models.TaskTemplate{
		ProjectID:      &project.ID,
		Name:           "Template",
		Description:    "desc",
		DefaultPrompt:  "Default prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		CreatedBy:      "user",
	}
	templateRepo.Create(ctx, tmpl)

	// Create task with custom prompt
	form := url.Values{}
	form.Set("prompt", "Custom prompt override")

	req := httptest.NewRequest(http.MethodPost, "/templates/"+tmpl.ID+"/create?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/templates/:id/create")
	c.SetParamNames("id")
	c.SetParamValues(tmpl.ID)

	if err := h.CreateTaskFromTemplate(c); err != nil {
		t.Fatalf("CreateTaskFromTemplate: %v", err)
	}

	// Verify task has custom prompt
	tasks, _ := taskRepo.ListByProject(ctx, project.ID, "")
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Prompt != "Custom prompt override" {
		t.Errorf("expected custom prompt, got %q", tasks[0].Prompt)
	}
}

func TestHandler_CreateTaskFromTemplate_MissingProjectID(t *testing.T) {
	h, e, _, _, _, _ := setupTemplateTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/templates/test-id/create", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/templates/:id/create")
	c.SetParamNames("id")
	c.SetParamValues("test-id")

	err := h.CreateTaskFromTemplate(c)
	if err == nil {
		t.Error("expected error for missing project_id")
	}

	httpErr, ok := err.(*echo.HTTPError)
	if !ok {
		t.Error("expected echo.HTTPError")
	}
	if httpErr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", httpErr.Code)
	}
}

func TestHandler_SaveTaskAsTemplate(t *testing.T) {
	h, e, templateRepo, taskRepo, projectRepo, _ := setupTemplateTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project"}
	projectRepo.Create(ctx, project)

	// Create a task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Task Title",
		Category:  models.CategoryActive,
		Priority:  3,
		Prompt:    "Do something important",
		Status:    models.StatusPending,
		Tag:       models.TagFeature,
	}
	taskRepo.Create(ctx, task)

	// Save as template
	form := url.Values{}
	form.Set("name", "My Template")
	form.Set("description", "Saved from task")
	form.Set("category_filter", string(models.TaskTemplateCategoryFeature))

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/save-as-template?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:id/save-as-template")
	c.SetParamNames("id")
	c.SetParamValues(task.ID)

	if err := h.SaveTaskAsTemplate(c); err != nil {
		t.Fatalf("SaveTaskAsTemplate: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Verify template was created
	templates, _ := templateRepo.ListByProject(ctx, project.ID)
	found := false
	for _, tmpl := range templates {
		if tmpl.Name == "My Template" {
			found = true
			if tmpl.DefaultPrompt != "Do something important" {
				t.Errorf("expected prompt from task, got %q", tmpl.DefaultPrompt)
			}
			break
		}
	}
	if !found {
		t.Error("template should be created from task")
	}
}

func TestHandler_SaveTaskAsTemplate_MissingName(t *testing.T) {
	h, e, _, _, projectRepo, _ := setupTemplateTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project"}
	projectRepo.Create(ctx, project)

	form := url.Values{}
	// No name provided

	req := httptest.NewRequest(http.MethodPost, "/tasks/task-id/save-as-template?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:id/save-as-template")
	c.SetParamNames("id")
	c.SetParamValues("task-id")

	err := h.SaveTaskAsTemplate(c)
	if err == nil {
		t.Error("expected error for missing name")
	}

	httpErr, ok := err.(*echo.HTTPError)
	if !ok {
		t.Error("expected echo.HTTPError")
	}
	if httpErr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", httpErr.Code)
	}
}

func TestHandler_UpdateTemplate(t *testing.T) {
	h, e, templateRepo, _, projectRepo, _ := setupTemplateTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project"}
	projectRepo.Create(ctx, project)

	// Create a template
	tmpl := &models.TaskTemplate{
		ProjectID:      &project.ID,
		Name:           "Original Name",
		Description:    "Original description",
		DefaultPrompt:  "Original prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		CreatedBy:      "user",
	}
	templateRepo.Create(ctx, tmpl)

	// Update template
	form := url.Values{}
	form.Set("name", "Updated Name")
	form.Set("description", "Updated description")
	form.Set("default_prompt", "Updated prompt")
	form.Set("category", string(models.CategoryActive))
	form.Set("category_filter", string(models.TaskTemplateCategoryBugFix))
	form.Set("priority", "4")
	form.Set("tag", string(models.TagBug))

	req := httptest.NewRequest(http.MethodPut, "/templates/"+tmpl.ID+"?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/templates/:id")
	c.SetParamNames("id")
	c.SetParamValues(tmpl.ID)

	if err := h.UpdateTemplate(c); err != nil {
		t.Fatalf("UpdateTemplate: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Verify update
	updated, _ := templateRepo.GetByID(ctx, tmpl.ID)
	if updated.Name != "Updated Name" {
		t.Errorf("expected Name='Updated Name', got %q", updated.Name)
	}
	if updated.Description != "Updated description" {
		t.Errorf("expected updated description, got %q", updated.Description)
	}
	if updated.Priority != 4 {
		t.Errorf("expected Priority=4, got %d", updated.Priority)
	}
}

func TestHandler_DeleteTemplate(t *testing.T) {
	h, e, templateRepo, _, projectRepo, _ := setupTemplateTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project"}
	projectRepo.Create(ctx, project)

	// Create a template
	tmpl := &models.TaskTemplate{
		ProjectID:      &project.ID,
		Name:           "Template to Delete",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		CreatedBy:      "user",
	}
	templateRepo.Create(ctx, tmpl)

	// Delete template
	req := httptest.NewRequest(http.MethodDelete, "/templates/"+tmpl.ID+"?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/templates/:id")
	c.SetParamNames("id")
	c.SetParamValues(tmpl.ID)

	if err := h.DeleteTemplate(c); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Verify deletion
	deleted, _ := templateRepo.GetByID(ctx, tmpl.ID)
	if deleted != nil {
		t.Error("template should be deleted")
	}
}

func TestHandler_ToggleFavoriteTemplate(t *testing.T) {
	h, e, templateRepo, _, projectRepo, _ := setupTemplateTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project"}
	projectRepo.Create(ctx, project)

	// Create a template
	tmpl := &models.TaskTemplate{
		ProjectID:      &project.ID,
		Name:           "Template",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		IsFavorite:     false,
		CreatedBy:      "user",
	}
	templateRepo.Create(ctx, tmpl)

	// Toggle to favorite
	form := url.Values{}
	form.Set("is_favorite", "true")

	req := httptest.NewRequest(http.MethodPost, "/templates/"+tmpl.ID+"/favorite?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/templates/:id/favorite")
	c.SetParamNames("id")
	c.SetParamValues(tmpl.ID)

	if err := h.ToggleFavoriteTemplate(c); err != nil {
		t.Fatalf("ToggleFavoriteTemplate: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Verify favorite status
	updated, _ := templateRepo.GetByID(ctx, tmpl.ID)
	if !updated.IsFavorite {
		t.Error("expected IsFavorite=true")
	}
}

func TestHandler_SearchTemplates(t *testing.T) {
	h, e, templateRepo, _, projectRepo, _ := setupTemplateTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project"}
	projectRepo.Create(ctx, project)

	// Create templates
	templateRepo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &project.ID,
		Name:           "Security Review",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryCodeReview),
		CreatedBy:      "user",
	})
	templateRepo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &project.ID,
		Name:           "Performance Test",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryTesting),
		CreatedBy:      "user",
	})

	// Search
	req := httptest.NewRequest(http.MethodGet, "/templates/search?project_id="+project.ID+"&q=security", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.SearchTemplates(c); err != nil {
		t.Fatalf("SearchTemplates: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Security Review") {
		t.Error("expected 'Security Review' in search results")
	}
	if strings.Contains(body, "Performance Test") {
		t.Error("should not contain 'Performance Test' in search results")
	}
}

func TestHandler_FilterTemplates(t *testing.T) {
	h, e, templateRepo, _, projectRepo, _ := setupTemplateTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project"}
	projectRepo.Create(ctx, project)

	// Create templates with different categories
	templateRepo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &project.ID,
		Name:           "Bug Fix",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryBugFix),
		CreatedBy:      "user",
	})
	templateRepo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &project.ID,
		Name:           "Feature",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		CreatedBy:      "user",
	})

	// Filter by bug_fix
	req := httptest.NewRequest(http.MethodGet, "/templates/filter?project_id="+project.ID+"&category=bug_fix", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.FilterTemplates(c); err != nil {
		t.Fatalf("FilterTemplates: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Bug Fix") {
		t.Error("expected 'Bug Fix' in filtered results")
	}
	if strings.Contains(body, "Feature") {
		t.Error("should not contain 'Feature' in bug_fix filter")
	}
}

func TestHandler_FilterTemplates_AllCategory(t *testing.T) {
	h, e, templateRepo, _, projectRepo, _ := setupTemplateTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project"}
	projectRepo.Create(ctx, project)

	// Create templates
	templateRepo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &project.ID,
		Name:           "Template 1",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryBugFix),
		CreatedBy:      "user",
	})
	templateRepo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &project.ID,
		Name:           "Template 2",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		CreatedBy:      "user",
	})

	// Filter by "all"
	req := httptest.NewRequest(http.MethodGet, "/templates/filter?project_id="+project.ID+"&category=all", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.FilterTemplates(c); err != nil {
		t.Fatalf("FilterTemplates: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Template 1") {
		t.Error("expected 'Template 1' when filtering by 'all'")
	}
	if !strings.Contains(body, "Template 2") {
		t.Error("expected 'Template 2' when filtering by 'all'")
	}
}

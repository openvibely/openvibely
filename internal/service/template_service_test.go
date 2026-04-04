package service

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func setupTemplateService(t *testing.T) (*TemplateService, *repository.TemplateRepo, *repository.TaskRepo, *repository.ProjectRepo, *repository.LLMConfigRepo) {
	t.Helper()
	db := testutil.NewTestDB(t)

	templateRepo := repository.NewTemplateRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	svc := NewTemplateService(templateRepo, taskRepo, projectRepo)
	return svc, templateRepo, taskRepo, projectRepo, llmConfigRepo
}

// createTestProject creates a project in the DB and returns its ID.
func createTestProject(t *testing.T, ctx context.Context, projectRepo *repository.ProjectRepo) string {
	t.Helper()
	p := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(ctx, p); err != nil {
		t.Fatalf("Create project: %v", err)
	}
	return p.ID
}

func TestTemplateService_GetDashboard(t *testing.T) {
	svc, templateRepo, _, projectRepo, _ := setupTemplateService(t)
	ctx := context.Background()
	projectID := createTestProject(t, ctx, projectRepo)

	// Migration 030 seeds 15 built-in templates; add one more for the test
	builtIn := &models.TaskTemplate{
		ProjectID:      nil, // Global
		Name:           "Built-in Template",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryCodeReview),
		IsBuiltIn:      true,
		CreatedBy:      "system",
	}
	templateRepo.Create(ctx, builtIn)

	custom1 := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Custom Template 1",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		IsBuiltIn:      false,
		IsFavorite:     true,
		CreatedBy:      "user",
	}
	if err := templateRepo.Create(ctx, custom1); err != nil {
		t.Fatalf("Create custom1: %v", err)
	}
	// Increment usage so it appears in "recently used" (Create doesn't set usage_count)
	templateRepo.IncrementUsage(ctx, custom1.ID)

	custom2 := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Custom Template 2",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryBugFix),
		IsBuiltIn:      false,
		CreatedBy:      "user",
	}
	if err := templateRepo.Create(ctx, custom2); err != nil {
		t.Fatalf("Create custom2: %v", err)
	}

	// Get dashboard
	data, err := svc.GetDashboard(ctx, projectID)
	if err != nil {
		t.Fatalf("GetDashboard: %v", err)
	}

	if data == nil {
		t.Fatal("expected dashboard data")
	}

	// Verify built-in templates (15 seeded + 1 test-created = 16)
	if len(data.BuiltInTemplates) != 16 {
		t.Errorf("expected 16 built-in templates (15 seeded + 1 test), got %d", len(data.BuiltInTemplates))
	}

	// Verify custom templates
	if len(data.CustomTemplates) != 2 {
		t.Errorf("expected 2 custom templates, got %d", len(data.CustomTemplates))
	}

	// Verify favorites
	if len(data.Favorites) != 1 {
		t.Errorf("expected 1 favorite, got %d", len(data.Favorites))
	}

	// Verify recently used
	if len(data.RecentlyUsed) != 1 {
		t.Errorf("expected 1 recently used, got %d", len(data.RecentlyUsed))
	}
}

func TestTemplateService_GetDashboard_EmptyState(t *testing.T) {
	svc, _, _, projectRepo, _ := setupTemplateService(t)
	ctx := context.Background()
	projectID := createTestProject(t, ctx, projectRepo)

	data, err := svc.GetDashboard(ctx, projectID)
	if err != nil {
		t.Fatalf("GetDashboard: %v", err)
	}

	// All slices should be non-nil (built-in has 15 seeded)
	if data.BuiltInTemplates == nil {
		t.Error("expected non-nil BuiltInTemplates")
	}
	if data.CustomTemplates == nil {
		t.Error("expected non-nil CustomTemplates")
	}
	if data.Favorites == nil {
		t.Error("expected non-nil Favorites")
	}
	if data.RecentlyUsed == nil {
		t.Error("expected non-nil RecentlyUsed")
	}
}

func TestTemplateService_CreateFromTemplate(t *testing.T) {
	svc, templateRepo, taskRepo, projectRepo, llmConfigRepo := setupTemplateService(t)
	ctx := context.Background()
	projectID := createTestProject(t, ctx, projectRepo)

	// Create an agent in agent_configs to satisfy FK constraint
	agent := &models.LLMConfig{Name: "Test Agent", Provider: "anthropic", Model: "claude-sonnet-4-5-20250929"}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("Create agent: %v", err)
	}
	agentID := agent.ID

	// Create a template
	tmpl := &models.TaskTemplate{
		ProjectID:        &projectID,
		Name:             "Bug Fix Template",
		Description:      "desc",
		DefaultPrompt:    "Find and fix the bug",
		SuggestedAgentID: &agentID,
		Category:         models.CategoryActive,
		Priority:         4,
		Tag:              models.TagBug,
		TagsJSON:         "[]",
		CategoryFilter:   string(models.TaskTemplateCategoryBugFix),
		CreatedBy:        "user",
	}
	if err := templateRepo.Create(ctx, tmpl); err != nil {
		t.Fatalf("Create template: %v", err)
	}

	// Create task from template
	task, err := svc.CreateFromTemplate(ctx, tmpl.ID, projectID, "")
	if err != nil {
		t.Fatalf("CreateFromTemplate: %v", err)
	}

	if task == nil {
		t.Fatal("expected task")
	}

	// Verify task properties
	if task.Title != "Bug Fix Template" {
		t.Errorf("expected Title='Bug Fix Template', got %q", task.Title)
	}
	if task.Prompt != "Find and fix the bug" {
		t.Errorf("expected template's default prompt, got %q", task.Prompt)
	}
	if task.Category != models.CategoryActive {
		t.Errorf("expected Category=active, got %q", task.Category)
	}
	if task.Priority != 4 {
		t.Errorf("expected Priority=4, got %d", task.Priority)
	}
	if task.Tag != models.TagBug {
		t.Errorf("expected Tag=bug, got %q", task.Tag)
	}
	if task.Status != models.StatusPending {
		t.Errorf("expected Status=pending, got %q", task.Status)
	}
	if task.AgentID == nil || *task.AgentID != agentID {
		t.Errorf("expected AgentID=%q, got %v", agentID, task.AgentID)
	}

	// Verify task was created in database
	savedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if savedTask == nil {
		t.Error("task should be saved in database")
	}

	// Verify usage count incremented
	updatedTmpl, _ := templateRepo.GetByID(ctx, tmpl.ID)
	if updatedTmpl.UsageCount != 1 {
		t.Errorf("expected UsageCount=1, got %d", updatedTmpl.UsageCount)
	}
}

func TestTemplateService_CreateFromTemplate_WithUserPromptOverride(t *testing.T) {
	svc, templateRepo, _, projectRepo, _ := setupTemplateService(t)
	ctx := context.Background()
	projectID := createTestProject(t, ctx, projectRepo)

	// Create a template
	tmpl := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Template",
		Description:    "desc",
		DefaultPrompt:  "Default prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		CreatedBy:      "user",
	}
	if err := templateRepo.Create(ctx, tmpl); err != nil {
		t.Fatalf("Create template: %v", err)
	}

	// Create task with user prompt override
	userPrompt := "Custom user prompt"
	task, err := svc.CreateFromTemplate(ctx, tmpl.ID, projectID, userPrompt)
	if err != nil {
		t.Fatalf("CreateFromTemplate: %v", err)
	}

	if task.Prompt != userPrompt {
		t.Errorf("expected user prompt %q, got %q", userPrompt, task.Prompt)
	}
}

func TestTemplateService_CreateFromTemplate_NotFound(t *testing.T) {
	svc, _, _, _, _ := setupTemplateService(t)
	ctx := context.Background()

	task, err := svc.CreateFromTemplate(ctx, "nonexistent", "project", "")
	if err == nil {
		t.Error("expected error for nonexistent template")
	}
	if task != nil {
		t.Error("expected nil task")
	}
}

func TestTemplateService_SaveAsTemplate(t *testing.T) {
	svc, templateRepo, taskRepo, projectRepo, llmConfigRepo := setupTemplateService(t)
	ctx := context.Background()
	projectID := createTestProject(t, ctx, projectRepo)

	// Create an agent in agent_configs to satisfy FK constraint
	agent := &models.LLMConfig{Name: "Test Agent", Provider: "anthropic", Model: "claude-sonnet-4-5-20250929"}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("Create agent: %v", err)
	}
	agentID := agent.ID

	// Create a task
	task := &models.Task{
		ProjectID: projectID,
		Title:     "Original Task",
		Category:  models.CategoryActive,
		Priority:  3,
		Prompt:    "Do something important",
		Status:    models.StatusPending,
		Tag:       models.TagFeature,
		AgentID:   &agentID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Save as template
	tmpl, err := svc.SaveAsTemplate(ctx, task.ID, projectID, "My Template", "A useful template", string(models.TaskTemplateCategoryFeature), "user")
	if err != nil {
		t.Fatalf("SaveAsTemplate: %v", err)
	}

	if tmpl == nil {
		t.Fatal("expected template")
	}

	// Verify template properties
	if tmpl.Name != "My Template" {
		t.Errorf("expected Name='My Template', got %q", tmpl.Name)
	}
	if tmpl.Description != "A useful template" {
		t.Errorf("expected Description='A useful template', got %q", tmpl.Description)
	}
	if tmpl.DefaultPrompt != "Do something important" {
		t.Errorf("expected DefaultPrompt from task, got %q", tmpl.DefaultPrompt)
	}
	if tmpl.Category != models.CategoryActive {
		t.Errorf("expected Category=active, got %q", tmpl.Category)
	}
	if tmpl.Priority != 3 {
		t.Errorf("expected Priority=3, got %d", tmpl.Priority)
	}
	if tmpl.Tag != models.TagFeature {
		t.Errorf("expected Tag=feature, got %q", tmpl.Tag)
	}
	if tmpl.IsBuiltIn {
		t.Error("expected IsBuiltIn=false")
	}
	if tmpl.SuggestedAgentID == nil || *tmpl.SuggestedAgentID != agentID {
		t.Errorf("expected SuggestedAgentID=%q, got %v", agentID, tmpl.SuggestedAgentID)
	}

	// Verify it's saved in database
	savedTmpl, _ := templateRepo.GetByID(ctx, tmpl.ID)
	if savedTmpl == nil {
		t.Error("template should be saved in database")
	}
}

func TestTemplateService_SaveAsTemplate_TaskNotFound(t *testing.T) {
	svc, _, _, _, _ := setupTemplateService(t)
	ctx := context.Background()

	tmpl, err := svc.SaveAsTemplate(ctx, "nonexistent", "project", "Name", "Desc", "feature", "user")
	if err == nil {
		t.Error("expected error for nonexistent task")
	}
	if tmpl != nil {
		t.Error("expected nil template")
	}
}

func TestTemplateService_CreateCustomTemplate(t *testing.T) {
	svc, templateRepo, _, projectRepo, _ := setupTemplateService(t)
	ctx := context.Background()
	projectID := createTestProject(t, ctx, projectRepo)

	tmpl := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Custom Template",
		Description:    "A custom template",
		DefaultPrompt:  "Do something",
		Category:       models.CategoryActive,
		Priority:       2,
		Tag:            models.TagFeature,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		IsBuiltIn:      true, // This should be overridden
		CreatedBy:      "user",
	}

	if err := svc.CreateCustomTemplate(ctx, tmpl); err != nil {
		t.Fatalf("CreateCustomTemplate: %v", err)
	}

	// Verify IsBuiltIn was forced to false
	saved, _ := templateRepo.GetByID(ctx, tmpl.ID)
	if saved.IsBuiltIn {
		t.Error("CreateCustomTemplate should force IsBuiltIn=false")
	}
}

func TestTemplateService_UpdateTemplate(t *testing.T) {
	svc, templateRepo, _, projectRepo, _ := setupTemplateService(t)
	ctx := context.Background()
	projectID := createTestProject(t, ctx, projectRepo)

	// Create a custom template
	tmpl := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Original Name",
		Description:    "Original description",
		DefaultPrompt:  "Original prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		IsBuiltIn:      false,
		CreatedBy:      "user",
	}
	if err := templateRepo.Create(ctx, tmpl); err != nil {
		t.Fatalf("Create template: %v", err)
	}

	// Update template
	tmpl.Name = "Updated Name"
	tmpl.Description = "Updated description"
	tmpl.Priority = 4

	if err := svc.UpdateTemplate(ctx, tmpl); err != nil {
		t.Fatalf("UpdateTemplate: %v", err)
	}

	// Verify update
	updated, _ := templateRepo.GetByID(ctx, tmpl.ID)
	if updated.Name != "Updated Name" {
		t.Errorf("expected Name='Updated Name', got %q", updated.Name)
	}
	if updated.Priority != 4 {
		t.Errorf("expected Priority=4, got %d", updated.Priority)
	}
}

func TestTemplateService_UpdateTemplate_BuiltInRejected(t *testing.T) {
	svc, templateRepo, _, projectRepo, _ := setupTemplateService(t)
	ctx := context.Background()
	projectID := createTestProject(t, ctx, projectRepo)

	// Create a built-in template
	tmpl := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Built-in Template",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		IsBuiltIn:      true,
		CreatedBy:      "system",
	}
	if err := templateRepo.Create(ctx, tmpl); err != nil {
		t.Fatalf("Create template: %v", err)
	}

	// Try to update
	tmpl.Name = "Modified Name"
	err := svc.UpdateTemplate(ctx, tmpl)
	if err == nil {
		t.Error("expected error when updating built-in template")
	}
	if err.Error() != "cannot modify built-in template" {
		t.Errorf("expected specific error message, got %q", err.Error())
	}
}

func TestTemplateService_UpdateTemplate_NotFound(t *testing.T) {
	svc, _, _, _, _ := setupTemplateService(t)
	ctx := context.Background()

	tmpl := &models.TaskTemplate{
		ID:          "nonexistent",
		Name:        "Name",
		Description: "desc",
	}

	err := svc.UpdateTemplate(ctx, tmpl)
	if err == nil {
		t.Error("expected error for nonexistent template")
	}
}

func TestTemplateService_DeleteTemplate(t *testing.T) {
	svc, templateRepo, _, projectRepo, _ := setupTemplateService(t)
	ctx := context.Background()
	projectID := createTestProject(t, ctx, projectRepo)

	// Create a custom template
	tmpl := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Template to Delete",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		IsBuiltIn:      false,
		CreatedBy:      "user",
	}
	if err := templateRepo.Create(ctx, tmpl); err != nil {
		t.Fatalf("Create template: %v", err)
	}

	// Delete template
	if err := svc.DeleteTemplate(ctx, tmpl.ID); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}

	// Verify deletion
	deleted, _ := templateRepo.GetByID(ctx, tmpl.ID)
	if deleted != nil {
		t.Error("template should be deleted")
	}
}

func TestTemplateService_DeleteTemplate_BuiltInRejected(t *testing.T) {
	svc, templateRepo, _, projectRepo, _ := setupTemplateService(t)
	ctx := context.Background()
	projectID := createTestProject(t, ctx, projectRepo)

	// Create a built-in template
	tmpl := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Built-in Template",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		IsBuiltIn:      true,
		CreatedBy:      "system",
	}
	if err := templateRepo.Create(ctx, tmpl); err != nil {
		t.Fatalf("Create template: %v", err)
	}

	// Try to delete
	err := svc.DeleteTemplate(ctx, tmpl.ID)
	if err == nil {
		t.Error("expected error when deleting built-in template")
	}
	if err.Error() != "cannot delete built-in template" {
		t.Errorf("expected specific error message, got %q", err.Error())
	}

	// Verify it still exists
	still, _ := templateRepo.GetByID(ctx, tmpl.ID)
	if still == nil {
		t.Error("built-in template should not be deleted")
	}
}

func TestTemplateService_DeleteTemplate_NotFound(t *testing.T) {
	svc, _, _, _, _ := setupTemplateService(t)
	ctx := context.Background()

	err := svc.DeleteTemplate(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent template")
	}
}

func TestTemplateService_ToggleFavorite(t *testing.T) {
	svc, templateRepo, _, projectRepo, _ := setupTemplateService(t)
	ctx := context.Background()
	projectID := createTestProject(t, ctx, projectRepo)

	// Create a template
	tmpl := &models.TaskTemplate{
		ProjectID:      &projectID,
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
	if err := templateRepo.Create(ctx, tmpl); err != nil {
		t.Fatalf("Create template: %v", err)
	}

	// Toggle to favorite
	if err := svc.ToggleFavorite(ctx, tmpl.ID, true); err != nil {
		t.Fatalf("ToggleFavorite: %v", err)
	}

	updated, _ := templateRepo.GetByID(ctx, tmpl.ID)
	if !updated.IsFavorite {
		t.Error("expected IsFavorite=true")
	}
}

func TestTemplateService_SearchTemplates(t *testing.T) {
	svc, templateRepo, _, projectRepo, _ := setupTemplateService(t)
	ctx := context.Background()
	projectID := createTestProject(t, ctx, projectRepo)

	// Create templates
	templateRepo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Security Review Custom",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryCodeReview),
		CreatedBy:      "user",
	})
	templateRepo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Performance Test Custom",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryTesting),
		CreatedBy:      "user",
	})

	// Search - use "Security Review Custom" to avoid matching seeded "Security Audit"
	results, err := svc.SearchTemplates(ctx, projectID, "Security Review Custom")
	if err != nil {
		t.Fatalf("SearchTemplates: %v", err)
	}

	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
	if len(results) > 0 && results[0].Name != "Security Review Custom" {
		t.Errorf("expected 'Security Review Custom', got %q", results[0].Name)
	}
}

func TestTemplateService_FilterByCategory(t *testing.T) {
	svc, templateRepo, _, projectRepo, _ := setupTemplateService(t)
	ctx := context.Background()
	projectID := createTestProject(t, ctx, projectRepo)

	// Create templates with different categories
	templateRepo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &projectID,
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
		ProjectID:      &projectID,
		Name:           "Feature",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		CreatedBy:      "user",
	})

	// Filter by bug_fix (seeded templates include "Bug Investigation" as bug_fix)
	results, err := svc.FilterByCategory(ctx, projectID, models.TaskTemplateCategoryBugFix)
	if err != nil {
		t.Fatalf("FilterByCategory: %v", err)
	}

	// Should include our test template + any seeded bug_fix templates
	foundOurs := false
	for _, r := range results {
		if r.Name == "Bug Fix" {
			foundOurs = true
		}
		if r.CategoryFilter != string(models.TaskTemplateCategoryBugFix) {
			t.Errorf("expected bug_fix category filter, got %q for template %q", r.CategoryFilter, r.Name)
		}
	}
	if !foundOurs {
		t.Error("expected our 'Bug Fix' template in results")
	}
}

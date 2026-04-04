package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func setupTemplateRepoTest(t *testing.T) (*TemplateRepo, *ProjectRepo, context.Context, string) {
	t.Helper()
	db := testutil.NewTestDB(t)
	repo := NewTemplateRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// Create a project for foreign key
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	return repo, projectRepo, ctx, project.ID
}

func TestTemplateRepo_CreateAndGetByID(t *testing.T) {
	repo, _, ctx, projectID := setupTemplateRepoTest(t)

	tmpl := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Test Template",
		Description:    "A test template",
		DefaultPrompt:  "Do something",
		Category:       models.CategoryActive,
		Priority:       3,
		Tag:            models.TagFeature,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		IsBuiltIn:      false,
		IsFavorite:     true,
		CreatedBy:      "test-user",
	}

	if err := repo.Create(ctx, tmpl); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tmpl.ID == "" {
		t.Fatal("expected ID to be set")
	}
	if tmpl.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be set")
	}

	got, err := repo.GetByID(ctx, tmpl.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected template, got nil")
	}
	if got.Name != "Test Template" {
		t.Errorf("expected Name='Test Template', got %q", got.Name)
	}
	if got.Category != models.CategoryActive {
		t.Errorf("expected Category=active, got %q", got.Category)
	}
	if got.IsFavorite != true {
		t.Error("expected IsFavorite=true")
	}
}

func TestTemplateRepo_GetByID_NotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTemplateRepo(db)
	ctx := context.Background()

	got, err := repo.GetByID(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent template")
	}
}

func TestTemplateRepo_Update(t *testing.T) {
	repo, _, ctx, projectID := setupTemplateRepoTest(t)

	tmpl := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Original Name",
		Description:    "Original description",
		DefaultPrompt:  "Original prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		Tag:            models.TagFeature,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		IsBuiltIn:      false,
		CreatedBy:      "test-user",
	}

	if err := repo.Create(ctx, tmpl); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update the template
	tmpl.Name = "Updated Name"
	tmpl.Description = "Updated description"
	tmpl.DefaultPrompt = "Updated prompt"
	tmpl.Priority = 4
	tmpl.IsFavorite = true

	if err := repo.Update(ctx, tmpl); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Verify update
	got, err := repo.GetByID(ctx, tmpl.ID)
	if err != nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if got.Name != "Updated Name" {
		t.Errorf("expected Name='Updated Name', got %q", got.Name)
	}
	if got.Description != "Updated description" {
		t.Errorf("expected updated description, got %q", got.Description)
	}
	if got.Priority != 4 {
		t.Errorf("expected Priority=4, got %d", got.Priority)
	}
	if !got.IsFavorite {
		t.Error("expected IsFavorite=true")
	}
}

func TestTemplateRepo_Update_BuiltInProtected(t *testing.T) {
	repo, _, ctx, projectID := setupTemplateRepoTest(t)

	// Create a built-in template
	tmpl := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Built-in Template",
		Description:    "A built-in template",
		DefaultPrompt:  "Do something",
		Category:       models.CategoryActive,
		Priority:       2,
		Tag:            models.TagFeature,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		IsBuiltIn:      true,
		CreatedBy:      "system",
	}

	if err := repo.Create(ctx, tmpl); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Try to update (should silently fail due to WHERE is_built_in = 0)
	tmpl.Name = "Modified Name"
	if err := repo.Update(ctx, tmpl); err != nil {
		t.Fatalf("Update should not error: %v", err)
	}

	// Verify it wasn't updated
	got, err := repo.GetByID(ctx, tmpl.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "Built-in Template" {
		t.Errorf("built-in template should not be updated, got Name=%q", got.Name)
	}
}

func TestTemplateRepo_Delete(t *testing.T) {
	repo, _, ctx, projectID := setupTemplateRepoTest(t)

	tmpl := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Template to Delete",
		Description:    "Will be deleted",
		DefaultPrompt:  "Do something",
		Category:       models.CategoryActive,
		Priority:       2,
		Tag:            models.TagFeature,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		IsBuiltIn:      false,
		CreatedBy:      "test-user",
	}

	if err := repo.Create(ctx, tmpl); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Delete(ctx, tmpl.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Verify deletion
	got, err := repo.GetByID(ctx, tmpl.ID)
	if err != nil {
		t.Fatalf("GetByID after delete: %v", err)
	}
	if got != nil {
		t.Error("expected template to be deleted")
	}
}

func TestTemplateRepo_Delete_BuiltInProtected(t *testing.T) {
	repo, _, ctx, projectID := setupTemplateRepoTest(t)

	// Create a built-in template
	tmpl := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Built-in Template",
		Description:    "Cannot be deleted",
		DefaultPrompt:  "Do something",
		Category:       models.CategoryActive,
		Priority:       2,
		Tag:            models.TagFeature,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		IsBuiltIn:      true,
		CreatedBy:      "system",
	}

	if err := repo.Create(ctx, tmpl); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Try to delete (should silently fail)
	if err := repo.Delete(ctx, tmpl.ID); err != nil {
		t.Fatalf("Delete should not error: %v", err)
	}

	// Verify it still exists
	got, err := repo.GetByID(ctx, tmpl.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Error("built-in template should not be deleted")
	}
}

func TestTemplateRepo_ListByProject(t *testing.T) {
	repo, _, ctx, projectID := setupTemplateRepoTest(t)

	// Create templates for the project
	repo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Project Template 1",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		CreatedBy:      "user",
	})
	repo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Project Template 2",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryBugFix),
		CreatedBy:      "user",
		IsFavorite:     true,
	})

	// List templates for test-project (includes 15 seeded global templates + 2 project-specific)
	templates, err := repo.ListByProject(ctx, projectID)
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}

	// Should have at least our 2 templates (plus 15 seeded global ones)
	if len(templates) < 2 {
		t.Errorf("expected at least 2 templates, got %d", len(templates))
	}

	// Verify our templates exist
	foundCount := 0
	for _, tmpl := range templates {
		if tmpl.Name == "Project Template 1" || tmpl.Name == "Project Template 2" {
			foundCount++
		}
	}
	if foundCount != 2 {
		t.Errorf("expected to find 2 project templates, found %d", foundCount)
	}

	// Verify ordering: favorites first
	if !templates[0].IsFavorite && templates[1].IsFavorite {
		t.Error("favorites should be ordered first")
	}
}

func TestTemplateRepo_ListByCategory(t *testing.T) {
	repo, _, ctx, projectID := setupTemplateRepoTest(t)

	// Create templates with specific category filter
	repo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "My Bug Fix Template",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryBugFix),
		CreatedBy:      "user",
	})

	// List by specific category (includes seeded templates with same category)
	bugFixTemplates, err := repo.ListByCategory(ctx, projectID, models.TaskTemplateCategoryBugFix)
	if err != nil {
		t.Fatalf("ListByCategory: %v", err)
	}

	// Should have at least our template
	found := false
	for _, tmpl := range bugFixTemplates {
		if tmpl.Name == "My Bug Fix Template" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find our bug fix template")
	}

	// All should have bug_fix category filter
	for _, tmpl := range bugFixTemplates {
		if tmpl.CategoryFilter != string(models.TaskTemplateCategoryBugFix) {
			t.Errorf("expected bug_fix category filter, got %q", tmpl.CategoryFilter)
		}
	}
}

func TestTemplateRepo_Search(t *testing.T) {
	repo, _, ctx, projectID := setupTemplateRepoTest(t)

	// Create template with unique searchable name
	uniqueName := "VeryUniqueTemplateName12345"
	repo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           uniqueName,
		Description:    "Review code for security vulnerabilities",
		DefaultPrompt:  "Check for SQL injection",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryCodeReview),
		CreatedBy:      "user",
	})

	// Search by unique name
	results, err := repo.Search(ctx, projectID, "VeryUniqueTemplateName")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 template with unique name, got %d", len(results))
	}
	if len(results) > 0 && results[0].Name != uniqueName {
		t.Errorf("expected %q, got %q", uniqueName, results[0].Name)
	}

	// Search with no results
	results, err = repo.Search(ctx, projectID, "NonexistentXYZ123")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 templates, got %d", len(results))
	}
}

func TestTemplateRepo_ListFavorites(t *testing.T) {
	repo, _, ctx, projectID := setupTemplateRepoTest(t)

	// Create templates with favorite status
	repo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "My Favorite 1",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		IsFavorite:     true,
		CreatedBy:      "user",
	})
	repo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Not Favorite",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		IsFavorite:     false,
		CreatedBy:      "user",
	})
	repo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "My Favorite 2",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		IsFavorite:     true,
		CreatedBy:      "user",
	})

	favorites, err := repo.ListFavorites(ctx, projectID)
	if err != nil {
		t.Fatalf("ListFavorites: %v", err)
	}

	// Should have at least our 2 favorites
	foundCount := 0
	for _, fav := range favorites {
		if fav.Name == "My Favorite 1" || fav.Name == "My Favorite 2" {
			foundCount++
		}
	}
	if foundCount != 2 {
		t.Errorf("expected to find 2 favorite templates, found %d", foundCount)
	}
}

func TestTemplateRepo_ListRecentlyUsed(t *testing.T) {
	repo, _, ctx, projectID := setupTemplateRepoTest(t)

	// Create template and increment usage
	tmpl := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Used Template",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		CreatedBy:      "user",
	}
	repo.Create(ctx, tmpl)
	repo.IncrementUsage(ctx, tmpl.ID)

	repo.Create(ctx, &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Never Used",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		CreatedBy:      "user",
	})

	// List recently used
	recent, err := repo.ListRecentlyUsed(ctx, projectID, 10)
	if err != nil {
		t.Fatalf("ListRecentlyUsed: %v", err)
	}

	// Should find our used template
	found := false
	for _, r := range recent {
		if r.Name == "Used Template" {
			found = true
			if r.UsageCount < 1 {
				t.Error("used template should have usage_count > 0")
			}
			break
		}
	}
	if !found {
		t.Error("expected to find used template in recently used list")
	}

	// "Never Used" should not be in the list
	for _, r := range recent {
		if r.Name == "Never Used" {
			t.Error("template with 0 usage should not be in recently used")
		}
	}
}

func TestTemplateRepo_ToggleFavorite(t *testing.T) {
	repo, _, ctx, projectID := setupTemplateRepoTest(t)

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

	if err := repo.Create(ctx, tmpl); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Toggle to favorite
	if err := repo.ToggleFavorite(ctx, tmpl.ID, true); err != nil {
		t.Fatalf("ToggleFavorite: %v", err)
	}

	got, _ := repo.GetByID(ctx, tmpl.ID)
	if !got.IsFavorite {
		t.Error("expected IsFavorite=true")
	}

	// Toggle back to non-favorite
	if err := repo.ToggleFavorite(ctx, tmpl.ID, false); err != nil {
		t.Fatalf("ToggleFavorite: %v", err)
	}

	got, _ = repo.GetByID(ctx, tmpl.ID)
	if got.IsFavorite {
		t.Error("expected IsFavorite=false")
	}
}

func TestTemplateRepo_IncrementUsage(t *testing.T) {
	repo, _, ctx, projectID := setupTemplateRepoTest(t)

	tmpl := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           "Template",
		Description:    "desc",
		DefaultPrompt:  "prompt",
		Category:       models.CategoryActive,
		Priority:       2,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryFeature),
		CreatedBy:      "user",
	}

	if err := repo.Create(ctx, tmpl); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Initial usage count should be 0
	got, _ := repo.GetByID(ctx, tmpl.ID)
	if got.UsageCount != 0 {
		t.Errorf("expected initial UsageCount=0, got %d", got.UsageCount)
	}

	// Increment usage
	if err := repo.IncrementUsage(ctx, tmpl.ID); err != nil {
		t.Fatalf("IncrementUsage: %v", err)
	}

	got, _ = repo.GetByID(ctx, tmpl.ID)
	if got.UsageCount != 1 {
		t.Errorf("expected UsageCount=1, got %d", got.UsageCount)
	}

	// Increment again
	repo.IncrementUsage(ctx, tmpl.ID)
	got, _ = repo.GetByID(ctx, tmpl.ID)
	if got.UsageCount != 2 {
		t.Errorf("expected UsageCount=2, got %d", got.UsageCount)
	}
}

func TestTemplateRepo_ScanTemplates_EmptyResult(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTemplateRepo(db)
	ctx := context.Background()

	// Search from non-existent project with impossible query should return empty slice, not nil
	templates, err := repo.Search(ctx, "nonexistent-project", "ImpossibleQueryXYZ999")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if templates == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(templates) != 0 {
		t.Errorf("expected 0 templates, got %d", len(templates))
	}
}

func TestTemplateRepo_Create_GlobalTemplate(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTemplateRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// Create project for verification
	project := &models.Project{Name: "Any Project"}
	projectRepo.Create(ctx, project)

	// Create global template (nil project_id)
	tmpl := &models.TaskTemplate{
		ProjectID:      nil, // Global template
		Name:           "My Global Template",
		Description:    "Available to all projects",
		DefaultPrompt:  "Do something globally",
		Category:       models.CategoryActive,
		Priority:       2,
		Tag:            models.TagFeature,
		TagsJSON:       "[]",
		CategoryFilter: string(models.TaskTemplateCategoryAll),
		IsBuiltIn:      true,
		CreatedBy:      "system",
	}

	if err := repo.Create(ctx, tmpl); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, tmpl.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ProjectID != nil {
		t.Error("expected nil ProjectID for global template")
	}
	if !got.IsBuiltIn {
		t.Error("expected IsBuiltIn=true")
	}

	// Verify it appears in any project's list
	templates, err := repo.ListByProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	found := false
	for _, t := range templates {
		if t.ID == tmpl.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("global template should appear in any project's list")
	}
}

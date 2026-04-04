package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

// seedBuiltinPatterns creates built-in patterns for testing. The migration seeds
// these via INSERT...SELECT from projects, but in tests no projects exist at
// migration time so we need to seed manually.
func seedBuiltinPatterns(t *testing.T, repo *PatternRepo, projectID string) {
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

func TestPatternRepo_Create(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewPatternRepo(db)
	ctx := context.Background()

	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Test Pattern",
		Description:  "Test description",
		TemplateText: "Fix {{issue}} in {{file}}",
		Variables:    `["issue","file"]`,
		Category:     models.CategoryDebugging,
		IsBuiltin:    false,
		Tags:         `["test","debugging"]`,
	}

	err := repo.Create(ctx, pattern)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if pattern.ID == "" {
		t.Error("expected pattern ID to be set")
	}
	if pattern.CreatedAt.IsZero() {
		t.Error("expected created_at to be set")
	}
	if pattern.UpdatedAt.IsZero() {
		t.Error("expected updated_at to be set")
	}
}

func TestPatternRepo_GetByID(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewPatternRepo(db)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Get Test",
		Description:  "Get test",
		TemplateText: "Test {{var}}",
		Variables:    `["var"]`,
		Category:     models.CategoryTesting,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	repo.Create(ctx, pattern)

	// Get pattern
	got, err := repo.GetByID(ctx, pattern.ID)
	if err != nil {
		t.Fatalf("GetByID error: %v", err)
	}
	if got == nil {
		t.Fatal("expected pattern to be found")
	}
	if got.Title != "Get Test" {
		t.Errorf("expected title='Get Test', got %q", got.Title)
	}
	if got.Category != models.CategoryTesting {
		t.Errorf("expected category=%s, got %s", models.CategoryTesting, got.Category)
	}

	// Get non-existent
	notFound, err := repo.GetByID(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetByID nonexistent error: %v", err)
	}
	if notFound != nil {
		t.Error("expected nil for nonexistent pattern")
	}
}

func TestPatternRepo_ListByProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewPatternRepo(db)
	ctx := context.Background()

	seedBuiltinPatterns(t, repo, "default")

	patterns, err := repo.ListByProject(ctx, "default")
	if err != nil {
		t.Fatalf("ListByProject error: %v", err)
	}
	if len(patterns) < 10 {
		t.Errorf("expected at least 10 built-in patterns, got %d", len(patterns))
	}

	// Create custom pattern
	custom := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Custom Pattern",
		Description:  "Custom",
		TemplateText: "Custom {{var}}",
		Variables:    `["var"]`,
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	repo.Create(ctx, custom)

	// List again
	patterns, err = repo.ListByProject(ctx, "default")
	if err != nil {
		t.Fatalf("ListByProject after create error: %v", err)
	}
	if len(patterns) < 11 {
		t.Errorf("expected at least 11 patterns, got %d", len(patterns))
	}

	// Different project should have no patterns
	other, err := repo.ListByProject(ctx, "other-project")
	if err != nil {
		t.Fatalf("ListByProject other project error: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("expected 0 patterns for other project, got %d", len(other))
	}
}

func TestPatternRepo_ListByCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewPatternRepo(db)
	ctx := context.Background()

	seedBuiltinPatterns(t, repo, "default")

	// List debugging patterns (should have built-ins)
	patterns, err := repo.ListByCategory(ctx, "default", models.CategoryDebugging)
	if err != nil {
		t.Fatalf("ListByCategory error: %v", err)
	}
	if len(patterns) < 1 {
		t.Error("expected at least 1 debugging pattern")
	}

	// Create custom debugging pattern
	custom := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Custom Debug",
		Description:  "Custom debug",
		TemplateText: "Debug {{component}}",
		Variables:    `["component"]`,
		Category:     models.CategoryDebugging,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	repo.Create(ctx, custom)

	// List again
	patterns, err = repo.ListByCategory(ctx, "default", models.CategoryDebugging)
	if err != nil {
		t.Fatalf("ListByCategory after create error: %v", err)
	}
	found := false
	for _, p := range patterns {
		if p.Title == "Custom Debug" {
			found = true
			break
		}
	}
	if !found {
		t.Error("custom pattern not found in category list")
	}
}

func TestPatternRepo_ListMostPopular(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewPatternRepo(db)
	ctx := context.Background()

	// Create patterns with usage
	p1 := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Popular 1",
		Description:  "Popular",
		TemplateText: "Test",
		Variables:    "[]",
		Category:     models.CategoryTesting,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	repo.Create(ctx, p1)
	// Use it 5 times
	for i := 0; i < 5; i++ {
		repo.IncrementUsage(ctx, p1.ID)
	}

	p2 := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Popular 2",
		Description:  "More popular",
		TemplateText: "Test",
		Variables:    "[]",
		Category:     models.CategoryTesting,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	repo.Create(ctx, p2)
	// Use it 10 times
	for i := 0; i < 10; i++ {
		repo.IncrementUsage(ctx, p2.ID)
	}

	// List most popular
	patterns, err := repo.ListMostPopular(ctx, "default", 5)
	if err != nil {
		t.Fatalf("ListMostPopular error: %v", err)
	}
	if len(patterns) < 2 {
		t.Fatalf("expected at least 2 patterns, got %d", len(patterns))
	}
	// Most popular should be first
	if patterns[0].UsageCount < patterns[1].UsageCount {
		t.Error("patterns not sorted by usage count")
	}
}

func TestPatternRepo_ListRecentlyUsed(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewPatternRepo(db)
	ctx := context.Background()

	// Create and use pattern
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Recent",
		Description:  "Recent",
		TemplateText: "Test",
		Variables:    "[]",
		Category:     models.CategoryTesting,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	repo.Create(ctx, pattern)
	repo.IncrementUsage(ctx, pattern.ID)

	// List recently used
	patterns, err := repo.ListRecentlyUsed(ctx, "default", 5)
	if err != nil {
		t.Fatalf("ListRecentlyUsed error: %v", err)
	}
	if len(patterns) < 1 {
		t.Error("expected at least 1 recently used pattern")
	}
	// Verify it has last_used_at
	if patterns[0].LastUsedAt == nil {
		t.Error("expected last_used_at to be set")
	}
}

func TestPatternRepo_Search(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewPatternRepo(db)
	ctx := context.Background()

	// Create pattern with specific title and tags
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Unique Search Test Pattern",
		Description:  "Searchable description",
		TemplateText: "Test",
		Variables:    "[]",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         `["searchable","unique"]`,
	}
	repo.Create(ctx, pattern)

	// Search by title
	patterns, err := repo.Search(ctx, "default", "Unique Search", 10)
	if err != nil {
		t.Fatalf("Search by title error: %v", err)
	}
	if len(patterns) < 1 {
		t.Error("expected to find pattern by title")
	}

	// Search by description
	patterns, err = repo.Search(ctx, "default", "Searchable", 10)
	if err != nil {
		t.Fatalf("Search by description error: %v", err)
	}
	if len(patterns) < 1 {
		t.Error("expected to find pattern by description")
	}

	// Search by tag
	patterns, err = repo.Search(ctx, "default", "unique", 10)
	if err != nil {
		t.Fatalf("Search by tag error: %v", err)
	}
	if len(patterns) < 1 {
		t.Error("expected to find pattern by tag")
	}

	// Search nonexistent
	patterns, err = repo.Search(ctx, "default", "nonexistent-xyz", 10)
	if err != nil {
		t.Fatalf("Search nonexistent error: %v", err)
	}
	if len(patterns) != 0 {
		t.Error("expected no results for nonexistent search")
	}
}

func TestPatternRepo_Update(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewPatternRepo(db)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Update Test",
		Description:  "Original",
		TemplateText: "Original {{var}}",
		Variables:    `["var"]`,
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	repo.Create(ctx, pattern)

	// Update pattern
	pattern.Title = "Updated Title"
	pattern.Description = "Updated description"
	pattern.TemplateText = "Updated {{new_var}}"
	pattern.Variables = `["new_var"]`
	pattern.Category = models.CategoryRefactoring
	pattern.Tags = `["updated"]`

	err := repo.Update(ctx, pattern)
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}

	// Get updated pattern
	got, err := repo.GetByID(ctx, pattern.ID)
	if err != nil {
		t.Fatalf("GetByID after update error: %v", err)
	}
	if got.Title != "Updated Title" {
		t.Errorf("expected title='Updated Title', got %q", got.Title)
	}
	if got.Description != "Updated description" {
		t.Errorf("expected description='Updated description', got %q", got.Description)
	}
	if got.Category != models.CategoryRefactoring {
		t.Errorf("expected category=%s, got %s", models.CategoryRefactoring, got.Category)
	}
}

func TestPatternRepo_Delete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewPatternRepo(db)
	ctx := context.Background()

	// Create custom pattern
	custom := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Delete Test",
		Description:  "Will be deleted",
		TemplateText: "Test",
		Variables:    "[]",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	repo.Create(ctx, custom)

	// Delete it
	err := repo.Delete(ctx, custom.ID)
	if err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	// Verify deleted
	got, err := repo.GetByID(ctx, custom.ID)
	if err != nil {
		t.Fatalf("GetByID after delete error: %v", err)
	}
	if got != nil {
		t.Error("expected pattern to be deleted")
	}
}

func TestPatternRepo_Delete_BuiltinProtection(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewPatternRepo(db)
	ctx := context.Background()

	seedBuiltinPatterns(t, repo, "default")

	// Get a built-in pattern
	patterns, _ := repo.ListByProject(ctx, "default")
	var builtin *models.PromptPattern
	for i := range patterns {
		if patterns[i].IsBuiltin {
			builtin = &patterns[i]
			break
		}
	}
	if builtin == nil {
		t.Fatal("no built-in pattern found for test")
	}

	// Try to delete built-in (should not delete due to is_builtin=0 filter)
	err := repo.Delete(ctx, builtin.ID)
	if err != nil {
		t.Fatalf("Delete built-in error: %v", err)
	}

	// Built-in should still exist
	still, err := repo.GetByID(ctx, builtin.ID)
	if err != nil {
		t.Fatalf("GetByID after delete attempt error: %v", err)
	}
	if still == nil {
		t.Error("built-in pattern should not be deleted")
	}
}

func TestPatternRepo_IncrementUsage(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewPatternRepo(db)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Usage Test",
		Description:  "Test",
		TemplateText: "Test",
		Variables:    "[]",
		Category:     models.CategoryTesting,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	repo.Create(ctx, pattern)

	// Initial usage should be 0
	got, _ := repo.GetByID(ctx, pattern.ID)
	if got.UsageCount != 0 {
		t.Errorf("expected usage_count=0, got %d", got.UsageCount)
	}
	if got.LastUsedAt != nil {
		t.Error("expected last_used_at to be nil")
	}

	// Increment usage
	err := repo.IncrementUsage(ctx, pattern.ID)
	if err != nil {
		t.Fatalf("IncrementUsage error: %v", err)
	}

	// Verify increment
	got, _ = repo.GetByID(ctx, pattern.ID)
	if got.UsageCount != 1 {
		t.Errorf("expected usage_count=1, got %d", got.UsageCount)
	}
	if got.LastUsedAt == nil {
		t.Error("expected last_used_at to be set")
	}

	// Increment again
	repo.IncrementUsage(ctx, pattern.ID)
	got, _ = repo.GetByID(ctx, pattern.ID)
	if got.UsageCount != 2 {
		t.Errorf("expected usage_count=2, got %d", got.UsageCount)
	}
}

func TestPatternRepo_UsageHistory(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewPatternRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "History Test",
		Description:  "Test",
		TemplateText: "Test {{var}}",
		Variables:    `["var"]`,
		Category:     models.CategoryTesting,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	repo.Create(ctx, pattern)

	// Create task
	task := &models.Task{
		ProjectID:   "default",
		Title:       "Pattern Task",
		Prompt:      "Applied pattern",
		Category:    models.CategoryBacklog,
		Status:      models.StatusPending,
		ChainConfig: "{}",
	}
	taskRepo.Create(ctx, task)

	// Create usage history
	history := &models.PatternUsageHistory{
		PatternID:        pattern.ID,
		TaskID:           task.ID,
		VariablesApplied: `{"var":"test_value"}`,
		ResultStatus:     "unknown",
	}
	err := repo.CreateUsageHistory(ctx, history)
	if err != nil {
		t.Fatalf("CreateUsageHistory error: %v", err)
	}
	if history.ID == "" {
		t.Error("expected history ID to be set")
	}
	if history.CreatedAt.IsZero() {
		t.Error("expected created_at to be set")
	}

	// Update status
	err = repo.UpdateUsageStatus(ctx, task.ID, "success")
	if err != nil {
		t.Fatalf("UpdateUsageStatus error: %v", err)
	}

	// Get usage history
	histories, err := repo.GetUsageHistory(ctx, pattern.ID, 10)
	if err != nil {
		t.Fatalf("GetUsageHistory error: %v", err)
	}
	if len(histories) != 1 {
		t.Fatalf("expected 1 history record, got %d", len(histories))
	}
	if histories[0].ResultStatus != "success" {
		t.Errorf("expected status='success', got %q", histories[0].ResultStatus)
	}
	if histories[0].VariablesApplied != `{"var":"test_value"}` {
		t.Errorf("expected variables applied, got %q", histories[0].VariablesApplied)
	}
}

func TestPatternRepo_CountByCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewPatternRepo(db)
	ctx := context.Background()

	seedBuiltinPatterns(t, repo, "default")

	// Count categories (should have built-ins)
	counts, err := repo.CountByCategory(ctx, "default")
	if err != nil {
		t.Fatalf("CountByCategory error: %v", err)
	}
	if len(counts) < 1 {
		t.Error("expected at least 1 category with patterns")
	}

	// Create custom pattern in new category
	custom := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Count Test",
		Description:  "Test",
		TemplateText: "Test",
		Variables:    "[]",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	repo.Create(ctx, custom)

	// Count again
	counts, err = repo.CountByCategory(ctx, "default")
	if err != nil {
		t.Fatalf("CountByCategory after create error: %v", err)
	}
	if counts[string(models.CategoryCustom)] < 1 {
		t.Error("expected at least 1 custom pattern")
	}
}

func TestPatternRepo_GetStats(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewPatternRepo(db)
	ctx := context.Background()

	seedBuiltinPatterns(t, repo, "default")

	// Get stats
	stats, err := repo.GetStats(ctx, "default")
	if err != nil {
		t.Fatalf("GetStats error: %v", err)
	}
	if stats.TotalPatterns < 10 {
		t.Errorf("expected at least 10 patterns (built-ins), got %d", stats.TotalPatterns)
	}
	if stats.BuiltinPatterns < 10 {
		t.Errorf("expected at least 10 built-in patterns, got %d", stats.BuiltinPatterns)
	}
	if stats.CustomPatterns != 0 {
		t.Errorf("expected 0 custom patterns initially, got %d", stats.CustomPatterns)
	}
	if stats.CategoriesUsed < 1 {
		t.Error("expected at least 1 category used")
	}

	// Create custom pattern and use it
	custom := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Stats Test",
		Description:  "Test",
		TemplateText: "Test",
		Variables:    "[]",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	repo.Create(ctx, custom)
	repo.IncrementUsage(ctx, custom.ID)

	// Get stats again
	stats, err = repo.GetStats(ctx, "default")
	if err != nil {
		t.Fatalf("GetStats after create error: %v", err)
	}
	if stats.CustomPatterns != 1 {
		t.Errorf("expected 1 custom pattern, got %d", stats.CustomPatterns)
	}
	if stats.TotalUsages < 1 {
		t.Errorf("expected at least 1 usage, got %d", stats.TotalUsages)
	}
	if stats.AvgUsagePerPattern <= 0 {
		t.Error("expected positive average usage per pattern")
	}
}

func TestPatternRepo_ExistsByTitle(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewPatternRepo(db)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Exists Test Pattern",
		Description:  "Test",
		TemplateText: "Test",
		Variables:    "[]",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	repo.Create(ctx, pattern)

	// Check exists
	exists, err := repo.ExistsByTitle(ctx, "default", "Exists Test Pattern")
	if err != nil {
		t.Fatalf("ExistsByTitle error: %v", err)
	}
	if !exists {
		t.Error("expected pattern to exist")
	}

	// Check not exists
	exists, err = repo.ExistsByTitle(ctx, "default", "Nonexistent Pattern")
	if err != nil {
		t.Fatalf("ExistsByTitle nonexistent error: %v", err)
	}
	if exists {
		t.Error("expected pattern not to exist")
	}

	// Different project
	exists, err = repo.ExistsByTitle(ctx, "other-project", "Exists Test Pattern")
	if err != nil {
		t.Fatalf("ExistsByTitle other project error: %v", err)
	}
	if exists {
		t.Error("expected pattern not to exist in other project")
	}
}

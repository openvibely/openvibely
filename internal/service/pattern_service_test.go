package service

import (
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

// seedBuiltinPatterns creates built-in patterns for testing. The migration seeds
// these via INSERT...SELECT from projects, but in tests no projects exist at
// migration time so we need to seed manually.
func seedBuiltinPatterns(t *testing.T, repo *repository.PatternRepo, projectID string) {
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

func TestPatternService_GetDashboard(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	seedBuiltinPatterns(t, patternRepo, "default")

	// Get dashboard (should have built-in patterns)
	data, err := svc.GetDashboard(ctx, "default")
	if err != nil {
		t.Fatalf("GetDashboard error: %v", err)
	}
	if data.TotalPatterns < 10 {
		t.Errorf("expected at least 10 patterns, got %d", data.TotalPatterns)
	}
	if data.BuiltinCount < 10 {
		t.Errorf("expected at least 10 built-ins, got %d", data.BuiltinCount)
	}
	if data.CustomPatternCount != 0 {
		t.Errorf("expected 0 custom patterns initially, got %d", data.CustomPatternCount)
	}
	if len(data.AllPatterns) < 10 {
		t.Errorf("expected at least 10 in AllPatterns, got %d", len(data.AllPatterns))
	}
}

func TestPatternService_CreatePattern(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Service Create Test",
		Description:  "Test description",
		TemplateText: "Fix {{issue}} in {{file}} for {{module}}",
		Category:     models.CategoryDebugging,
		IsBuiltin:    false,
	}

	// Set tags
	tags := []string{"test", "debugging"}
	pattern.SetTags(tags)

	err := svc.CreatePattern(ctx, pattern)
	if err != nil {
		t.Fatalf("CreatePattern error: %v", err)
	}
	if pattern.ID == "" {
		t.Error("expected pattern ID to be set")
	}

	// Verify variables were auto-extracted
	vars, err := pattern.ParseVariables()
	if err != nil {
		t.Fatalf("ParseVariables error: %v", err)
	}
	if len(vars) != 3 {
		t.Errorf("expected 3 variables, got %d: %v", len(vars), vars)
	}
	// Verify all expected variables are present
	expectedVars := map[string]bool{"issue": true, "file": true, "module": true}
	for _, v := range vars {
		if !expectedVars[v] {
			t.Errorf("unexpected variable: %s", v)
		}
	}
}

func TestPatternService_CreatePattern_DuplicateTitle(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Create first pattern
	pattern1 := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Duplicate Title Test",
		Description:  "First",
		TemplateText: "Test",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	svc.CreatePattern(ctx, pattern1)

	// Try to create duplicate
	pattern2 := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Duplicate Title Test",
		Description:  "Second",
		TemplateText: "Different",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	err := svc.CreatePattern(ctx, pattern2)
	if err == nil {
		t.Error("expected error for duplicate title")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %v", err)
	}
}

func TestPatternService_UpdatePattern(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Update Service Test",
		Description:  "Original",
		TemplateText: "Original {{var}}",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	svc.CreatePattern(ctx, pattern)

	// Update pattern
	pattern.Title = "Updated Title"
	pattern.Description = "Updated description"
	pattern.TemplateText = "Updated {{new_var}} and {{another}}"
	pattern.Category = models.CategoryRefactoring

	err := svc.UpdatePattern(ctx, pattern)
	if err != nil {
		t.Fatalf("UpdatePattern error: %v", err)
	}

	// Verify variables were auto-extracted from updated template
	vars, err := pattern.ParseVariables()
	if err != nil {
		t.Fatalf("ParseVariables error: %v", err)
	}
	if len(vars) != 2 {
		t.Errorf("expected 2 variables, got %d: %v", len(vars), vars)
	}

	// Get updated pattern
	got, _ := patternRepo.GetByID(ctx, pattern.ID)
	if got.Title != "Updated Title" {
		t.Errorf("expected title='Updated Title', got %q", got.Title)
	}
}

func TestPatternService_UpdatePattern_BuiltinProtection(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	seedBuiltinPatterns(t, patternRepo, "default")

	// Get a built-in pattern
	patterns, _ := patternRepo.ListByProject(ctx, "default")
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

	// Try to update built-in
	builtin.Title = "Modified Builtin"
	err := svc.UpdatePattern(ctx, builtin)
	if err == nil {
		t.Error("expected error when updating built-in pattern")
	}
	if !strings.Contains(err.Error(), "cannot modify built-in") {
		t.Errorf("expected 'cannot modify built-in' error, got: %v", err)
	}
}

func TestPatternService_DeletePattern(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Create custom pattern
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Delete Service Test",
		Description:  "Will be deleted",
		TemplateText: "Test",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	svc.CreatePattern(ctx, pattern)

	// Delete it
	err := svc.DeletePattern(ctx, pattern.ID)
	if err != nil {
		t.Fatalf("DeletePattern error: %v", err)
	}

	// Verify deleted
	got, _ := patternRepo.GetByID(ctx, pattern.ID)
	if got != nil {
		t.Error("expected pattern to be deleted")
	}
}

func TestPatternService_DeletePattern_BuiltinProtection(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	seedBuiltinPatterns(t, patternRepo, "default")

	// Get a built-in pattern
	patterns, _ := patternRepo.ListByProject(ctx, "default")
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

	// Try to delete built-in
	err := svc.DeletePattern(ctx, builtin.ID)
	if err == nil {
		t.Error("expected error when deleting built-in pattern")
	}
	if !strings.Contains(err.Error(), "cannot delete built-in") {
		t.Errorf("expected 'cannot delete built-in' error, got: %v", err)
	}

	// Verify still exists
	still, _ := patternRepo.GetByID(ctx, builtin.ID)
	if still == nil {
		t.Error("built-in pattern should not be deleted")
	}
}

func TestPatternService_ApplyPattern(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Apply Test Pattern",
		Description:  "Test",
		TemplateText: "Fix {{issue}} in {{file}}",
		Category:     models.CategoryDebugging,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	svc.CreatePattern(ctx, pattern)

	// Apply pattern
	variableValues := map[string]string{
		"issue": "null pointer",
		"file":  "handler.go",
	}

	task, err := svc.ApplyPattern(
		ctx,
		pattern.ID,
		variableValues,
		"Fix null pointer in handler",
		"default",
		nil,
		models.CategoryBacklog,
	)
	if err != nil {
		t.Fatalf("ApplyPattern error: %v", err)
	}
	if task == nil {
		t.Fatal("expected task to be created")
	}
	if task.ID == "" {
		t.Error("expected task ID to be set")
	}
	if task.Title != "Fix null pointer in handler" {
		t.Errorf("expected title='Fix null pointer in handler', got %q", task.Title)
	}

	// Verify prompt has variables substituted
	expectedPrompt := "Fix null pointer in handler.go"
	if task.Prompt != expectedPrompt {
		t.Errorf("expected prompt=%q, got %q", expectedPrompt, task.Prompt)
	}

	// Verify usage was incremented
	updatedPattern, _ := patternRepo.GetByID(ctx, pattern.ID)
	if updatedPattern.UsageCount != 1 {
		t.Errorf("expected usage_count=1, got %d", updatedPattern.UsageCount)
	}
	if updatedPattern.LastUsedAt == nil {
		t.Error("expected last_used_at to be set")
	}

	// Verify usage history was created
	histories, _ := patternRepo.GetUsageHistory(ctx, pattern.ID, 10)
	if len(histories) != 1 {
		t.Fatalf("expected 1 history record, got %d", len(histories))
	}
	if histories[0].TaskID != task.ID {
		t.Error("history record should link to created task")
	}
}

func TestPatternService_ApplyPattern_MissingVariable(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Missing Var Test",
		Description:  "Test",
		TemplateText: "Fix {{issue}} in {{file}}",
		Category:     models.CategoryDebugging,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	svc.CreatePattern(ctx, pattern)

	// Try to apply with missing variable
	variableValues := map[string]string{
		"issue": "null pointer",
		// Missing "file" variable
	}

	_, err := svc.ApplyPattern(
		ctx,
		pattern.ID,
		variableValues,
		"Test Task",
		"default",
		nil,
		models.CategoryBacklog,
	)
	if err == nil {
		t.Error("expected error for missing variable")
	}
	if !strings.Contains(err.Error(), "missing required variable") {
		t.Errorf("expected 'missing required variable' error, got: %v", err)
	}
}

func TestPatternService_GetPattern(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Get Pattern Test",
		Description:  "Test",
		TemplateText: "Test",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	svc.CreatePattern(ctx, pattern)

	// Get pattern
	got, err := svc.GetPattern(ctx, pattern.ID)
	if err != nil {
		t.Fatalf("GetPattern error: %v", err)
	}
	if got == nil {
		t.Fatal("expected pattern to be found")
	}
	if got.Title != "Get Pattern Test" {
		t.Errorf("expected title='Get Pattern Test', got %q", got.Title)
	}
}

func TestPatternService_ListByCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Create pattern in specific category
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Category Test",
		Description:  "Test",
		TemplateText: "Test",
		Category:     models.CategoryRefactoring,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	svc.CreatePattern(ctx, pattern)

	// List by category
	patterns, err := svc.ListByCategory(ctx, "default", models.CategoryRefactoring)
	if err != nil {
		t.Fatalf("ListByCategory error: %v", err)
	}
	found := false
	for _, p := range patterns {
		if p.Title == "Category Test" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find pattern in category list")
	}
}

func TestPatternService_Search(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Create pattern with unique title
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Unique Search Service Test",
		Description:  "Unique description",
		TemplateText: "Test",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         `["unique","searchable"]`,
	}
	svc.CreatePattern(ctx, pattern)

	// Search
	patterns, err := svc.Search(ctx, "default", "Unique Search Service")
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(patterns) < 1 {
		t.Error("expected to find pattern by search")
	}
}

func TestPatternService_ExportPatterns(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Create custom pattern
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Export Test",
		Description:  "Export test",
		TemplateText: "Test {{var}}",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         `["export"]`,
	}
	svc.CreatePattern(ctx, pattern)

	// Export patterns
	data, err := svc.ExportPatterns(ctx, "default")
	if err != nil {
		t.Fatalf("ExportPatterns error: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty export data")
	}

	// Verify JSON format
	jsonStr := string(data)
	if !strings.Contains(jsonStr, "Export Test") {
		t.Error("expected exported pattern in JSON")
	}
	// Built-ins should not be exported
	if strings.Contains(jsonStr, `"is_builtin":true`) {
		t.Error("built-in patterns should not be exported")
	}
}

func TestPatternService_ImportPatterns(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Create JSON data to import
	jsonData := `[
		{
			"title": "Import Test 1",
			"description": "First import",
			"template_text": "Test {{var1}}",
			"variables": "[\"var1\"]",
			"category": "custom",
			"tags": "[\"import\"]"
		},
		{
			"title": "Import Test 2",
			"description": "Second import",
			"template_text": "Test {{var2}}",
			"variables": "[\"var2\"]",
			"category": "debugging",
			"tags": "[]"
		}
	]`

	// Import patterns
	imported, errors, err := svc.ImportPatterns(ctx, "default", []byte(jsonData))
	if err != nil {
		t.Fatalf("ImportPatterns error: %v", err)
	}
	if imported != 2 {
		t.Errorf("expected 2 imported patterns, got %d", imported)
	}
	if len(errors) != 0 {
		t.Errorf("expected no errors, got: %v", errors)
	}

	// Verify patterns were imported
	patterns, _ := patternRepo.ListByProject(ctx, "default")
	found1, found2 := false, false
	for _, p := range patterns {
		if p.Title == "Import Test 1" {
			found1 = true
			if p.IsBuiltin {
				t.Error("imported pattern should not be built-in")
			}
			if p.UsageCount != 0 {
				t.Error("imported pattern should have zero usage")
			}
		}
		if p.Title == "Import Test 2" {
			found2 = true
		}
	}
	if !found1 || !found2 {
		t.Error("expected both imported patterns to be found")
	}
}

func TestPatternService_ImportPatterns_InvalidJSON(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Invalid JSON
	jsonData := `{not valid json`

	_, _, err := svc.ImportPatterns(ctx, "default", []byte(jsonData))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("expected 'invalid JSON' error, got: %v", err)
	}
}

func TestPatternService_ImportPatterns_Duplicate(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Create existing pattern
	existing := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Duplicate Import",
		Description:  "Existing",
		TemplateText: "Test",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	svc.CreatePattern(ctx, existing)

	// Try to import duplicate
	jsonData := `[
		{
			"title": "Duplicate Import",
			"description": "New import",
			"template_text": "Different",
			"category": "custom",
			"tags": "[]"
		}
	]`

	imported, errors, err := svc.ImportPatterns(ctx, "default", []byte(jsonData))
	if err != nil {
		t.Fatalf("ImportPatterns error: %v", err)
	}
	if imported != 0 {
		t.Errorf("expected 0 imported patterns, got %d", imported)
	}
	if len(errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errors))
	}
	if !strings.Contains(errors[0], "already exists") {
		t.Errorf("expected 'already exists' error, got: %s", errors[0])
	}
}

func TestPatternService_DuplicatePattern(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Create original pattern
	original := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Original Pattern",
		Description:  "Original description",
		TemplateText: "Original {{var}}",
		Category:     models.CategoryDebugging,
		IsBuiltin:    false,
		Tags:         `["original"]`,
	}
	svc.CreatePattern(ctx, original)

	// Duplicate it
	copy, err := svc.DuplicatePattern(ctx, original.ID)
	if err != nil {
		t.Fatalf("DuplicatePattern error: %v", err)
	}
	if copy == nil {
		t.Fatal("expected duplicate pattern to be created")
	}
	if copy.ID == original.ID {
		t.Error("duplicate should have different ID")
	}
	if copy.Title != "Original Pattern (Copy)" {
		t.Errorf("expected title='Original Pattern (Copy)', got %q", copy.Title)
	}
	if copy.Description != original.Description {
		t.Error("description should match original")
	}
	if copy.TemplateText != original.TemplateText {
		t.Error("template text should match original")
	}
	if copy.IsBuiltin {
		t.Error("duplicate should not be built-in")
	}
}

func TestPatternService_DuplicatePattern_BuiltIn(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	seedBuiltinPatterns(t, patternRepo, "default")

	// Get a built-in pattern
	patterns, _ := patternRepo.ListByProject(ctx, "default")
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

	// Duplicate built-in
	copy, err := svc.DuplicatePattern(ctx, builtin.ID)
	if err != nil {
		t.Fatalf("DuplicatePattern builtin error: %v", err)
	}
	if copy.IsBuiltin {
		t.Error("duplicate of built-in should not be built-in")
	}
	if copy.Title == builtin.Title {
		t.Error("duplicate should have modified title")
	}
}

func TestPatternService_DuplicatePattern_MultipleConflicts(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Create original
	original := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Conflict Test",
		Description:  "Test",
		TemplateText: "Test",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	svc.CreatePattern(ctx, original)

	// Duplicate multiple times
	copy1, _ := svc.DuplicatePattern(ctx, original.ID)
	if copy1.Title != "Conflict Test (Copy)" {
		t.Errorf("expected first copy title='Conflict Test (Copy)', got %q", copy1.Title)
	}

	copy2, _ := svc.DuplicatePattern(ctx, original.ID)
	if copy2.Title != "Conflict Test (Copy) 2" {
		t.Errorf("expected second copy title='Conflict Test (Copy) 2', got %q", copy2.Title)
	}

	copy3, _ := svc.DuplicatePattern(ctx, original.ID)
	if copy3.Title != "Conflict Test (Copy) 3" {
		t.Errorf("expected third copy title='Conflict Test (Copy) 3', got %q", copy3.Title)
	}
}

func TestPatternService_GetUsageHistory(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "History Test",
		Description:  "Test",
		TemplateText: "Test {{var}}",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	svc.CreatePattern(ctx, pattern)

	// Apply pattern (creates usage history)
	variableValues := map[string]string{"var": "test"}
	svc.ApplyPattern(ctx, pattern.ID, variableValues, "Test Task", "default", nil, models.CategoryBacklog)

	// Get usage history
	histories, err := svc.GetUsageHistory(ctx, pattern.ID, 10)
	if err != nil {
		t.Fatalf("GetUsageHistory error: %v", err)
	}
	if len(histories) != 1 {
		t.Fatalf("expected 1 history record, got %d", len(histories))
	}
}

func TestPatternService_UpdateTaskResultStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	patternRepo := repository.NewPatternRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	svc := NewPatternService(patternRepo, taskRepo)
	ctx := context.Background()

	// Create pattern
	pattern := &models.PromptPattern{
		ProjectID:    "default",
		Title:        "Status Test",
		Description:  "Test",
		TemplateText: "Test",
		Category:     models.CategoryCustom,
		IsBuiltin:    false,
		Tags:         "[]",
	}
	svc.CreatePattern(ctx, pattern)

	// Apply pattern
	variableValues := map[string]string{}
	task, err := svc.ApplyPattern(ctx, pattern.ID, variableValues, "Test Task", "default", nil, models.CategoryBacklog)
	if err != nil {
		t.Fatalf("ApplyPattern error: %v", err)
	}

	// Update status
	err = svc.UpdateTaskResultStatus(ctx, task.ID, "success")
	if err != nil {
		t.Fatalf("UpdateTaskResultStatus error: %v", err)
	}

	// Verify status was updated
	histories, _ := patternRepo.GetUsageHistory(ctx, pattern.ID, 10)
	if len(histories) != 1 {
		t.Fatalf("expected 1 history, got %d", len(histories))
	}
	if histories[0].ResultStatus != "success" {
		t.Errorf("expected status='success', got %q", histories[0].ResultStatus)
	}
}

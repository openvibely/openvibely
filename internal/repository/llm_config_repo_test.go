package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestLLMConfigRepo_CreateAndGetByID(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	a := &models.LLMConfig{
		Name:        "Test Model",
		Provider:    models.ProviderAnthropic,
		Model:       "claude-sonnet-4-5-20250929",
		MaxTokens:   4096,
		Temperature: 0.5,
		IsDefault:   false,
	}

	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if a.ID == "" {
		t.Fatal("expected ID to be set after Create")
	}

	got, err := repo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected model config, got nil")
	}
	if got.Name != "Test Model" {
		t.Errorf("expected Name=Test Model, got %q", got.Name)
	}
	if got.Provider != models.ProviderAnthropic {
		t.Errorf("expected Provider=anthropic, got %q", got.Provider)
	}
	if got.Temperature != 0.5 {
		t.Errorf("expected Temperature=0.5, got %f", got.Temperature)
	}
}

func TestLLMConfigRepo_Create_FirstModelAutoDefault(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}

	a := &models.LLMConfig{
		Name:      "First Model",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		MaxTokens: 4096,
		IsDefault: false,
	}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected model config, got nil")
	}
	if !got.IsDefault {
		t.Fatal("expected first created model to be default")
	}

	def, err := repo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if def == nil || def.ID != a.ID {
		t.Fatalf("expected default model ID %s, got %+v", a.ID, def)
	}
}

func TestLLMConfigRepo_GetDefault(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Migration 003 seeds a default Claude Max model config
	def, err := repo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if def == nil {
		t.Fatal("expected seeded default model config, got nil")
	}
	if def.Provider != models.ProviderAnthropic {
		t.Errorf("expected default Provider=anthropic, got %q", def.Provider)
	}
	if !def.IsDefault {
		t.Error("expected IsDefault=true")
	}
}

func TestLLMConfigRepo_CreateDefault_UnsetsOthers(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Seeded default exists from migration 003
	original, _ := repo.GetDefault(ctx)
	if original == nil {
		t.Fatal("expected seeded default model config")
	}

	// Create a new default model config
	newConfig := &models.LLMConfig{
		Name:      "New Default",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		APIKey:    "sk-test",
		MaxTokens: 2048,
		IsDefault: true,
	}
	if err := repo.Create(ctx, newConfig); err != nil {
		t.Fatalf("Create new default: %v", err)
	}

	// Old default should no longer be default
	oldConfig, _ := repo.GetByID(ctx, original.ID)
	if oldConfig.IsDefault {
		t.Error("expected old model config IsDefault=false after new default created")
	}

	// New config should be default
	def, _ := repo.GetDefault(ctx)
	if def.ID != newConfig.ID {
		t.Errorf("expected new default ID=%s, got %s", newConfig.ID, def.ID)
	}
}

func TestLLMConfigRepo_List(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Should have seeded default
	configs, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(configs) < 1 {
		t.Fatal("expected at least 1 seeded model config")
	}

	// Add another
	repo.Create(ctx, &models.LLMConfig{
		Name:      "Second",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-haiku-4-5-20251001",
		MaxTokens: 1024,
	})

	configs, _ = repo.List(ctx)
	if len(configs) < 2 {
		t.Errorf("expected at least 2 model configs, got %d", len(configs))
	}
}

func TestLLMConfigRepo_Update(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	a := &models.LLMConfig{
		Name:      "Original",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		MaxTokens: 4096,
	}
	repo.Create(ctx, a)

	a.Name = "Updated"
	a.MaxTokens = 8192
	if err := repo.Update(ctx, a); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := repo.GetByID(ctx, a.ID)
	if got.Name != "Updated" {
		t.Errorf("expected Name=Updated, got %q", got.Name)
	}
	if got.MaxTokens != 8192 {
		t.Errorf("expected MaxTokens=8192, got %d", got.MaxTokens)
	}
}

func TestLLMConfigRepo_Delete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	a := &models.LLMConfig{
		Name:     "ToDelete",
		Provider: models.ProviderAnthropic,
		Model:    "claude-sonnet-4-5-20250929",
	}
	repo.Create(ctx, a)

	if err := repo.Delete(ctx, a.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, _ := repo.GetByID(ctx, a.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestLLMConfigRepo_Delete_OnlyModelAllowed(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}

	only := &models.LLMConfig{
		Name:      "Only",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		MaxTokens: 4096,
	}
	if err := repo.Create(ctx, only); err != nil {
		t.Fatalf("Create only model: %v", err)
	}

	if err := repo.Delete(ctx, only.ID); err != nil {
		t.Fatalf("Delete only model: %v", err)
	}

	count, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no models after delete, got %d", count)
	}
	def, err := repo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if def != nil {
		t.Fatal("expected no default model after deleting only model")
	}
}

func TestLLMConfigRepo_UpdateDefault_UnsetsOthers(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Seeded default exists from migration 003
	original, _ := repo.GetDefault(ctx)
	if original == nil {
		t.Fatal("expected seeded default model config")
	}

	// Create a non-default model config
	second := &models.LLMConfig{
		Name:      "Second",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		MaxTokens: 4096,
		IsDefault: false,
	}
	if err := repo.Create(ctx, second); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update second to be the default
	second.IsDefault = true
	if err := repo.Update(ctx, second); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Second should now be default
	def, _ := repo.GetDefault(ctx)
	if def.ID != second.ID {
		t.Errorf("expected new default ID=%s, got %s", second.ID, def.ID)
	}

	// Original should no longer be default
	orig, _ := repo.GetByID(ctx, original.ID)
	if orig.IsDefault {
		t.Error("expected original model config IsDefault=false after update")
	}
}

func TestLLMConfigRepo_CreateNonDefault_PreservesExisting(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Seeded default exists from migration 003
	original, _ := repo.GetDefault(ctx)
	if original == nil {
		t.Fatal("expected seeded default model config")
	}

	// Create a non-default model config
	newConfig := &models.LLMConfig{
		Name:      "Non-Default",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		APIKey:    "sk-test",
		MaxTokens: 2048,
		IsDefault: false,
	}
	if err := repo.Create(ctx, newConfig); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Original should still be default
	def, _ := repo.GetDefault(ctx)
	if def.ID != original.ID {
		t.Errorf("expected original default ID=%s preserved, got %s", original.ID, def.ID)
	}
}

func TestLLMConfigRepo_GetByID_NotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	got, err := repo.GetByID(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetByID should not error on not found: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent ID")
	}
}

func TestLLMConfigRepo_Delete_WithTaskReferences(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmRepo := NewLLMConfigRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create a non-default model config to delete
	config := &models.LLMConfig{
		Name:     "Config To Delete",
		Provider: models.ProviderAnthropic,
		Model:    "claude-sonnet-4-5-20250929",
	}
	if err := llmRepo.Create(ctx, config); err != nil {
		t.Fatalf("Create model config: %v", err)
	}

	// Create a project for the task
	proj := &models.Project{Name: "Test Project", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, proj); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	// Create a task referencing the model config
	configID := config.ID
	task := &models.Task{
		ProjectID: proj.ID,
		Title:     "Test Task",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		AgentID:   &configID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Verify task has agent_id set
	gotTask, _ := taskRepo.GetByID(ctx, task.ID)
	if gotTask.AgentID == nil || *gotTask.AgentID != config.ID {
		t.Fatal("expected task to have agent_id set")
	}

	// Delete the model config - should succeed by nullifying references
	if err := llmRepo.Delete(ctx, config.ID); err != nil {
		t.Fatalf("Delete model config with task references: %v", err)
	}

	// Verify model config is deleted
	gotConfig, _ := llmRepo.GetByID(ctx, config.ID)
	if gotConfig != nil {
		t.Error("expected model config to be deleted")
	}

	// Verify task still exists but agent_id is NULL
	gotTask, _ = taskRepo.GetByID(ctx, task.ID)
	if gotTask == nil {
		t.Fatal("expected task to still exist after model config deletion")
	}
	if gotTask.AgentID != nil {
		t.Errorf("expected task agent_id to be NULL after model config deletion, got %v", *gotTask.AgentID)
	}
}

func TestLLMConfigRepo_Delete_WithExecutionReferences(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmRepo := NewLLMConfigRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	// Create a non-default model config
	config := &models.LLMConfig{
		Name:     "Config With Executions",
		Provider: models.ProviderAnthropic,
		Model:    "claude-sonnet-4-5-20250929",
	}
	if err := llmRepo.Create(ctx, config); err != nil {
		t.Fatalf("Create model config: %v", err)
	}

	// Create a project and task
	proj := &models.Project{Name: "Test Project", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, proj); err != nil {
		t.Fatalf("Create project: %v", err)
	}
	task := &models.Task{
		ProjectID: proj.ID,
		Title:     "Test Task",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Create an execution referencing the model config
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: config.ID,
		Status:        models.ExecCompleted,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("Create execution: %v", err)
	}

	// Delete the model config - should succeed by nullifying execution references
	if err := llmRepo.Delete(ctx, config.ID); err != nil {
		t.Fatalf("Delete model config with execution references: %v", err)
	}

	// Verify model config is deleted
	gotConfig, _ := llmRepo.GetByID(ctx, config.ID)
	if gotConfig != nil {
		t.Error("expected model config to be deleted")
	}

	// Verify execution still exists but agent_config_id is empty
	gotExec, _ := execRepo.GetByID(ctx, exec.ID)
	if gotExec == nil {
		t.Fatal("expected execution to still exist after model config deletion")
	}
	if gotExec.AgentConfigID != "" {
		t.Errorf("expected execution agent_config_id to be empty, got %v", gotExec.AgentConfigID)
	}
}

func TestLLMConfigRepo_Count(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Should have at least the seeded default
	count, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count < 1 {
		t.Fatalf("expected count >= 1, got %d", count)
	}

	// Add another model
	repo.Create(ctx, &models.LLMConfig{
		Name:     "Extra",
		Provider: models.ProviderAnthropic,
		Model:    "claude-sonnet-4-5-20250929",
	})

	count2, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count after create: %v", err)
	}
	if count2 != count+1 {
		t.Errorf("expected count=%d, got %d", count+1, count2)
	}
}

func TestLLMConfigRepo_TransferDefaultAndDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Get the seeded default
	original, err := repo.GetDefault(ctx)
	if err != nil || original == nil {
		t.Fatal("expected seeded default model config")
	}

	// Create a second non-default model
	second := &models.LLMConfig{
		Name:      "Second Model",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		MaxTokens: 4096,
		IsDefault: false,
	}
	if err := repo.Create(ctx, second); err != nil {
		t.Fatalf("Create second model: %v", err)
	}

	// Transfer default from original to second, then delete original
	if err := repo.TransferDefaultAndDelete(ctx, original.ID, second.ID); err != nil {
		t.Fatalf("TransferDefaultAndDelete: %v", err)
	}

	// Original should be deleted
	got, _ := repo.GetByID(ctx, original.ID)
	if got != nil {
		t.Error("expected original model to be deleted")
	}

	// Second should now be the default
	newDefault, err := repo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault after transfer: %v", err)
	}
	if newDefault == nil {
		t.Fatal("expected a default model after transfer")
	}
	if newDefault.ID != second.ID {
		t.Errorf("expected new default ID=%s, got %s", second.ID, newDefault.ID)
	}
	if !newDefault.IsDefault {
		t.Error("expected new default IsDefault=true")
	}
}

func TestLLMConfigRepo_Delete_DefaultAutoReassigns(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	originalDefault, err := repo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if originalDefault == nil {
		t.Fatal("expected seeded default model config")
	}

	replacement := &models.LLMConfig{
		Name:      "Replacement",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-haiku-4-5-20251001",
		MaxTokens: 1024,
		IsDefault: false,
	}
	if err := repo.Create(ctx, replacement); err != nil {
		t.Fatalf("Create replacement model: %v", err)
	}

	if err := repo.Delete(ctx, originalDefault.ID); err != nil {
		t.Fatalf("Delete original default: %v", err)
	}

	def, err := repo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault after delete: %v", err)
	}
	if def == nil {
		t.Fatal("expected default model after deleting previous default")
	}
	if def.ID != replacement.ID {
		t.Fatalf("expected replacement model %s to become default, got %s", replacement.ID, def.ID)
	}
}

func TestLLMConfigRepo_GetByIDs(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create two configs
	a := &models.LLMConfig{Name: "Alpha", Provider: models.ProviderAnthropic, Model: "claude-sonnet-4-5-20250929"}
	b := &models.LLMConfig{Name: "Beta", Provider: models.ProviderAnthropic, Model: "claude-sonnet-4-5-20250929"}
	repo.Create(ctx, a)
	repo.Create(ctx, b)

	// Batch get both
	result, err := repo.GetByIDs(ctx, []string{a.ID, b.ID})
	if err != nil {
		t.Fatalf("GetByIDs: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(result))
	}
	if result[a.ID].Name != "Alpha" {
		t.Errorf("expected Alpha, got %q", result[a.ID].Name)
	}
	if result[b.ID].Name != "Beta" {
		t.Errorf("expected Beta, got %q", result[b.ID].Name)
	}

	// Nonexistent ID should not appear
	result2, err := repo.GetByIDs(ctx, []string{a.ID, "nonexistent"})
	if err != nil {
		t.Fatalf("GetByIDs with nonexistent: %v", err)
	}
	if len(result2) != 1 {
		t.Fatalf("expected 1 config, got %d", len(result2))
	}

	// Empty input
	result3, err := repo.GetByIDs(ctx, []string{})
	if err != nil {
		t.Fatalf("GetByIDs empty: %v", err)
	}
	if len(result3) != 0 {
		t.Fatalf("expected 0 configs, got %d", len(result3))
	}
}

func TestLLMConfigRepo_TransferDefaultAndDelete_PreservesOtherModels(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Get the seeded default
	original, _ := repo.GetDefault(ctx)
	if original == nil {
		t.Fatal("expected seeded default model config")
	}

	// Create two more models
	second := &models.LLMConfig{
		Name:     "Second",
		Provider: models.ProviderAnthropic,
		Model:    "claude-sonnet-4-5-20250929",
	}
	third := &models.LLMConfig{
		Name:     "Third",
		Provider: models.ProviderAnthropic,
		Model:    "claude-haiku-4-5-20251001",
		APIKey:   "sk-test",
	}
	repo.Create(ctx, second)
	repo.Create(ctx, third)

	// Transfer default to second, delete original
	if err := repo.TransferDefaultAndDelete(ctx, original.ID, second.ID); err != nil {
		t.Fatalf("TransferDefaultAndDelete: %v", err)
	}

	// Third should still exist and not be default
	gotThird, _ := repo.GetByID(ctx, third.ID)
	if gotThird == nil {
		t.Fatal("expected third model to still exist")
	}
	if gotThird.IsDefault {
		t.Error("expected third model to not be default")
	}

	// Total count should be 2
	count, _ := repo.Count(ctx)
	if count != 2 {
		t.Errorf("expected count=2, got %d", count)
	}
}

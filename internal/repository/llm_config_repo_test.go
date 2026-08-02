package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestLLMConfigRepo_ListCardsUsesBoundedProjection(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	largeBody := strings.Repeat("x", 1024*1024)
	aggregator := &models.LLMConfig{Name: "Aggregator", Provider: models.ProviderTest, Model: "agg"}
	if err := repo.Create(ctx, aggregator); err != nil {
		t.Fatal(err)
	}
	config := &models.LLMConfig{
		Name: "Large Custom", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodOAuth,
		Model: "custom-model", APIKey: "secret-key", OAuthAccessToken: "secret-token",
		OAuthRefreshToken: "secret-refresh", OAuthClientSecret: "secret-client",
		ExtraHeadersJSON: `{"secret":"header"}`, ExtraBodyJSON: largeBody,
		CustomAuthConfigJSON: `{"signing_secret":"secret"}`, CustomAuthStateJSON: `{"token":"secret"}`,
		MixtureConfigJSON: `{"large":"` + largeBody + `"}`,
	}
	if err := repo.Create(ctx, config); err != nil {
		t.Fatal(err)
	}
	mixture := &models.LLMConfig{
		Name: "Summary Mixture", Provider: models.ProviderMixture, Model: "mixture",
		MixtureConfigJSON: `{"aggregator":{"agent_config_id":"` + aggregator.ID + `","label":"Aggregator label"},"reference_models":[{"agent_config_id":"a"},{"agent_config_id":"b"}],"large":"` + largeBody + `"}`,
	}
	if err := repo.Create(ctx, mixture); err != nil {
		t.Fatal(err)
	}

	cards, err := repo.ListCards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]models.LLMConfig, len(cards))
	for _, card := range cards {
		byID[card.ID] = card
	}
	custom := byID[config.ID]
	if custom.APIKey != "present" || custom.OAuthAccessToken != "present" {
		t.Fatalf("credential-presence summaries not preserved: %#v", custom)
	}
	if custom.ExtraBodyJSON != "" || custom.ExtraHeadersJSON != "" || custom.OAuthRefreshToken != "" ||
		custom.OAuthClientSecret != "" || custom.CustomAuthConfigJSON != "" || custom.CustomAuthStateJSON != "" || custom.MixtureConfigJSON != "" {
		t.Fatalf("edit-only data materialized by card query: %#v", custom)
	}
	summary := byID[mixture.ID]
	if summary.MixtureAggregatorID != aggregator.ID || summary.MixtureAggregatorLabel != "Aggregator label" || summary.MixtureReferenceCount != 2 {
		t.Fatalf("mixture summary = %#v", summary)
	}
	for _, forbidden := range []string{"oauth_refresh_token", "oauth_client_secret", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json"} {
		if strings.Contains(llmConfigCardColumns, forbidden) {
			t.Fatalf("card projection contains edit-only column %q", forbidden)
		}
	}
}

func TestLLMConfigRepoCustomOAuthConnectionAndRefreshLease(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	cfg := &models.LLMConfig{
		Name: "Custom OAuth", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodOAuth, Model: "model",
	}
	if err := repo.Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	firstRevision, advanced, err := repo.AdvanceCustomOAuthRevision(ctx, cfg.ID)
	if err != nil || !advanced || firstRevision != 1 {
		t.Fatalf("first OAuth generation = %d, %v, %v", firstRevision, advanced, err)
	}
	secondRevision, advanced, err := repo.AdvanceCustomOAuthRevision(ctx, cfg.ID)
	if err != nil || !advanced || secondRevision != 2 {
		t.Fatalf("second OAuth generation = %d, %v, %v", secondRevision, advanced, err)
	}
	if err := repo.UpdateCustomOAuthConnection(ctx, cfg.ID, "access", "refresh", 1234, `{"instance_id":"instance"}`); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetByID(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.OAuthAccessToken != "access" || stored.OAuthRefreshToken != "refresh" ||
		stored.OAuthExpiresAt != 1234 || stored.CustomAuthStateJSON != `{"instance_id":"instance"}` {
		t.Fatalf("custom OAuth connection not persisted atomically: %#v", stored)
	}
	revision := stored.OAuthConfigRevision
	stored.Name = "Changed during OAuth"
	if err := repo.Update(ctx, stored); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.UpdateCustomOAuthConnectionIfRevision(
		ctx, stored.ID, revision, "stale-access", "stale-refresh", 5678, `{"instance_id":"stale"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Fatal("stale OAuth callback updated a newer model configuration")
	}
	updated, err = repo.UpdateCustomOAuthTokensIfRevision(
		ctx, stored.ID, stored.OAuthConfigRevision, "fresh-access", "fresh-refresh", 9012,
	)
	if err != nil || !updated {
		t.Fatalf("current-revision token update = %v, %v", updated, err)
	}

	now := time.Now()
	acquired, err := repo.TryAcquireOAuthRefreshLease(ctx, cfg.ID, "owner-1", now, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first lease acquire = %v, %v", acquired, err)
	}
	acquired, err = repo.TryAcquireOAuthRefreshLease(ctx, cfg.ID, "owner-2", now, time.Minute)
	if err != nil || acquired {
		t.Fatalf("competing lease acquire = %v, %v; want busy", acquired, err)
	}
	if err := repo.ReleaseOAuthRefreshLease(ctx, cfg.ID, "owner-1"); err != nil {
		t.Fatal(err)
	}
	acquired, err = repo.TryAcquireOAuthRefreshLease(ctx, cfg.ID, "owner-2", now, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("lease acquire after release = %v, %v", acquired, err)
	}
}

func TestLLMConfigRepo_MixtureConfigJSONPersists(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	mixtureJSON := `{"enabled":true,"reference_models":[{"agent_config_id":"ref"}],"aggregator":{"agent_config_id":"agg"}}`
	cfg := &models.LLMConfig{
		Name:              "Research Mixture",
		Provider:          models.ProviderMixture,
		Model:             "research-heavy",
		MixtureConfigJSON: mixtureJSON,
	}
	if err := repo.Create(ctx, cfg); err != nil {
		t.Fatalf("Create mixture: %v", err)
	}
	got, err := repo.GetByID(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("GetByID mixture: %v", err)
	}
	if got == nil || got.Provider != models.ProviderMixture || got.MixtureConfigJSON != mixtureJSON {
		t.Fatalf("mixture fields not persisted: %+v", got)
	}
	got.MixtureConfigJSON = `{"enabled":false,"aggregator":{"agent_config_id":"agg"}}`
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update mixture: %v", err)
	}
	updated, err := repo.GetByID(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("GetByID updated mixture: %v", err)
	}
	if updated.MixtureConfigJSON != got.MixtureConfigJSON {
		t.Fatalf("updated mixture_config_json = %q", updated.MixtureConfigJSON)
	}
}

func TestLLMConfigRepo_OpenAICompatibleFieldsDoNotBleedAcrossRows(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	openRouter := &models.LLMConfig{
		Name:                  "OpenRouter",
		Provider:              models.ProviderOpenAICompatible,
		AuthMethod:            models.AuthMethodAPIKey,
		Model:                 "nvidia/nemotron-3-ultra-550b-a55b:free",
		APIKey:                "sk-or",
		BaseURL:               "https://openrouter.ai/api/v1/",
		Transport:             "chat_completions",
		PresetSlug:            "openrouter",
		ModelsURL:             "https://openrouter.ai/api/v1/models",
		AuthHeaderName:        "Authorization",
		AuthHeaderValuePrefix: "Bearer ",
		DefaultMaxTokens:      1234,
	}
	custom := &models.LLMConfig{
		Name:       "Custom Gateway",
		Provider:   models.ProviderOpenAICompatible,
		AuthMethod: models.AuthMethodAPIKey,
		Model:      "custom-model",
		APIKey:     "sk-custom",
		BaseURL:    "http://127.0.0.1:8000/v1/",
		Transport:  "chat_completions",
		PresetSlug: "custom",
	}

	if err := repo.Create(ctx, openRouter); err != nil {
		t.Fatalf("Create openRouter: %v", err)
	}
	if err := repo.Create(ctx, custom); err != nil {
		t.Fatalf("Create custom: %v", err)
	}

	gotOpenRouter, err := repo.GetByID(ctx, openRouter.ID)
	if err != nil {
		t.Fatalf("GetByID openRouter: %v", err)
	}
	gotCustom, err := repo.GetByID(ctx, custom.ID)
	if err != nil {
		t.Fatalf("GetByID custom: %v", err)
	}

	if gotOpenRouter.BaseURL != openRouter.BaseURL || gotOpenRouter.PresetSlug != "openrouter" || gotOpenRouter.DefaultMaxTokens != 1234 {
		t.Fatalf("openrouter fields not persisted: %+v", gotOpenRouter)
	}
	if gotCustom.BaseURL != custom.BaseURL || gotCustom.PresetSlug != "custom" {
		t.Fatalf("custom fields not persisted: %+v", gotCustom)
	}
	if gotCustom.BaseURL == gotOpenRouter.BaseURL {
		t.Fatalf("custom row reused stale base URL %q", gotCustom.BaseURL)
	}
}

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

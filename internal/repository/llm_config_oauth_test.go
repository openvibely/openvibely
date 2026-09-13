package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestLLMConfigRepo_CreateWithOAuthFields(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	a := &models.LLMConfig{
		Name:       "OAuth Model",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-sonnet-4-5-20250929",
		MaxTokens:  4096,
		AuthMethod: models.AuthMethodOAuth,
	}

	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.AuthMethod != models.AuthMethodOAuth {
		t.Errorf("expected AuthMethod=oauth, got %q", got.AuthMethod)
	}
	if got.OAuthAccessToken != "" {
		t.Errorf("expected empty OAuthAccessToken, got %q", got.OAuthAccessToken)
	}
}

func TestLLMConfigRepo_CreateDefaultAuthMethod(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	a := &models.LLMConfig{
		Name:      "API Key Model",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		MaxTokens: 4096,
		// AuthMethod not set should default to API key.
	}

	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.AuthMethod != models.AuthMethodAPIKey {
		t.Errorf("expected AuthMethod=api_key, got %q", got.AuthMethod)
	}
}

func TestLLMConfigRepo_UpdateOAuthAccountIDPreservesCredentialsAndReauthState(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	cfg := &models.LLMConfig{Name: "OAuth", Provider: models.ProviderOpenAI, Model: "gpt", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "access", OAuthRefreshToken: "refresh", OAuthExpiresAt: 1900000000000}
	if err := repo.Create(ctx, cfg); err != nil {
		t.Fatalf("Create: %v", err)
	}
	marked, err := repo.MarkOAuthNeedsReauthIfRevision(ctx, cfg.ID, cfg.OAuthConfigRevision, cfg.Provider)
	if err != nil || !marked {
		t.Fatalf("MarkOAuthNeedsReauthIfRevision = %v, %v", marked, err)
	}
	updated, err := repo.UpdateOAuthAccountIDIfRevision(ctx, cfg.ID, cfg.OAuthConfigRevision, cfg.Provider, "workspace")
	if err != nil || !updated {
		t.Fatalf("UpdateOAuthAccountIDIfRevision = %v, %v", updated, err)
	}
	loaded, err := repo.GetByID(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if loaded.OAuthAccessToken != "access" || loaded.OAuthRefreshToken != "refresh" || loaded.OAuthExpiresAt != 1900000000000 {
		t.Fatalf("account identity update changed credentials: %#v", loaded)
	}
	if !loaded.OAuthNeedsReauth || loaded.OAuthAccountID != "workspace" {
		t.Fatalf("account identity/re-auth state = %q/%v", loaded.OAuthAccountID, loaded.OAuthNeedsReauth)
	}
	cards, err := repo.ListCards(ctx)
	if err != nil {
		t.Fatalf("ListCards: %v", err)
	}
	var card *models.LLMConfig
	for i := range cards {
		if cards[i].ID == cfg.ID {
			card = &cards[i]
			break
		}
	}
	if card == nil || !card.OAuthNeedsReauth || card.HasValidOAuthToken() {
		t.Fatalf("model card did not retain reconnect-required state: %#v", cards)
	}
}

func TestLLMConfigRepo_UpdateStandardOAuthConnectionAdvancesGenerationAtomically(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	cfg := &models.LLMConfig{
		Name:              "OAuth reconnect",
		Provider:          models.ProviderOpenAI,
		Model:             "gpt",
		AuthMethod:        models.AuthMethodOAuth,
		OAuthAccessToken:  "old-access",
		OAuthRefreshToken: "old-refresh",
		OAuthExpiresAt:    1,
		OAuthNeedsReauth:  true,
	}
	if err := repo.Create(ctx, cfg); err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := repo.UpdateStandardOAuthConnectionIfRevision(ctx, cfg.ID, cfg.OAuthConfigRevision, cfg.Provider, "new-access", "new-refresh", 1900000000000, "workspace")
	if err != nil || !updated {
		t.Fatalf("UpdateStandardOAuthConnectionIfRevision = %v, %v", updated, err)
	}
	staleUpdated, err := repo.UpdateStandardOAuthTokensIfRevision(ctx, cfg.ID, cfg.OAuthConfigRevision, cfg.Provider, "stale-access", "stale-refresh", 1900000000001)
	if err != nil {
		t.Fatalf("stale UpdateStandardOAuthTokensIfRevision: %v", err)
	}
	if staleUpdated {
		t.Fatal("prior credential generation updated after reconnect")
	}

	loaded, err := repo.GetByID(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if loaded.OAuthAccessToken != "new-access" || loaded.OAuthRefreshToken != "new-refresh" || loaded.OAuthExpiresAt != 1900000000000 {
		t.Fatalf("reconnected credentials = %#v", loaded)
	}
	if loaded.OAuthNeedsReauth || loaded.OAuthAccountID != "workspace" || loaded.OAuthConfigRevision != cfg.OAuthConfigRevision+1 {
		t.Fatalf("reconnected state = account %q, reauth %v, revision %d", loaded.OAuthAccountID, loaded.OAuthNeedsReauth, loaded.OAuthConfigRevision)
	}
}

func TestLLMConfigRepo_UpdateOAuthTokens(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	a := &models.LLMConfig{
		Name:       "OAuth Model",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-sonnet-4-5-20250929",
		MaxTokens:  4096,
		AuthMethod: models.AuthMethodOAuth,
	}

	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update OAuth tokens
	expiresAt := int64(1900000000000) // Far in the future
	if err := repo.UpdateOAuthTokens(ctx, a.ID, "access-token-123", "refresh-token-456", expiresAt); err != nil {
		t.Fatalf("UpdateOAuthTokens: %v", err)
	}

	got, err := repo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.OAuthAccessToken != "access-token-123" {
		t.Errorf("expected OAuthAccessToken=access-token-123, got %q", got.OAuthAccessToken)
	}
	if got.OAuthRefreshToken != "refresh-token-456" {
		t.Errorf("expected OAuthRefreshToken=refresh-token-456, got %q", got.OAuthRefreshToken)
	}
	if got.OAuthExpiresAt != expiresAt {
		t.Errorf("expected OAuthExpiresAt=%d, got %d", expiresAt, got.OAuthExpiresAt)
	}
}

func TestLLMConfigRepo_UpdateOAuthTokensMissingConfigFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	err := repo.UpdateOAuthTokens(ctx, "missing-model", "access-token", "refresh-token", 1900000000000)
	if err == nil {
		t.Fatal("expected UpdateOAuthTokens to fail when no config row is updated")
	}
}

func TestLLMConfigRepo_UpdateClearsOAuthTokens(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	a := &models.LLMConfig{
		Name:              "OAuth Model",
		Provider:          models.ProviderAnthropic,
		Model:             "claude-sonnet-4-5-20250929",
		MaxTokens:         4096,
		AuthMethod:        models.AuthMethodOAuth,
		OAuthAccessToken:  "old-token",
		OAuthRefreshToken: "old-refresh",
		OAuthExpiresAt:    1900000000000,
	}

	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Switch to API-key auth method (should clear tokens when handler processes this)
	a.AuthMethod = models.AuthMethodAPIKey
	a.OAuthAccessToken = ""
	a.OAuthRefreshToken = ""
	a.OAuthExpiresAt = 0
	if err := repo.Update(ctx, a); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := repo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.AuthMethod != models.AuthMethodAPIKey {
		t.Errorf("expected AuthMethod=api_key, got %q", got.AuthMethod)
	}
	if got.OAuthAccessToken != "" {
		t.Errorf("expected empty OAuthAccessToken after clearing, got %q", got.OAuthAccessToken)
	}
}

func TestLLMConfigRepo_SeededDefaultHasAPIKeyAuthMethod(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Test databases seed a default API-key model config.
	def, err := repo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}
	if def == nil {
		t.Fatal("expected seeded default model config, got nil")
	}
	if def.AuthMethod != models.AuthMethodAPIKey {
		t.Errorf("expected seeded default AuthMethod=api_key, got %q", def.AuthMethod)
	}
}

func TestLLMConfigRepo_ListIncludesOAuthFields(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create an OAuth config
	a := &models.LLMConfig{
		Name:              "OAuth Model",
		Provider:          models.ProviderAnthropic,
		Model:             "claude-sonnet-4-5-20250929",
		MaxTokens:         4096,
		AuthMethod:        models.AuthMethodOAuth,
		OAuthAccessToken:  "token-abc",
		OAuthRefreshToken: "refresh-xyz",
		OAuthExpiresAt:    1900000000000,
	}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}

	configs, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	var found bool
	for _, c := range configs {
		if c.ID == a.ID {
			found = true
			if c.AuthMethod != models.AuthMethodOAuth {
				t.Errorf("List: expected AuthMethod=oauth, got %q", c.AuthMethod)
			}
			if c.OAuthAccessToken != "token-abc" {
				t.Errorf("List: expected OAuthAccessToken=token-abc, got %q", c.OAuthAccessToken)
			}
		}
	}
	if !found {
		t.Error("OAuth config not found in List results")
	}
}

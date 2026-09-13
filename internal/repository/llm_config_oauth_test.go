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

func TestLLMConfigRepo_SharedOAuthConnectionUpdatesLinkedModelsOnly(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()

	first := &models.LLMConfig{Name: "OpenAI first", Provider: models.ProviderOpenAI, Model: "gpt-one", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "shared-old-access", OAuthRefreshToken: "shared-old-refresh", OAuthExpiresAt: 1}
	second := &models.LLMConfig{Name: "OpenAI second", Provider: models.ProviderOpenAI, Model: "gpt-two", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "private-access", OAuthRefreshToken: "private-refresh", OAuthExpiresAt: 2}
	other := &models.LLMConfig{Name: "OpenAI other account", Provider: models.ProviderOpenAI, Model: "gpt-three", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "other-access", OAuthRefreshToken: "other-refresh", OAuthExpiresAt: 3}
	anthropic := &models.LLMConfig{Name: "Anthropic account", Provider: models.ProviderAnthropic, Model: "claude", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "anthropic-access", OAuthRefreshToken: "anthropic-refresh", OAuthExpiresAt: 4}
	for _, cfg := range []*models.LLMConfig{first, second, other, anthropic} {
		if err := repo.Create(ctx, cfg); err != nil {
			t.Fatalf("Create(%s): %v", cfg.Name, err)
		}
	}
	var legacyOwners int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM agent_configs
		WHERE id IN (?, ?, ?, ?) AND (
			oauth_access_token != '' OR oauth_refresh_token != '' OR oauth_expires_at != 0 OR
			oauth_account_id != '' OR oauth_needs_reauth != 0
		)`, first.ID, second.ID, other.ID, anthropic.ID).Scan(&legacyOwners); err != nil {
		t.Fatalf("count legacy OAuth credential owners: %v", err)
	}
	if legacyOwners != 0 {
		t.Fatalf("standard OAuth credentials remained on %d model rows", legacyOwners)
	}
	if first.OAuthConnectionID == "" || first.OAuthConnectionID == second.OAuthConnectionID {
		t.Fatalf("new OAuth models did not start with private connections: %q/%q", first.OAuthConnectionID, second.OAuthConnectionID)
	}
	if err := repo.LinkOAuthConnection(ctx, second.ID, first.OAuthConnectionID); err != nil {
		t.Fatalf("LinkOAuthConnection: %v", err)
	}
	if err := repo.LinkOAuthConnection(ctx, anthropic.ID, first.OAuthConnectionID); err == nil {
		t.Fatal("cross-provider OAuth connection link succeeded")
	}

	updated, err := repo.UpdateStandardOAuthTokensIfRevision(ctx, second.ID, first.OAuthConfigRevision, models.ProviderOpenAI, "shared-new-access", "shared-new-refresh", 1900000000000, "workspace-a")
	if err != nil || !updated {
		t.Fatalf("UpdateStandardOAuthTokensIfRevision = %v, %v", updated, err)
	}
	for _, cfg := range []*models.LLMConfig{first, second} {
		loaded, loadErr := repo.GetByID(ctx, cfg.ID)
		if loadErr != nil {
			t.Fatalf("GetByID(%s): %v", cfg.Name, loadErr)
		}
		if loaded.OAuthConnectionID != first.OAuthConnectionID || loaded.OAuthAccessToken != "shared-new-access" || loaded.OAuthRefreshToken != "shared-new-refresh" || loaded.OAuthAccountID != "workspace-a" {
			t.Fatalf("linked model %s did not hydrate shared connection: %#v", cfg.Name, loaded)
		}
	}
	for _, cfg := range []*models.LLMConfig{other, anthropic} {
		loaded, loadErr := repo.GetByID(ctx, cfg.ID)
		if loadErr != nil {
			t.Fatalf("GetByID(%s): %v", cfg.Name, loadErr)
		}
		if loaded.OAuthAccessToken != cfg.OAuthAccessToken || loaded.OAuthRefreshToken != cfg.OAuthRefreshToken || loaded.OAuthConnectionID != cfg.OAuthConnectionID {
			t.Fatalf("independent account %s changed: %#v", cfg.Name, loaded)
		}
	}
}

func TestLLMConfigRepo_ListOAuthConnectionsReturnsSafeSummaries(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	cfg := &models.LLMConfig{
		Name:              "Safe OAuth summary",
		Provider:          models.ProviderAnthropic,
		Model:             "claude",
		AuthMethod:        models.AuthMethodOAuth,
		OAuthAccessToken:  "secret-access",
		OAuthRefreshToken: "secret-refresh",
		OAuthAccountID:    "secret-account-id",
	}
	if err := repo.Create(ctx, cfg); err != nil {
		t.Fatalf("Create: %v", err)
	}
	connections, err := repo.ListOAuthConnections(ctx, "")
	if err != nil {
		t.Fatalf("ListOAuthConnections: %v", err)
	}
	if len(connections) != 1 {
		t.Fatalf("connection summaries = %#v", connections)
	}
	summary := connections[0]
	if summary.AccessToken != "present" || summary.RefreshToken != "" || summary.AccountID != "" || summary.Revision != 0 {
		t.Fatalf("OAuth connection summary exposed private state: %#v", summary)
	}
}

func TestLLMConfigRepo_MoveModelsToOAuthConnectionIsAtomicAndProviderScoped(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	target := &models.LLMConfig{Name: "OpenAI target", Provider: models.ProviderOpenAI, Model: "gpt-target", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "target-access", OAuthRefreshToken: "target-refresh"}
	first := &models.LLMConfig{Name: "OpenAI first move", Provider: models.ProviderOpenAI, Model: "gpt-one", AuthMethod: models.AuthMethodOAuth}
	second := &models.LLMConfig{Name: "OpenAI second move", Provider: models.ProviderOpenAI, Model: "gpt-two", AuthMethod: models.AuthMethodOAuth}
	third := &models.LLMConfig{Name: "OpenAI atomic control", Provider: models.ProviderOpenAI, Model: "gpt-three", AuthMethod: models.AuthMethodOAuth}
	anthropic := &models.LLMConfig{Name: "Anthropic atomic control", Provider: models.ProviderAnthropic, Model: "claude", AuthMethod: models.AuthMethodOAuth}
	for _, cfg := range []*models.LLMConfig{target, first, second, third, anthropic} {
		if err := repo.Create(ctx, cfg); err != nil {
			t.Fatalf("Create(%s): %v", cfg.Name, err)
		}
	}

	if err := repo.MoveModelsToOAuthConnection(ctx, []string{third.ID, anthropic.ID}, target.OAuthConnectionID); err == nil {
		t.Fatal("mixed-provider bulk move succeeded")
	}
	for _, cfg := range []*models.LLMConfig{third, anthropic} {
		loaded, err := repo.GetByID(ctx, cfg.ID)
		if err != nil {
			t.Fatalf("GetByID(%s): %v", cfg.Name, err)
		}
		if loaded.OAuthConnectionID != cfg.OAuthConnectionID {
			t.Fatalf("failed bulk move partially changed %s: %#v", cfg.Name, loaded)
		}
	}

	if err := repo.MoveModelsToOAuthConnection(ctx, []string{first.ID, second.ID, first.ID}, target.OAuthConnectionID); err != nil {
		t.Fatalf("MoveModelsToOAuthConnection: %v", err)
	}
	for _, cfg := range []*models.LLMConfig{first, second} {
		loaded, err := repo.GetByID(ctx, cfg.ID)
		if err != nil {
			t.Fatalf("GetByID(%s): %v", cfg.Name, err)
		}
		if loaded.OAuthConnectionID != target.OAuthConnectionID || loaded.OAuthAccessToken != "target-access" || loaded.OAuthRefreshToken != "target-refresh" {
			t.Fatalf("moved model did not hydrate target account: %#v", loaded)
		}
	}
	if err := repo.RenameOAuthConnection(ctx, target.OAuthConnectionID, "Team Account"); err != nil {
		t.Fatalf("RenameOAuthConnection: %v", err)
	}
	connection, err := repo.GetOAuthConnectionByID(ctx, target.OAuthConnectionID)
	if err != nil {
		t.Fatalf("GetOAuthConnectionByID: %v", err)
	}
	if connection == nil || connection.Name != "Team Account" || connection.LinkedModels != 3 {
		t.Fatalf("renamed target connection = %#v", connection)
	}
}

func TestLLMConfigRepo_OAuthConnectionDeletionRequiresNoLinkedModels(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewLLMConfigRepo(db)
	ctx := context.Background()
	cfg := &models.LLMConfig{Name: "Deletable OAuth model", Provider: models.ProviderOpenAI, Model: "gpt-test", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "access", OAuthRefreshToken: "refresh"}
	if err := repo.Create(ctx, cfg); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.DeleteOAuthConnection(ctx, cfg.OAuthConnectionID); err == nil {
		t.Fatal("deleted OAuth connection while model was still linked")
	}
	if err := repo.Delete(ctx, cfg.ID); err != nil {
		t.Fatalf("Delete model: %v", err)
	}
	connection, err := repo.GetOAuthConnectionByID(ctx, cfg.OAuthConnectionID)
	if err != nil {
		t.Fatalf("GetOAuthConnectionByID: %v", err)
	}
	if connection == nil {
		t.Fatal("deleting model also deleted its OAuth connection")
	}
	if err := repo.DeleteOAuthConnection(ctx, cfg.OAuthConnectionID); err != nil {
		t.Fatalf("DeleteOAuthConnection after unlink: %v", err)
	}
	connection, err = repo.GetOAuthConnectionByID(ctx, cfg.OAuthConnectionID)
	if err != nil || connection != nil {
		t.Fatalf("deleted OAuth connection = %#v, %v", connection, err)
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

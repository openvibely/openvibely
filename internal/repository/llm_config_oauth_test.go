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
		Name:      "CLI Model",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-sonnet-4-5-20250929",
		MaxTokens: 4096,
		// AuthMethod not set — should default to "cli"
	}

	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.AuthMethod != models.AuthMethodCLI {
		t.Errorf("expected AuthMethod=cli, got %q", got.AuthMethod)
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

	// Switch to CLI auth method (should clear tokens when handler processes this)
	a.AuthMethod = models.AuthMethodCLI
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
	if got.AuthMethod != models.AuthMethodCLI {
		t.Errorf("expected AuthMethod=cli, got %q", got.AuthMethod)
	}
	if got.OAuthAccessToken != "" {
		t.Errorf("expected empty OAuthAccessToken after clearing, got %q", got.OAuthAccessToken)
	}
}

func TestLLMConfigRepo_SeededDefaultHasCLIAuthMethod(t *testing.T) {
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
	// Seeded config should have cli auth method (default)
	if def.AuthMethod != models.AuthMethodCLI {
		t.Errorf("expected seeded default AuthMethod=cli, got %q", def.AuthMethod)
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

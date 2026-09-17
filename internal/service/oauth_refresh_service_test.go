package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	llmoauth "github.com/openvibely/openvibely/internal/llm/oauth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestOAuthRefreshServiceRunOnceTimesOutAnthropicIdentityAndContinues(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	anthropic := &models.LLMConfig{
		Name: "Anthropic", Provider: models.ProviderAnthropic, Model: "claude",
		AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "anthropic-access", OAuthRefreshToken: "anthropic-refresh",
		OAuthExpiresAt: time.Now().Add(2 * time.Minute).UnixMilli(),
	}
	openAI := &models.LLMConfig{
		Name: "OpenAI", Provider: models.ProviderOpenAI, Model: "gpt",
		AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "openai-access", OAuthRefreshToken: "openai-refresh",
		OAuthExpiresAt: time.Now().Add(5 * time.Minute).UnixMilli(),
	}
	for _, cfg := range []*models.LLMConfig{anthropic, openAI} {
		if err := repo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}

	openAIRefreshed := false
	worker := NewOAuthRefreshService(repo, llmoauth.NewManager(repo))
	worker.identityTimeout = 20 * time.Millisecond
	worker.SetRefreshers(
		func(_ context.Context, cfg models.LLMConfig) (llmoauth.TokenSet, error) {
			return llmoauth.TokenSet{AccessToken: "anthropic-fresh", RefreshToken: cfg.OAuthRefreshToken, ExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}, nil
		},
		func(_ context.Context, cfg models.LLMConfig) (llmoauth.TokenSet, error) {
			openAIRefreshed = true
			return llmoauth.TokenSet{AccessToken: "openai-fresh", RefreshToken: cfg.OAuthRefreshToken, ExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}, nil
		},
	)
	worker.SetAnthropicIdentityResolver(func(ctx context.Context, _ string) (AnthropicOAuthIdentity, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("identity lookup context has no deadline")
		}
		<-ctx.Done()
		return AnthropicOAuthIdentity{}, ctx.Err()
	})

	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !openAIRefreshed {
		t.Fatal("later OpenAI connection was not refreshed after Anthropic identity timeout")
	}
	loaded, err := repo.GetByID(ctx, openAI.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.OAuthAccessToken != "openai-fresh" {
		t.Fatalf("OpenAI access token = %q, want refreshed token", loaded.OAuthAccessToken)
	}
}

func TestOAuthRefreshServiceRunOnceAdoptsVerifiedAnthropicPrincipal(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	configs := []*models.LLMConfig{
		{Name: "First", Provider: models.ProviderAnthropic, Model: "claude-one", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "access-a", OAuthRefreshToken: "refresh-a", OAuthExpiresAt: time.Now().Add(3 * time.Hour).UnixMilli()},
		{Name: "Second", Provider: models.ProviderAnthropic, Model: "claude-two", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "access-b", OAuthRefreshToken: "refresh-b", OAuthExpiresAt: time.Now().Add(3 * time.Hour).UnixMilli()},
		{Name: "Organization only", Provider: models.ProviderAnthropic, Model: "claude-three", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "access-c", OAuthRefreshToken: "refresh-c", OAuthExpiresAt: time.Now().Add(3 * time.Hour).UnixMilli()},
	}
	for _, cfg := range configs {
		if err := repo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}

	worker := NewOAuthRefreshService(repo, llmoauth.NewManager(repo))
	worker.SetAnthropicIdentityResolver(func(_ context.Context, token string) (AnthropicOAuthIdentity, error) {
		if token == "access-c" {
			return AnthropicOAuthIdentity{AccountID: "organization:shared", DisplayName: "Organization account"}, nil
		}
		return AnthropicOAuthIdentity{AccountID: "organization:shared", DisplayName: "Verified account", PrincipalHash: "same-user-hash"}, nil
	})
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	first, err := repo.GetByID(ctx, configs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.GetByID(ctx, configs[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	third, err := repo.GetByID(ctx, configs[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.OAuthConnectionID != second.OAuthConnectionID {
		t.Fatalf("verified same-user connections remain split: %q != %q", first.OAuthConnectionID, second.OAuthConnectionID)
	}
	if third.OAuthConnectionID == first.OAuthConnectionID {
		t.Fatal("organization-only connection was adopted without strong user evidence")
	}
	connections, err := repo.ListOAuthConnections(ctx, models.ProviderAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != 2 {
		t.Fatalf("connections = %d, want 2", len(connections))
	}
}

func TestOAuthRefreshServiceRunOnceAdoptsVerifiedOpenAIPrincipal(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	configs := []*models.LLMConfig{
		{Name: "First", Provider: models.ProviderOpenAI, Model: "gpt-one", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "access-a", OAuthRefreshToken: "refresh-a", OAuthExpiresAt: time.Now().Add(3 * time.Hour).UnixMilli()},
		{Name: "Second", Provider: models.ProviderOpenAI, Model: "gpt-two", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "access-b", OAuthRefreshToken: "refresh-b", OAuthExpiresAt: time.Now().Add(3 * time.Hour).UnixMilli()},
		{Name: "Other account", Provider: models.ProviderOpenAI, Model: "gpt-three", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "access-c", OAuthRefreshToken: "refresh-c", OAuthExpiresAt: time.Now().Add(3 * time.Hour).UnixMilli()},
	}
	for _, cfg := range configs {
		if err := repo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}

	worker := NewOAuthRefreshService(repo, llmoauth.NewManager(repo))
	worker.SetOpenAIIdentityResolver(func(_ context.Context, token string) (OpenAIOAuthIdentity, error) {
		if token == "access-c" {
			return OpenAIOAuthIdentity{AccountID: "account-other", DisplayName: "other@example.com", PrincipalHash: "other-user-account"}, nil
		}
		return OpenAIOAuthIdentity{AccountID: "account-shared", DisplayName: "owner@example.com", PrincipalHash: "same-user-account"}, nil
	})
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	first, err := repo.GetByID(ctx, configs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.GetByID(ctx, configs[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	third, err := repo.GetByID(ctx, configs[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.OAuthConnectionID != second.OAuthConnectionID {
		t.Fatalf("verified same OpenAI user/account connections remain split: %q != %q", first.OAuthConnectionID, second.OAuthConnectionID)
	}
	if third.OAuthConnectionID == first.OAuthConnectionID {
		t.Fatal("different OpenAI account was adopted")
	}
	connections, err := repo.ListOAuthConnections(ctx, models.ProviderOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != 2 {
		t.Fatalf("connections = %d, want 2", len(connections))
	}
}

func TestOAuthRefreshServiceRunOnceLinksExpiredOpenAIJWTWithMatchingIdentity(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	sharedToken := openAITestJWT(`{"sub":"user-shared","chatgpt_account_id":"account-shared","email":"owner@example.com"}`)
	differentToken := openAITestJWT(`{"sub":"user-other","chatgpt_account_id":"account-shared","email":"other@example.com"}`)
	healthy := &models.LLMConfig{Name: "Codex 5.5", Provider: models.ProviderOpenAI, Model: "gpt-5.5-codex", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: sharedToken, OAuthRefreshToken: "healthy-refresh", OAuthExpiresAt: time.Now().Add(3 * time.Hour).UnixMilli()}
	matchingExpired := &models.LLMConfig{Name: "Codex older", Provider: models.ProviderOpenAI, Model: "codex-old", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: sharedToken, OAuthRefreshToken: "expired-refresh", OAuthNeedsReauth: true}
	differentExpired := &models.LLMConfig{Name: "Codex other user", Provider: models.ProviderOpenAI, Model: "codex-other", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: differentToken, OAuthRefreshToken: "other-expired-refresh", OAuthNeedsReauth: true}
	for _, cfg := range []*models.LLMConfig{healthy, matchingExpired, differentExpired} {
		if err := repo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}

	worker := NewOAuthRefreshService(repo, llmoauth.NewManager(repo))
	worker.SetOpenAIIdentityResolver(func(_ context.Context, token string) (OpenAIOAuthIdentity, error) {
		if token == differentToken {
			return OpenAIOAuthIdentity{AccountID: "account-shared", PrincipalHash: "different-user"}, nil
		}
		return OpenAIOAuthIdentity{AccountID: "account-shared", PrincipalHash: "shared-user"}, nil
	})
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	matched, err := repo.GetByID(ctx, matchingExpired.ID)
	if err != nil {
		t.Fatal(err)
	}
	if matched.OAuthConnectionID != healthy.OAuthConnectionID || matched.OAuthNeedsReauth || matched.OAuthAccessToken != sharedToken {
		t.Fatalf("matching expired Codex model was not linked: %#v", matched)
	}
	different, err := repo.GetByID(ctx, differentExpired.ID)
	if err != nil {
		t.Fatal(err)
	}
	if different.OAuthConnectionID != differentExpired.OAuthConnectionID || !different.OAuthNeedsReauth {
		t.Fatalf("different Codex identity was linked: %#v", different)
	}
}

func TestOAuthRefreshServiceRunOnceUsesPersistedVerifiedOpenAIPrincipalAfterKeyRetirement(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	healthy := &models.LLMConfig{Name: "Current Codex", Provider: models.ProviderOpenAI, Model: "codex-current", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "current-token", OAuthRefreshToken: "current-refresh", OAuthExpiresAt: time.Now().Add(3 * time.Hour).UnixMilli()}
	stale := &models.LLMConfig{Name: "Old Codex", Provider: models.ProviderOpenAI, Model: "codex-old", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "retired-key-token", OAuthRefreshToken: "old-refresh", OAuthNeedsReauth: true}
	for _, cfg := range []*models.LLMConfig{healthy, stale} {
		if err := repo.Create(ctx, cfg); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE oauth_connections SET oauth_principal_hash = 'persisted-verified-principal', oauth_principal_verified = 1 WHERE id IN (?, ?)`, healthy.OAuthConnectionID, stale.OAuthConnectionID); err != nil {
		t.Fatal(err)
	}

	worker := NewOAuthRefreshService(repo, llmoauth.NewManager(repo))
	worker.SetOpenAIIdentityResolver(func(_ context.Context, _ string) (OpenAIOAuthIdentity, error) {
		return OpenAIOAuthIdentity{}, errors.New("signing key retired")
	})
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.GetByID(ctx, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.OAuthConnectionID != healthy.OAuthConnectionID || loaded.OAuthNeedsReauth {
		t.Fatalf("persisted verified principal was not adopted: %#v", loaded)
	}
}

func TestOAuthRefreshServiceRunOnceRefreshesEachExpiringConfigIndependently(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	expiresSoon := time.Now().Add(5 * time.Minute).UnixMilli()
	configs := []*models.LLMConfig{
		{Name: "OpenAI", Provider: models.ProviderOpenAI, Model: "gpt", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "openai-old", OAuthRefreshToken: "openai-refresh", OAuthExpiresAt: expiresSoon},
		{Name: "Anthropic", Provider: models.ProviderAnthropic, Model: "claude", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "anthropic-old", OAuthRefreshToken: "anthropic-refresh", OAuthExpiresAt: expiresSoon},
		{Name: "Fresh", Provider: models.ProviderOpenAI, Model: "fresh", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "fresh-access", OAuthRefreshToken: "fresh-refresh", OAuthExpiresAt: time.Now().Add(3 * time.Hour).UnixMilli()},
		{Name: "API key", Provider: models.ProviderOpenAI, Model: "api", AuthMethod: models.AuthMethodAPIKey, APIKey: "secret"},
	}
	for _, cfg := range configs {
		if err := repo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}

	var mu sync.Mutex
	calls := map[string]int{}
	refresh := func(ctx context.Context, cfg models.LLMConfig) (llmoauth.TokenSet, error) {
		mu.Lock()
		calls[cfg.ID]++
		mu.Unlock()
		return llmoauth.TokenSet{
			AccessToken:  cfg.ID + "-access",
			RefreshToken: cfg.ID + "-refresh",
			ExpiresAt:    time.Now().Add(2 * time.Hour).UnixMilli(),
		}, nil
	}
	worker := NewOAuthRefreshService(repo, llmoauth.NewManager(repo))
	worker.SetRefreshers(refresh, refresh)
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	for _, cfg := range configs[:2] {
		loaded, err := repo.GetByID(ctx, cfg.ID)
		if err != nil {
			t.Fatalf("load %s: %v", cfg.Name, err)
		}
		if loaded.OAuthAccessToken != cfg.ID+"-access" || loaded.OAuthRefreshToken != cfg.ID+"-refresh" {
			t.Fatalf("%s was not independently refreshed: %#v", cfg.Name, loaded)
		}
		if calls[cfg.ID] != 1 {
			t.Fatalf("%s refresh calls = %d, want 1", cfg.Name, calls[cfg.ID])
		}
	}
	if len(calls) != 2 {
		t.Fatalf("unexpected configs refreshed: %#v", calls)
	}
}

func TestOAuthRefreshServiceRunOnceRefreshesSharedConnectionOnce(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic} {
		t.Run(string(provider), func(t *testing.T) {
			db := testutil.NewTestDB(t)
			repo := repository.NewLLMConfigRepo(db)
			ctx := context.Background()
			expiresSoon := time.Now().Add(5 * time.Minute).UnixMilli()
			first := &models.LLMConfig{Name: "Shared first", Provider: provider, Model: "model-one", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "shared-old", OAuthRefreshToken: "shared-refresh", OAuthExpiresAt: expiresSoon}
			second := &models.LLMConfig{Name: "Shared second", Provider: provider, Model: "model-two", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "private-old", OAuthRefreshToken: "private-refresh", OAuthExpiresAt: expiresSoon}
			other := &models.LLMConfig{Name: "Other", Provider: provider, Model: "model-three", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "other-old", OAuthRefreshToken: "other-refresh", OAuthExpiresAt: expiresSoon}
			for _, cfg := range []*models.LLMConfig{first, second, other} {
				if err := repo.Create(ctx, cfg); err != nil {
					t.Fatalf("create %s: %v", cfg.Name, err)
				}
			}
			if err := repo.LinkOAuthConnection(ctx, second.ID, first.OAuthConnectionID); err != nil {
				t.Fatalf("link shared connection: %v", err)
			}

			calls := map[string]int{}
			refresh := func(_ context.Context, cfg models.LLMConfig) (llmoauth.TokenSet, error) {
				calls[cfg.OAuthConnectionID]++
				return llmoauth.TokenSet{AccessToken: cfg.OAuthConnectionID + "-access", RefreshToken: cfg.OAuthConnectionID + "-refresh", ExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}, nil
			}
			worker := NewOAuthRefreshService(repo, llmoauth.NewManager(repo))
			if provider == models.ProviderAnthropic {
				worker.SetRefreshers(refresh, nil)
			} else {
				worker.SetRefreshers(nil, refresh)
			}
			if err := worker.RunOnce(ctx); err != nil {
				t.Fatalf("RunOnce: %v", err)
			}
			if calls[first.OAuthConnectionID] != 1 || calls[other.OAuthConnectionID] != 1 || len(calls) != 2 {
				t.Fatalf("refresh calls by connection = %#v, want one per account", calls)
			}
			for _, cfg := range []*models.LLMConfig{first, second} {
				loaded, err := repo.GetByID(ctx, cfg.ID)
				if err != nil {
					t.Fatalf("load %s: %v", cfg.Name, err)
				}
				if loaded.OAuthAccessToken != first.OAuthConnectionID+"-access" || loaded.OAuthRefreshToken != first.OAuthConnectionID+"-refresh" {
					t.Fatalf("linked model %s did not receive shared refresh: %#v", cfg.Name, loaded)
				}
			}
		})
	}
}

func TestOAuthRefreshServicePersistsPermanentReauthAndSkipsUntilCleared(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	cfg := &models.LLMConfig{Name: "Anthropic", Provider: models.ProviderAnthropic, Model: "claude", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "old", OAuthRefreshToken: "refresh", OAuthExpiresAt: time.Now().Add(-time.Minute).UnixMilli()}
	if err := repo.Create(ctx, cfg); err != nil {
		t.Fatalf("create config: %v", err)
	}

	calls := 0
	worker := NewOAuthRefreshService(repo, llmoauth.NewManager(repo))
	worker.SetRefreshers(func(context.Context, models.LLMConfig) (llmoauth.TokenSet, error) {
		calls++
		return llmoauth.TokenSet{}, llmoauth.ErrReauthenticationRequired
	}, nil)
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce permanent failure: %v", err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce skipped permanent failure: %v", err)
	}
	if calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls)
	}
	loaded, err := repo.GetByID(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !loaded.OAuthNeedsReauth {
		t.Fatal("permanent refresh failure did not persist reauthentication state")
	}

	updated, err := repo.UpdateStandardOAuthTokensIfRevision(ctx, loaded.ID, loaded.OAuthConfigRevision, loaded.Provider, "new-access", "new-refresh", time.Now().Add(2*time.Hour).UnixMilli())
	if err != nil || !updated {
		t.Fatalf("successful reconnect update = %v, %v", updated, err)
	}
	loaded, err = repo.GetByID(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if loaded.OAuthNeedsReauth {
		t.Fatal("successful token update did not clear reauthentication state")
	}
}

func TestOAuthRefreshServiceStopWaitsForCanceledRefresh(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	cfg := &models.LLMConfig{Name: "OpenAI", Provider: models.ProviderOpenAI, Model: "gpt", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "old", OAuthRefreshToken: "refresh", OAuthExpiresAt: time.Now().Add(-time.Minute).UnixMilli()}
	if err := repo.Create(context.Background(), cfg); err != nil {
		t.Fatalf("create config: %v", err)
	}
	started := make(chan struct{})
	stopped := make(chan struct{})
	worker := NewOAuthRefreshService(repo, llmoauth.NewManager(repo))
	worker.SetRefreshers(nil, func(ctx context.Context, cfg models.LLMConfig) (llmoauth.TokenSet, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return llmoauth.TokenSet{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)
	<-started
	cancel()
	worker.Stop()
	select {
	case <-stopped:
	default:
		t.Fatal("Stop returned before the active refresh exited")
	}
}

func TestOAuthRefreshServiceReturnsOnlyOperationalErrors(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	cfg := &models.LLMConfig{Name: "OpenAI", Provider: models.ProviderOpenAI, Model: "gpt", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "old", OAuthRefreshToken: "refresh", OAuthExpiresAt: time.Now().Add(-time.Minute).UnixMilli()}
	if err := repo.Create(ctx, cfg); err != nil {
		t.Fatalf("create config: %v", err)
	}
	worker := NewOAuthRefreshService(repo, llmoauth.NewManager(repo))
	worker.SetRefreshers(nil, func(context.Context, models.LLMConfig) (llmoauth.TokenSet, error) {
		return llmoauth.TokenSet{}, errors.New("temporary network failure")
	})
	if err := worker.RunOnce(ctx); err == nil {
		t.Fatal("expected transient operational error")
	}
}

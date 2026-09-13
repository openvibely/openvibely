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

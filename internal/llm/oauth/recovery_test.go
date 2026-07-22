package oauth

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestManagerRefreshErrorsDoNotExposeConfigID(t *testing.T) {
	cfg := models.LLMConfig{
		ID:               "private-config-id",
		Name:             "OAuth Config",
		Provider:         models.ProviderOpenAI,
		Model:            "gpt-5.3-codex",
		AuthMethod:       models.AuthMethodOAuth,
		OAuthAccessToken: "access-token",
	}
	_, err := NewManager(nil).EnsureFresh(context.Background(), cfg, time.Hour, func(context.Context, models.LLMConfig) (TokenSet, error) {
		return TokenSet{}, nil
	})
	if err == nil {
		t.Fatal("expected unavailable recovery error")
	}
	if strings.Contains(err.Error(), cfg.ID) {
		t.Fatalf("OAuth recovery error exposed config id: %v", err)
	}
}

func createOAuthConfig(t *testing.T, repo *repository.LLMConfigRepo, cfg models.LLMConfig) models.LLMConfig {
	t.Helper()
	if cfg.ID == "" {
		cfg.ID = "cfg-1"
	}
	if cfg.Name == "" {
		cfg.Name = "OAuth Config"
	}
	if cfg.Provider == "" {
		cfg.Provider = models.ProviderOpenAI
	}
	if cfg.Model == "" {
		cfg.Model = "model"
	}
	cfg.AuthMethod = models.AuthMethodOAuth
	if cfg.OAuthAccessToken == "" {
		cfg.OAuthAccessToken = "old-access"
	}
	if cfg.OAuthRefreshToken == "" {
		cfg.OAuthRefreshToken = "old-refresh"
	}
	if cfg.OAuthExpiresAt == 0 {
		cfg.OAuthExpiresAt = time.Now().Add(-time.Minute).UnixMilli()
	}
	if err := repo.Create(context.Background(), &cfg); err != nil {
		t.Fatalf("Create config: %v", err)
	}
	return cfg
}

func TestManagerEnsureFreshSingleflightsConcurrentRefresh(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	cfg := createOAuthConfig(t, repo, models.LLMConfig{ID: "cfg-singleflight", Provider: models.ProviderOpenAI})
	mgr := NewManager(repo)

	const workers = 8
	ready := make(chan struct{}, workers)
	release := make(chan struct{})
	start := make(chan struct{})
	finish := make(chan struct{})
	var startOnce sync.Once
	calls := 0
	var mu sync.Mutex
	refresh := func(ctx context.Context, cfg models.LLMConfig) (TokenSet, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		startOnce.Do(func() { close(start) })
		<-finish
		return TokenSet{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}, nil
	}

	var wg sync.WaitGroup
	results := make([]models.LLMConfig, workers)
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ready <- struct{}{}
			<-release
			results[i], errs[i] = mgr.EnsureFresh(context.Background(), cfg, time.Hour, refresh)
		}(i)
	}
	for range workers {
		<-ready
	}
	close(release)
	<-start
	time.Sleep(10 * time.Millisecond)
	close(finish)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("EnsureFresh[%d]: %v", i, err)
		}
		if results[i].OAuthAccessToken != "new-access" {
			t.Fatalf("EnsureFresh[%d] access token = %q", i, results[i].OAuthAccessToken)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls)
	}
}

func TestManagerRecoverUnauthorizedSkipsRefreshWhenConfigAlreadyChanged(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	cfg := createOAuthConfig(t, repo, models.LLMConfig{ID: "cfg-race", Provider: models.ProviderAnthropic})
	if err := repo.UpdateOAuthTokens(context.Background(), cfg.ID, "already-new", "already-refresh", time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatalf("UpdateOAuthTokens: %v", err)
	}
	mgr := NewManager(repo)

	calls := 0
	fresh, recovered, err := mgr.RecoverUnauthorized(context.Background(), cfg, "old-access", func(ctx context.Context, cfg models.LLMConfig) (TokenSet, error) {
		calls++
		return TokenSet{}, nil
	})
	if err != nil {
		t.Fatalf("RecoverUnauthorized: %v", err)
	}
	if !recovered {
		t.Fatal("expected recovered=true")
	}
	if fresh.OAuthAccessToken != "already-new" {
		t.Fatalf("access token = %q", fresh.OAuthAccessToken)
	}
	if calls != 0 {
		t.Fatalf("refresh calls = %d, want 0", calls)
	}
}

func TestManagerEnsureFreshDoesNotSkipChangedButStillExpiringToken(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	cfg := createOAuthConfig(t, repo, models.LLMConfig{ID: "cfg-still-expiring", Provider: models.ProviderOpenAI})
	if err := repo.UpdateOAuthTokens(context.Background(), cfg.ID, "changed-but-expiring", "changed-refresh", time.Now().Add(time.Minute).UnixMilli()); err != nil {
		t.Fatalf("UpdateOAuthTokens: %v", err)
	}
	mgr := NewManager(repo)

	calls := 0
	fresh, err := mgr.EnsureFresh(context.Background(), cfg, time.Hour, func(ctx context.Context, cfg models.LLMConfig) (TokenSet, error) {
		calls++
		if cfg.OAuthAccessToken != "changed-but-expiring" {
			t.Fatalf("refresh saw access token %q", cfg.OAuthAccessToken)
		}
		return TokenSet{AccessToken: "fresh-access", RefreshToken: "fresh-refresh", ExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}, nil
	})
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls)
	}
	if fresh.OAuthAccessToken != "fresh-access" {
		t.Fatalf("access token = %q", fresh.OAuthAccessToken)
	}
}

func TestManagerRefreshPersistsSelectedConfigOnly(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	selected := createOAuthConfig(t, repo, models.LLMConfig{ID: "selected", Name: "Selected", Provider: models.ProviderAnthropic})
	other := createOAuthConfig(t, repo, models.LLMConfig{ID: "other", Name: "Other", Provider: models.ProviderAnthropic, OAuthAccessToken: "other-access", OAuthRefreshToken: "other-refresh"})
	mgr := NewManager(repo)

	fresh, err := mgr.EnsureFresh(context.Background(), selected, time.Hour, func(ctx context.Context, cfg models.LLMConfig) (TokenSet, error) {
		if cfg.ID != selected.ID {
			t.Fatalf("refresh cfg id = %q", cfg.ID)
		}
		return TokenSet{AccessToken: "selected-new", RefreshToken: "selected-refresh-new", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}, nil
	})
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if fresh.ID != selected.ID || fresh.OAuthAccessToken != "selected-new" {
		t.Fatalf("fresh config = %#v", fresh)
	}

	loadedOther, err := repo.GetByID(context.Background(), other.ID)
	if err != nil {
		t.Fatalf("GetByID other: %v", err)
	}
	if loadedOther.OAuthAccessToken != "other-access" || loadedOther.OAuthRefreshToken != "other-refresh" {
		t.Fatalf("other config was modified: %#v", loadedOther)
	}
}

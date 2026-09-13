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

func TestManagerEnsureFreshSingleflightPreservesEachLinkedModel(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	first := createOAuthConfig(t, repo, models.LLMConfig{ID: "shared-first", Name: "Shared First", Model: "model-first", Provider: models.ProviderOpenAI})
	second := createOAuthConfig(t, repo, models.LLMConfig{ID: "shared-second", Name: "Shared Second", Model: "model-second", Provider: models.ProviderOpenAI})
	if err := repo.LinkOAuthConnection(context.Background(), second.ID, first.OAuthConnectionID); err != nil {
		t.Fatalf("LinkOAuthConnection: %v", err)
	}
	second.OAuthConnectionID = first.OAuthConnectionID
	mgr := NewManager(repo)

	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	var mu sync.Mutex
	refresh := func(context.Context, models.LLMConfig) (TokenSet, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		close(started)
		<-release
		return TokenSet{AccessToken: "shared-access", RefreshToken: "shared-refresh", ExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}, nil
	}

	type result struct {
		cfg models.LLMConfig
		err error
	}
	firstDone := make(chan result, 1)
	secondDone := make(chan result, 1)
	go func() {
		cfg, err := mgr.EnsureFresh(context.Background(), first, time.Hour, refresh)
		firstDone <- result{cfg: cfg, err: err}
	}()
	<-started
	go func() {
		cfg, err := mgr.EnsureFresh(context.Background(), second, time.Hour, refresh)
		secondDone <- result{cfg: cfg, err: err}
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)

	firstResult := <-firstDone
	secondResult := <-secondDone
	if firstResult.err != nil || secondResult.err != nil {
		t.Fatalf("EnsureFresh errors: first=%v second=%v", firstResult.err, secondResult.err)
	}
	if firstResult.cfg.ID != first.ID || firstResult.cfg.Model != first.Model {
		t.Fatalf("first result used another linked model: %#v", firstResult.cfg)
	}
	if secondResult.cfg.ID != second.ID || secondResult.cfg.Model != second.Model {
		t.Fatalf("second result used another linked model: %#v", secondResult.cfg)
	}
	if firstResult.cfg.OAuthAccessToken != "shared-access" || secondResult.cfg.OAuthAccessToken != "shared-access" {
		t.Fatalf("linked models did not receive shared credentials: first=%q second=%q", firstResult.cfg.OAuthAccessToken, secondResult.cfg.OAuthAccessToken)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls)
	}
}

func TestManagerEnsureFreshUsesDurableLeaseAcrossManagers(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	cfg := createOAuthConfig(t, repo, models.LLMConfig{ID: "cfg-durable-lease", Provider: models.ProviderOpenAI})
	firstManager := NewManager(repo)
	secondManager := NewManager(repo)

	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	var mu sync.Mutex
	refresh := func(ctx context.Context, cfg models.LLMConfig) (TokenSet, error) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			close(started)
			<-release
		}
		return TokenSet{AccessToken: "leased-access", RefreshToken: "leased-refresh", ExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}, nil
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := firstManager.EnsureFresh(context.Background(), cfg, time.Hour, refresh)
		firstDone <- err
	}()
	<-started
	secondDone := make(chan error, 1)
	go func() {
		_, err := secondManager.EnsureFresh(context.Background(), cfg, time.Hour, refresh)
		secondDone <- err
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second refresh: %v", err)
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

func TestManagerRefreshRejectsConnectionReassignment(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	cfg := createOAuthConfig(t, repo, models.LLMConfig{ID: "cfg-stale", Provider: models.ProviderOpenAI})
	other := createOAuthConfig(t, repo, models.LLMConfig{ID: "cfg-other", Name: "Other account", Provider: models.ProviderOpenAI, OAuthAccessToken: "other-access", OAuthRefreshToken: "other-refresh"})
	mgr := NewManager(repo)

	_, err := mgr.EnsureFresh(context.Background(), cfg, time.Hour, func(ctx context.Context, refreshing models.LLMConfig) (TokenSet, error) {
		if updateErr := repo.LinkOAuthConnection(ctx, refreshing.ID, other.OAuthConnectionID); updateErr != nil {
			return TokenSet{}, updateErr
		}
		return TokenSet{AccessToken: "stale-access", RefreshToken: "stale-refresh", ExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "changed while OAuth refresh was in progress") {
		t.Fatalf("EnsureFresh error = %v, want stale connection rejection", err)
	}
	loaded, loadErr := repo.GetByID(context.Background(), cfg.ID)
	if loadErr != nil {
		t.Fatalf("GetByID: %v", loadErr)
	}
	if loaded.OAuthConnectionID != other.OAuthConnectionID || loaded.OAuthAccessToken != "other-access" || loaded.OAuthRefreshToken != "other-refresh" {
		t.Fatalf("stale refresh changed reassigned model: %#v", loaded)
	}
	oldConnection, loadErr := repo.GetOAuthConnectionByID(context.Background(), cfg.OAuthConnectionID)
	if loadErr != nil {
		t.Fatalf("GetOAuthConnectionByID: %v", loadErr)
	}
	if oldConnection.AccessToken == "stale-access" || oldConnection.RefreshToken == "stale-refresh" {
		t.Fatalf("stale refresh overwrote previous connection: %#v", oldConnection)
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

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

// Any usage-store access panics. Provider cards must not read or persist
// usage records, even when a prior successful response exists.
type liveUsageWithoutStorage struct{ persistence.UsageStore }

func TestLiveAccountUsageAlwaysFetchesAndNeverFallsBack(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()
	cfg := models.LLMConfig{Name: "live", Provider: models.ProviderOpenAI, Model: "test", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "fixture", OAuthAccountID: "account", OAuthExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	if err := env.LLMConfigs.Create(ctx, &cfg); err != nil {
		t.Fatal(err)
	}
	svc := NewUsageAnalyticsService(liveUsageWithoutStorage{}, env.LLMConfigs)
	calls := 0
	svc.SetAccountUsageFetcher(func(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageResult, error) {
		calls++
		if calls == 3 {
			return nil, errors.New("private provider failure")
		}
		used := float64(calls * 10)
		return &models.AccountUsageResult{PrimaryLabel: "Session", PrimaryUsedPercent: &used}, nil
	})
	accounts, err := svc.ListUsageAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	filter := persistence.UsageFilter{AccountGroupKey: accounts[0].Key}
	for i := 1; i <= 3; i++ {
		view, err := svc.BuildLiveAnalyticsAccountLimits(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		if calls != i {
			t.Fatalf("provider calls=%d, want %d", calls, i)
		}
		if len(view.AccountLimits) != 1 {
			t.Fatalf("cards: %+v", view.AccountLimits)
		}
		card := view.AccountLimits[0]
		if i < 3 {
			if card.Error != "" || len(card.Limits) != 1 || (card.Limits[0].UsedPercent == nil || *card.Limits[0].UsedPercent != float64(i*10)) {
				t.Fatalf("fresh card: %+v", card)
			}
		} else if card.Error != "Usage unavailable" || len(card.Limits) != 0 || !card.UpdatedAt.IsZero() || view.LastUpdatedAt != nil {
			t.Fatalf("failure must contain no old usage or timestamp: %+v", view)
		}
	}
}

func TestLiveAccountUsageCancellationAndNavigationRead(t *testing.T) {
	env := teststores.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	task := models.Task{ProjectID: "default", Title: "Navigation", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := env.Tasks.Create(ctx, &task); err != nil {
		t.Fatal(err)
	}
	cfg := models.LLMConfig{Name: "live", Provider: models.ProviderOpenAI, Model: "test", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "fixture", OAuthAccountID: "account", OAuthExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	if err := env.LLMConfigs.Create(ctx, &cfg); err != nil {
		t.Fatal(err)
	}
	svc := NewUsageAnalyticsService(liveUsageWithoutStorage{}, env.LLMConfigs)
	started := make(chan struct{})
	stopped := make(chan struct{})
	svc.SetAccountUsageFetcher(func(ctx context.Context, _ models.LLMConfig) (*models.AccountUsageResult, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	})
	accounts, err := svc.ListUsageAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = svc.BuildLiveAnalyticsAccountLimits(ctx, persistence.UsageFilter{AccountGroupKey: accounts[0].Key})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not start")
	}
	readCtx, stopRead := context.WithTimeout(context.Background(), time.Second)
	defer stopRead()
	if _, err := env.Tasks.GetByID(readCtx, task.ID); err != nil {
		t.Fatalf("unrelated database read blocked by provider: %v", err)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("provider request not cancelled")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("live request did not finish")
	}
}

func TestLiveAccountUsageCredentialFallbackAndReconnect(t *testing.T) {
	for _, reconnect := range []bool{false, true} {
		name := "fallback"
		if reconnect {
			name = "reconnect"
		}
		t.Run(name, func(t *testing.T) {
			env := teststores.New(t)
			ctx := context.Background()
			for _, name := range []string{"first", "second"} {
				cfg := models.LLMConfig{Name: name, Provider: models.ProviderOpenAI, Model: "test", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: name, OAuthAccountID: "same-account", OAuthExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
				if err := env.LLMConfigs.Create(ctx, &cfg); err != nil {
					t.Fatal(err)
				}
			}
			svc := NewUsageAnalyticsService(liveUsageWithoutStorage{}, env.LLMConfigs)
			var calls []string
			reconnected := false
			svc.SetAccountUsageFetcher(func(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageResult, error) {
				calls = append(calls, cfg.ID)
				if len(calls) == 1 {
					return nil, errors.New("first credential unavailable")
				}
				if reconnect {
					changed, err := env.LLMConfigs.UpdateStandardOAuthConnectionIfRevision(ctx, cfg.ID, cfg.OAuthConfigRevision, cfg.Provider, "new-token", "new-refresh", time.Now().Add(time.Hour).UnixMilli(), "new-account")
					if err != nil || !changed {
						return nil, errors.New("test reconnect failed")
					}
					reconnected = true
				}
				pct := 42.0
				return &models.AccountUsageResult{PrimaryLabel: "Session", PrimaryUsedPercent: &pct}, nil
			})
			view, err := svc.BuildLiveAnalyticsAccountLimits(ctx, persistence.UsageFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if len(calls) != 2 || calls[0] == calls[1] {
				t.Fatalf("credential fallback calls: %v", calls)
			}
			if len(view.AccountLimits) != 1 {
				t.Fatalf("cards: %+v", view.AccountLimits)
			}
			card := view.AccountLimits[0]
			if reconnect {
				if !reconnected {
					t.Fatal("test did not reconnect the account")
				}
				if card.Error != "Usage unavailable" || len(card.Limits) != 0 {
					t.Fatalf("stale response rendered after reconnect: %+v", card)
				}
			} else if card.Error != "" || len(card.Limits) != 1 {
				t.Fatalf("healthy fallback missing: %+v", card)
			}
		})
	}
}

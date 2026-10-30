package service

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type refreshGatedConfigs struct {
	persistence.LLMConfigStore
	gate  chan struct{}
	block atomic.Bool
}

func (r *refreshGatedConfigs) List(ctx context.Context) ([]models.LLMConfig, error) {
	if r.block.CompareAndSwap(true, false) {
		<-r.gate
	}
	return r.LLMConfigStore.List(ctx)
}
func TestAccountLimitsForcedRefreshDoesNotJoinCachedRead(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()
	cfg := models.LLMConfig{Name: "test", Provider: models.ProviderOpenAI, Model: "gpt-6.1-sol", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "fixture", OAuthAccountID: "test-account", OAuthExpiresAt: time.Now().Add(4 * time.Hour).UnixMilli()}
	if err := env.LLMConfigs.Create(ctx, &cfg); err != nil {
		t.Fatal(err)
	}
	repo := &refreshGatedConfigs{LLMConfigStore: env.LLMConfigs, gate: make(chan struct{})}
	svc := NewUsageAnalyticsService(env.Usage, repo)
	calls := 0
	pct := 20.0
	svc.SetAccountUsageFetcher(func(ctx context.Context, c models.LLMConfig) (*models.AccountUsageSnapshot, error) {
		calls++
		return &models.AccountUsageSnapshot{Provider: "openai", AgentConfigID: c.ID, AccountID: c.OAuthAccountID, PrimaryLabel: "Session", PrimaryUsedPercent: &pct}, nil
	})
	accounts, err := svc.ListUsageAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := accounts[0].Key
	if _, err := svc.BuildAnalyticsAccountLimits(ctx, persistence.UsageFilter{AccountGroupKey: key, Refresh: true}); err != nil {
		t.Fatal(err)
	}
	pct = 96
	repo.block.Store(true)
	synctest.Test(t, func(t *testing.T) {
		repo.gate = make(chan struct{})
		done := make(chan error, 2)
		go func() {
			_, err := svc.BuildAnalyticsAccountLimits(ctx, persistence.UsageFilter{AccountGroupKey: key})
			done <- err
		}()
		synctest.Wait()
		go func() {
			_, err := svc.BuildAnalyticsAccountLimits(ctx, persistence.UsageFilter{AccountGroupKey: key, Refresh: true})
			done <- err
		}()
		synctest.Wait()
		close(repo.gate)
		synctest.Wait()
		for i := 0; i < 2; i++ {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
	})
	if calls != 2 {
		t.Fatalf("forced refresh did not fetch provider: calls=%d, want 2 (prime + refresh)", calls)
	}
}
